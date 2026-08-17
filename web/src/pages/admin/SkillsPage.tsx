import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  ApiError,
  adminCreateSkill,
  adminDeleteSkill,
  adminListSkills,
  adminRestoreSkillVersion,
  adminSetSkillEnabled,
  adminUpdateSkill,
  adminUploadSkill,
} from '@/lib/api'
import type { Skill } from '@/types/api'
import AgentScopeSelect from '@/components/admin/AgentScopeSelect'
import { useAgentScope } from '@/components/admin/useAgentScope'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'
import {
  AlertTriangle,
  Archive,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  FileText,
  FolderOpen,
  History,
  Loader2,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Sparkles,
  Trash2,
  Upload,
  Wrench,
} from 'lucide-react'

/** 新建技能时的 SKILL.md 模板（frontmatter name 须与技能名一致，version 必填 x.y.z） */
const TEMPLATE = (name: string) => `---
name: ${name}
description: 一句话说明什么时候使用这个技能
metadata:
  version: 1.0.0
---

# ${name}

给模型的执行指引：什么时候触发、按什么步骤执行、可引用同目录下的脚本与数据文件。
`

/** 简单弹窗（管理端复用）。点击遮罩不关闭——关闭只能由取消/保存按钮触发，
 *  避免用户误触遮罩丢失已输入内容。 */
function Modal({
  title,
  subtitle,
  children,
  footer,
}: {
  title: string
  subtitle?: string
  children: React.ReactNode
  footer: React.ReactNode
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div
        className="flex max-h-[85vh] w-full max-w-3xl flex-col overflow-hidden rounded-xl border bg-background shadow-2xl"
        role="dialog"
        aria-modal="true"
      >
        <div className="border-b px-5 py-3.5">
          <div className="text-sm font-semibold">{title}</div>
          {subtitle && <div className="mt-0.5 text-xs text-muted-foreground">{subtitle}</div>}
        </div>
        <div className="flex-1 overflow-y-auto p-5">{children}</div>
        <div className="flex items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">
          {footer}
        </div>
      </div>
    </div>
  )
}

/** 技能名首字头像配色（静态类名数组，保证 Tailwind 可扫描到）。 */
const AVATAR_COLORS = [
  'bg-indigo-500/15 text-indigo-600 dark:text-indigo-300',
  'bg-emerald-500/15 text-emerald-600 dark:text-emerald-300',
  'bg-amber-500/15 text-amber-600 dark:text-amber-300',
  'bg-rose-500/15 text-rose-600 dark:text-rose-300',
  'bg-sky-500/15 text-sky-600 dark:text-sky-300',
  'bg-violet-500/15 text-violet-600 dark:text-violet-300',
]

function avatarColor(name: string) {
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0
  return AVATAR_COLORS[h % AVATAR_COLORS.length]
}

/** 轻量解析 SKILL.md frontmatter（name/description/version），用于编辑器实时预览。
 *  与后端 YAML 解析尽力对齐：键大小写不敏感、允许缩进/引号、支持 metadata.version。
 *  注：此处只是"实时预览"，保存/上传时以后端权威校验为准。 */
function parseSkillFrontmatter(content: string): { name: string; description: string; version: string } {
  const out = { name: '', description: '', version: '' }
  let body = content.replace(/^\uFEFF/, '')
  const block = /^\s*---\s*\r?\n([\s\S]*?)\r?\n\s*---\s*/.exec(body)
  if (!block) return out
  body = block[1]
  for (const raw of body.split('\n')) {
    const line = raw.replace(/\r$/, '')
    const mm = /^\s*([A-Za-z0-9_]+)\s*:\s*(.*)$/.exec(line)
    if (!mm) continue
    const key = mm[1].toLowerCase()
    const val = mm[2].trim().replace(/^['"]|['"]$/g, '').trim()
    if (val === '|' || val.startsWith('|')) continue // 块标量，跳过
    if (key === 'name' && !out.name) out.name = val
    else if (key === 'description' && !out.description) out.description = val
    else if (key === 'version' && !out.version) out.version = val
  }
  // metadata.version 优先（Anthropic 规范），取最后一个 version 行
  const versions = [...body.matchAll(/^\s*version\s*:\s*(.+)$/gim)].map((m) => m[1].trim().replace(/^['"]|['"]$/g, '').trim())
  if (versions.length > 0) out.version = versions[versions.length - 1]
  return out
}

type StatusFilter = 'all' | 'enabled' | 'disabled' | 'invalid'

export default function SkillsPage({ fixedAgentId }: { fixedAgentId?: string } = {}) {
  const { agentId, canScope, setAgentId, agents } = useAgentScope(fixedAgentId)
  const [skills, setSkills] = useState<Skill[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  // 列表过滤
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState<StatusFilter>('all')
  const [expanded, setExpanded] = useState<string | null>(null) // 展开文件结构的技能名

  // 新建/编辑弹窗状态
  const [modal, setModal] = useState<'create' | 'edit' | 'upload' | null>(null)
  const [name, setName] = useState('')
  const [content, setContent] = useState('')
  const [editing, setEditing] = useState<Skill | null>(null)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  // 上传弹窗状态：只选文件，名字/版本由后端从 SKILL.md 自动提取
  const [zipFile, setZipFile] = useState<File | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const load = useCallback(() => {
    adminListSkills(agentId)
      .then((list) => {
        setSkills(list)
        setError('')
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false))
  }, [agentId])

  useEffect(() => {
    void load()
  }, [load])

  const filtered = useMemo(() => {
    const kw = search.trim().toLowerCase()
    return skills.filter((s) => {
      if (filter === 'enabled' && !s.enabled) return false
      if (filter === 'disabled' && s.enabled) return false
      if (filter === 'invalid' && s.valid) return false
      if (kw && !`${s.name} ${s.description}`.toLowerCase().includes(kw)) return false
      return true
    })
  }, [skills, search, filter])

  const stats = useMemo(
    () => ({
      total: skills.length,
      enabled: skills.filter((s) => s.enabled).length,
      invalid: skills.filter((s) => !s.valid).length,
    }),
    [skills],
  )

  // 编辑器中实时解析 frontmatter，给出校验反馈。
  const fm = useMemo(() => parseSkillFrontmatter(content), [content])
  const fmIssues = useMemo(() => {
    const issues: string[] = []
    if (!fm.name) issues.push('缺 name（技能名，须与上方"技能名"一致）')
    if (!fm.description) issues.push('缺 description（模型判断何时使用的说明）')
    if (!fm.version) issues.push('缺 metadata.version（x.y.z 语义版本号，必填）')
    else if (!/^\d+\.\d+\.\d+$/.test(fm.version)) issues.push(`version "${fm.version}" 不是 x.y.z 格式`)
    if (fm.name && name.trim() && fm.name !== name.trim()) issues.push(`frontmatter name（${fm.name}）与技能名不一致`)
    return issues
  }, [fm, name])

  function openCreate() {
    setName('')
    setContent(TEMPLATE('my-skill'))
    setSaveError('')
    setEditing(null)
    setModal('create')
  }

  function openEdit(sk: Skill) {
    setEditing(sk)
    setName(sk.name)
    setContent(sk.content)
    setSaveError('')
    setModal('edit')
  }

  function openUpload() {
    setZipFile(null)
    setSaveError('')
    setModal('upload')
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  async function save() {
    const trimmedName = name.trim()
    const trimmedContent = content.trim()
    if (!trimmedName || !trimmedContent) {
      setSaveError('技能名与 SKILL.md 内容都不能为空')
      return
    }
    setSaving(true)
    setSaveError('')
    try {
      if (modal === 'create') {
        await adminCreateSkill(trimmedName, trimmedContent, agentId)
        setNotice(`技能「${trimmedName}」创建成功`)
      } else {
        await adminUpdateSkill(trimmedName, trimmedContent, false, agentId)
        setNotice(`技能「${trimmedName}」已保存`)
      }
      setModal(null)
      void load()
    } catch (e) {
      if (modal === 'create' && e instanceof ApiError && e.code === 'ALREADY_EXISTS') {
        // 按"名字+版本号"组合区分冲突：同版本=覆盖当前副本；版本不同=发布新版本切换生效。
        const fm = parseSkillFrontmatter(trimmedContent)
        const prev = skills.find((s) => s.name === trimmedName)
        const sameVer = !!fm.version && !!prev && prev.semver === fm.version
        const confirmMsg = sameVer
          ? `技能「${trimmedName}」已存在且版本号相同（${fm.version}）。继续将覆盖当前副本并切换为生效版本，是否继续？`
          : `技能「${trimmedName}」已存在（当前版本 ${prev?.semver || '未知'}）。版本号 ${fm.version || '（未识别）'} 为新版本：继续将发布为新版本并切换为生效版本（原版本自动进入历史可回滚），是否继续？`
        if (window.confirm(confirmMsg)) {
          try {
            await adminUpdateSkill(trimmedName, trimmedContent, true, agentId)
            setNotice(
              sameVer
                ? `技能「${trimmedName}」已覆盖当前副本并切换为生效版本`
                : `技能「${trimmedName}」已发布新版本并切换为生效版本（原版本进入历史可回滚）`,
            )
            setModal(null)
            void load()
          } catch (e2) {
            setSaveError((e2 as Error).message)
          }
        }
      } else if (modal === 'edit') {
        await handleVersionConflict(e, trimmedName, trimmedContent)
      } else {
        setSaveError((e as Error).message)
      }
    } finally {
      setSaving(false)
    }
  }

  /** 版本冲突（同版本号内容不同）：询问是否覆盖该版本，确认后带 overwrite 重试。 */
  async function handleVersionConflict(e: unknown, skillName: string, skillContent: string) {
    if (e instanceof ApiError && e.code === 'VERSION_CONFLICT') {
      if (window.confirm(`技能「${skillName}」存在同版本号但内容不同。是否覆盖该版本？（旧内容自动进入历史版本，可回滚）`)) {
        try {
          await adminUpdateSkill(skillName, skillContent, true, agentId)
          setNotice(`技能「${skillName}」同版本已覆盖，历史版本保留`)
          setModal(null)
          void load()
        } catch (e2) {
          setSaveError((e2 as Error).message)
        }
      }
    } else {
      setSaveError((e as Error).message)
    }
  }

  /** 上传成功后的统一收尾：按新旧版本关系给出精确反馈。 */
  function finishUpload(sk: Skill) {
    const prev = skills.find((s) => s.name === sk.name)
    let msg: string
    if (!sk.valid) {
      msg = `技能「${sk.name}」已上传但解析失败：${sk.error}`
    } else if (!prev) {
      msg = `技能「${sk.name}」创建成功${sk.semver ? `（版本 ${sk.semver}）` : ''}`
    } else if (prev.semver === sk.semver) {
      msg = `技能「${sk.name}」已覆盖当前副本${sk.semver ? ` v${sk.semver}` : ''}并切换为生效版本`
    } else {
      msg = `技能「${sk.name}」已发布新版本${sk.semver ? ` v${sk.semver}` : ''}${prev.semver ? `（原 v${prev.semver} 进入历史）` : ''}并切换为生效版本`
    }
    if (sk.files?.length) msg += `，含 ${sk.files.length} 个附属文件`
    setNotice(msg)
    setModal(null)
    void load()
  }

  async function saveUpload() {
    if (!zipFile) {
      setSaveError('请选择 zip 文件')
      return
    }
    setSaving(true)
    setSaveError('')
    try {
      const sk = await adminUploadSkill(zipFile, false, agentId)
      finishUpload(sk)
    } catch (e) {
      if (e instanceof ApiError && e.code === 'VERSION_CONFLICT') {
        // 后端已返回精确文案（同版本覆盖 / 新版本发布切换生效），直接展示并二次确认；
        // 确认后带 overwrite=true 重试。同名同版本同内容也会走到这里——不再静默幂等。
        if (window.confirm(`${(e as Error).message}\n\n确认后继续上传？`)) {
          try {
            finishUpload(await adminUploadSkill(zipFile, true, agentId))
          } catch (e2) {
            setSaveError((e2 as Error).message)
          }
        }
      } else {
        setSaveError((e as Error).message)
      }
    } finally {
      setSaving(false)
    }
  }

  async function remove(sk: Skill) {
    if (!window.confirm(`删除技能「${sk.name}」？该技能将被移除，模型不再具备此能力。`)) return
    try {
      await adminDeleteSkill(sk.name, agentId)
      void load()
    } catch (e) {
      alert(`删除失败：${(e as Error).message}`)
    }
  }

  async function toggleEnabled(sk: Skill) {
    try {
      await adminSetSkillEnabled(sk.name, !sk.enabled, agentId)
      void load()
    } catch (e) {
      alert(`${sk.enabled ? '禁用' : '启用'}失败：${(e as Error).message}`)
    }
  }

  async function restoreVersion(sk: Skill, semver: string) {
    if (!window.confirm(`将技能「${sk.name}」回滚到版本 ${semver}？当前内容会作为历史版本保留（同版本号只留一份）。`)) return
    try {
      await adminRestoreSkillVersion(sk.name, semver, agentId)
      setModal(null)
      void load()
    } catch (e) {
      alert(`回滚失败：${(e as Error).message}`)
    }
  }

  const FILTER_TABS: { key: StatusFilter; label: string }[] = [
    { key: 'all', label: `全部 ${stats.total}` },
    { key: 'enabled', label: `已启用 ${stats.enabled}` },
    { key: 'disabled', label: `已禁用 ${stats.total - stats.enabled}` },
    { key: 'invalid', label: `异常 ${stats.invalid}` },
  ]

  return (
    <div className="mx-auto max-w-5xl p-6">
      {/* 页头 */}
      <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <div className="flex size-8 items-center justify-center rounded-lg bg-indigo-500/15 text-indigo-600 dark:text-indigo-300">
              <Wrench className="size-4.5" />
            </div>
            <h1 className="text-lg font-semibold tracking-tight">技能管理</h1>
          </div>
          <p className="mt-1.5 max-w-xl text-xs leading-relaxed text-muted-foreground">
            Anthropic Agent Skills 格式（目录 + SKILL.md）。保存后 agent 热加载生效，无需重启。
            上传 zip 自动提取技能名与版本号；支持中文名与多文件目录结构。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <AgentScopeSelect agentId={agentId} agents={agents} onChange={setAgentId} />
          <Button variant="outline" size="sm" onClick={() => { setLoading(true); void load() }} disabled={loading}>
            <RefreshCw className={cn('size-3.5', loading && 'animate-spin')} /> 刷新
          </Button>
          <Button variant="outline" onClick={openUpload}>
            <Upload className="size-4" /> 上传技能
          </Button>
          <Button onClick={openCreate}>
            <Plus className="size-4" /> 新建技能
          </Button>
        </div>
      </div>

      {/* 非超管管理员固定资源域提示 */}
      {!canScope && (
        <div className="mb-3 rounded-md border border-muted bg-muted/30 px-3 py-1.5 text-xs text-muted-foreground">
          当前管理的是智能体「{agentId}」的资源（由账号归属决定）。
        </div>
      )}

      {error && (
        <div className="mb-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div>
      )}
      {notice && (
        <div className="mb-3 flex items-center gap-1.5 rounded-md border border-primary/30 bg-primary/5 px-3 py-2 text-sm text-primary">
          <Sparkles className="size-4" /> {notice}
        </div>
      )}

      {/* 统计卡片 */}
      <div className="mb-4 grid grid-cols-3 gap-3">
        <StatCard icon={<Wrench className="size-4" />} label="全部技能" value={stats.total} accent="text-indigo-600 dark:text-indigo-400" />
        <StatCard
          icon={<CheckCircle2 className="size-4" />}
          label="已启用"
          value={stats.enabled}
          accent="text-emerald-600 dark:text-emerald-400"
        />
        <StatCard
          icon={<AlertTriangle className="size-4" />}
          label="解析异常"
          value={stats.invalid}
          accent="text-rose-600 dark:text-rose-400"
        />
      </div>

      {/* 工具栏：搜索 + 状态过滤 */}
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-1 rounded-lg bg-muted/70 p-0.5">
          {FILTER_TABS.map((t) => (
            <button
              key={t.key}
              type="button"
              onClick={() => setFilter(t.key)}
              className={cn(
                'rounded-md px-2.5 py-1 text-xs font-medium transition-colors',
                filter === t.key
                  ? 'bg-background text-foreground shadow-sm'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {t.label}
            </button>
          ))}
        </div>
        <div className="relative w-64">
          <Search className="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="搜索技能名 / 说明…"
            className="h-8 pl-8 text-sm"
          />
        </div>
      </div>

      {loading ? (
        <div className="flex justify-center py-16">
          <Loader2 className="size-5 animate-spin text-muted-foreground" />
        </div>
      ) : filtered.length === 0 ? (
        <div className="rounded-xl border border-dashed bg-card/50 py-16 text-center">
          <div className="mx-auto mb-2 flex size-10 items-center justify-center rounded-full bg-muted text-muted-foreground">
            <FolderOpen className="size-5" />
          </div>
          <p className="text-sm text-muted-foreground">
            {skills.length === 0 ? '暂无技能。点击右上角「新建技能」或「上传技能」。' : '没有匹配的技能。'}
          </p>
        </div>
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/50 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 font-medium">技能</th>
                <th className="px-3 py-2.5 font-medium">说明</th>
                <th className="px-3 py-2.5 font-medium">版本</th>
                <th className="px-3 py-2.5 font-medium">文件</th>
                <th className="px-3 py-2.5 font-medium">状态</th>
                <th className="px-3 py-2.5 font-medium">启用</th>
                <th className="px-4 py-2.5 text-right font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((sk) => (
                <Fragment key={sk.name}>
                <tr
                  className={cn(
                    'group border-b transition-colors last:border-0 hover:bg-accent/40',
                    !sk.enabled && 'opacity-60',
                  )}
                >
                  <td className="px-4 py-2.5">
                    <div className="flex items-center gap-2.5">
                      <span
                        className={cn(
                          'flex size-8 shrink-0 items-center justify-center rounded-lg text-sm font-semibold',
                          avatarColor(sk.name),
                        )}
                      >
                        {sk.name.slice(0, 1)}
                      </span>
                      <div className="min-w-0">
                        <div className="truncate font-medium">{sk.name}</div>
                        <div className="truncate font-mono text-[10px] text-muted-foreground" title={sk.tool_name}>
                          {sk.tool_name}
                        </div>
                      </div>
                    </div>
                  </td>
                  <td className="max-w-[220px] truncate px-3 py-2.5 text-muted-foreground" title={sk.description}>
                    {sk.description || '-'}
                  </td>
                  <td className="px-3 py-2.5">
                    <div className="flex items-center gap-1.5">
                      {sk.semver ? (
                        <span className="rounded-md border border-indigo-500/20 bg-indigo-500/10 px-1.5 py-0.5 font-mono text-[11px] font-medium text-indigo-600 dark:text-indigo-300">
                          v{sk.semver}
                        </span>
                      ) : (
                        <span className="rounded-md border bg-muted px-1.5 py-0.5 font-mono text-[11px]">
                          v{sk.version}
                        </span>
                      )}
                      {(sk.versions?.length ?? 0) > 0 && (
                        <span className="flex items-center gap-0.5 text-[11px] text-muted-foreground" title={`${sk.versions?.length} 个历史版本可回滚`}>
                          <History className="size-3" /> +{sk.versions?.length}
                        </span>
                      )}
                    </div>
                  </td>
                  <td className="px-3 py-2.5">
                    {sk.file_count > 0 ? (
                      <button
                        type="button"
                        title={expanded === sk.name ? '收起文件结构' : '展开文件结构（验证 zip 是否保留目录）'}
                        onClick={() => setExpanded(expanded === sk.name ? null : sk.name)}
                        className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
                      >
                        {expanded === sk.name ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
                        <FileText className="size-3" /> {sk.file_count}
                      </button>
                    ) : (
                      <span className="text-muted-foreground/60">-</span>
                    )}
                  </td>
                  <td className="px-3 py-2.5">
                    {sk.valid ? (
                      <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400">
                        有效
                      </Badge>
                    ) : (
                      <Badge variant="destructive" className="gap-1" title={sk.error}>
                        <AlertTriangle className="size-3" /> 无效
                      </Badge>
                    )}
                  </td>
                  <td className="px-3 py-2.5">
                    <button
                      type="button"
                      role="switch"
                      aria-checked={sk.enabled}
                      title={sk.enabled ? '点击禁用（agent 将移除该工具）' : '点击启用（agent 将注册该工具）'}
                      onClick={() => void toggleEnabled(sk)}
                      className={cn(
                        'relative h-5 w-9 cursor-pointer rounded-full transition-colors',
                        sk.enabled ? 'bg-emerald-500' : 'bg-muted',
                      )}
                    >
                      <span
                        className={cn(
                          'absolute left-0.5 top-0.5 size-4 rounded-full bg-background shadow transition-transform',
                          sk.enabled ? 'translate-x-4' : 'translate-x-0',
                        )}
                      />
                    </button>
                  </td>
                  <td className="px-4 py-2.5 text-right">
                    <div className="flex justify-end gap-0.5 opacity-70 transition-opacity group-hover:opacity-100">
                      <Button variant="ghost" size="icon" title="编辑" onClick={() => openEdit(sk)}>
                        <Pencil className="size-4" />
                      </Button>
                      <Button variant="ghost" size="icon" title="删除" className="text-destructive" onClick={() => void remove(sk)}>
                        <Trash2 className="size-4" />
                      </Button>
                    </div>
                  </td>
                </tr>
                {expanded === sk.name && (
                  <tr className="border-b bg-muted/30 last:border-0">
                    <td colSpan={7} className="px-6 py-3">
                      <div className="flex items-center gap-1 text-[11px] font-medium text-muted-foreground">
                        <FolderOpen className="size-3" />
                        目录结构（相对技能根，可验证 zip 嵌套是否保留）
                      </div>
                      <pre className="mt-2 overflow-x-auto rounded-md bg-background/70 p-3 font-mono text-[11px] leading-relaxed text-muted-foreground">
{`SKILL.md${(sk.files?.length ? '\n' + sk.files.join('\n') : '')}`}
                      </pre>
                    </td>
                  </tr>
                )}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {!loading && stats.invalid > 0 && (
        <div className="mt-3 flex items-center gap-1.5 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
          <AlertTriangle className="size-3.5" />
          有 {stats.invalid} 个技能解析失败（SKILL.md 缺 frontmatter / name 与目录名不一致等），编辑修复后保存即可重新生效。
        </div>
      )}

      {/* 新建 / 编辑弹窗 */}
      {modal && (modal === 'create' || modal === 'edit') && (
        <Modal
          title={modal === 'create' ? '新建技能' : `编辑技能：${name}`}
          subtitle="frontmatter 需含 name / description / metadata.version（x.y.z）；name 必须与技能名一致"
          footer={
            <>
              {saveError && <span className="mr-auto text-xs text-destructive">{saveError}</span>}
              <Button variant="outline" onClick={() => setModal(null)} disabled={saving}>
                取消
              </Button>
              <Button onClick={() => void save()} disabled={saving}>
                {saving ? <Loader2 className="size-4 animate-spin" /> : null} 保存
              </Button>
            </>
          }
        >
          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="skill-name">技能名</Label>
              <Input
                id="skill-name"
                value={name}
                maxLength={50}
                disabled={modal === 'edit'}
                placeholder="如 数据分析助手（支持中文/字母/数字/下划线/连字符）"
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <div className="flex items-center justify-between">
                <Label htmlFor="skill-content">SKILL.md 内容</Label>
                <span className="text-[11px] text-muted-foreground">版本号不同 = 发布新版本；同版本需确认覆盖</span>
              </div>
              <Textarea
                id="skill-content"
                value={content}
                rows={16}
                className="font-mono text-xs leading-relaxed"
                placeholder={'---\nname: my-skill\ndescription: 一句话说明什么时候使用这个技能\nmetadata:\n  version: 1.0.0\n---\n正文'}
                onChange={(e) => setContent(e.target.value)}
              />
            </div>

            {/* 实时 frontmatter 解析预览 */}
            <div className="rounded-lg border bg-muted/30 p-3">
              <div className="mb-2 flex items-center gap-1.5 text-xs font-semibold text-muted-foreground">
                <Sparkles className="size-3.5" /> frontmatter 实时解析
              </div>
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <span className="flex items-center gap-1 rounded-md border bg-background/70 px-2 py-1">
                  名称
                  <span className={cn('font-mono font-medium', fm.name ? 'text-foreground' : 'text-rose-500')}>
                    {fm.name || '（缺失）'}
                  </span>
                </span>
                <span className="flex items-center gap-1 rounded-md border bg-background/70 px-2 py-1">
                  版本
                  <span
                    className={cn(
                      'rounded bg-indigo-500/10 px-1 font-mono text-[11px] text-indigo-600 dark:text-indigo-300',
                      !fm.version && 'bg-rose-500/10 text-rose-500',
                    )}
                  >
                    {fm.version || '（缺失）'}
                  </span>
                </span>
                <span className="flex max-w-[50%] items-center gap-1 truncate rounded-md border bg-background/70 px-2 py-1 text-muted-foreground">
                  说明：{fm.description || '（缺失）'}
                </span>
              </div>
              {fmIssues.length > 0 && (
                <ul className="mt-2 space-y-0.5">
                  {fmIssues.map((msg) => (
                    <li key={msg} className="flex items-center gap-1 text-[11px] text-rose-500">
                      <AlertTriangle className="size-3 shrink-0" /> {msg}
                    </li>
                  ))}
                </ul>
              )}
              {fmIssues.length === 0 && (
                <div className="mt-2 flex items-center gap-1 text-[11px] text-emerald-600 dark:text-emerald-400">
                  <CheckCircle2 className="size-3" /> 校验通过，保存后将按版本号语义发布
                </div>
              )}
            </div>

            {/* 历史版本（编辑态展示，支持回滚） */}
            {modal === 'edit' && editing && (editing.versions?.length ?? 0) > 0 && (
              <div className="rounded-lg border bg-muted/30 p-3">
                <div className="mb-2 flex items-center gap-1.5 text-xs font-semibold text-muted-foreground">
                  <History className="size-3.5" /> 历史版本（回滚会作为新版本留痕）
                </div>
                <ul className="space-y-1">
                  {editing.versions?.map((v) => (
                    <li key={v.semver} className="flex items-center justify-between text-xs">
                      <span className="font-mono">
                        <span className="rounded bg-indigo-500/10 px-1 py-px text-[10px] text-indigo-600 dark:text-indigo-300">
                          {v.semver}
                        </span>
                        <span className="ml-2 text-muted-foreground">
                          {new Date(v.updated_at).toLocaleString()} · {v.size} B
                        </span>
                      </span>
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-6 px-2 text-xs"
                        disabled={saving}
                        onClick={() => void restoreVersion(editing, v.semver)}
                      >
                        回滚到 {v.semver}
                      </Button>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        </Modal>
      )}

      {/* 上传 zip 弹窗：只选文件，名字/版本自动提取 */}
      {modal === 'upload' && (
        <Modal
          title="上传技能（zip 多文件包）"
          subtitle="无需填写名字与版本——系统从包内 SKILL.md 自动提取"
          footer={
            <>
              {saveError && <span className="mr-auto text-xs text-destructive">{saveError}</span>}
              <Button variant="outline" onClick={() => setModal(null)} disabled={saving}>
                取消
              </Button>
              <Button onClick={() => void saveUpload()} disabled={saving || !zipFile}>
                {saving ? <Loader2 className="size-4 animate-spin" /> : null} 上传
              </Button>
            </>
          }
        >
          <div className="space-y-4">
            <label
              className={cn(
                'flex cursor-pointer flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed px-6 py-10 text-center transition-colors',
                zipFile
                  ? 'border-indigo-500/40 bg-indigo-500/5'
                  : 'border-border hover:border-indigo-500/40 hover:bg-accent/40',
              )}
            >
              <input
                ref={fileInputRef}
                type="file"
                accept=".zip"
                className="hidden"
                onChange={(e) => {
                  setZipFile(e.target.files?.[0] ?? null)
                  setSaveError('')
                }}
              />
              {zipFile ? (
                <>
                  <Archive className="size-7 text-indigo-500" />
                  <span className="text-sm font-medium">{zipFile.name}</span>
                  <span className="text-xs text-muted-foreground">{(zipFile.size / 1024).toFixed(1)} KB · 点击可重新选择</span>
                </>
              ) : (
                <>
                  <Upload className="size-7 text-muted-foreground" />
                  <span className="text-sm font-medium">点击选择 zip 文件</span>
                  <span className="text-xs text-muted-foreground">或拖拽文件到此处（浏览器支持）</span>
                </>
              )}
            </label>
            <div className="space-y-1 rounded-lg bg-muted/50 p-3 text-xs leading-relaxed text-muted-foreground">
              <p>
                <span className="font-medium text-foreground">自动提取：</span>
                <code className="font-mono">name</code>（缺失时取包裹目录名 / 文件名）与{' '}
                <code className="font-mono">metadata.version</code>（x.y.z，必填，缺失将拒绝上传）。
              </p>
              <p>
                <span className="font-medium text-foreground">结构保留：</span>
                zip 内完整目录结构原样解压（SKILL.md 可位于任意层级；docs/、ref/、scripts/ 等子目录及跨目录相对引用均保留）。单包 ≤ 50MB。
              </p>
              <p>
                <span className="font-medium text-foreground">版本语义：</span>
                上传同名技能时，版本号不同 = 发布新版本；同版本号但内容不同需确认覆盖（旧版本保留可回滚）。
              </p>
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}

/** 统计卡片。 */
function StatCard({
  icon,
  label,
  value,
  accent,
}: {
  icon: React.ReactNode
  label: string
  value: number
  accent: string
}) {
  return (
    <div className="flex items-center gap-3 rounded-xl border bg-card px-4 py-3">
      <div className={cn('flex size-9 items-center justify-center rounded-lg bg-muted', accent)}>{icon}</div>
      <div>
        <div className="text-lg font-semibold leading-tight tabular-nums">{value}</div>
        <div className="text-xs text-muted-foreground">{label}</div>
      </div>
    </div>
  )
}
