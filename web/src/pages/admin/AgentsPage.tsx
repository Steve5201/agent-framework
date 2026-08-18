import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { adminCreateAgent, adminDeleteAgent, adminListAgents, adminSetAgentStatus } from '@/lib/api'
import type { Agent } from '@/types/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'
import { Bot, ChevronRight, Loader2, Plus, RefreshCw, ShieldCheck, Square, Play, Trash2, Users } from 'lucide-react'

/** 简单弹窗（管理端复用，点击遮罩不关闭）。 */
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
        className="flex max-h-[85vh] w-full max-w-xl flex-col overflow-hidden rounded-xl border bg-background shadow-2xl"
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

/** 智能体状态 → 徽标（1=启用 0=停用） */
const STATUS_STYLE: Record<number, { label: string; cls: string }> = {
  1: { label: '启用', cls: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-300' },
  0: { label: '停用', cls: 'bg-muted text-muted-foreground' },
}
const STATUS_FALLBACK = { label: '未知', cls: 'bg-muted text-muted-foreground' }

/** 概览小统计卡（列表页顶部）。 */
function MiniStat({ icon, label, value, tone }: { icon: React.ReactNode; label: string; value: string; tone: string }) {
  return (
    <div className="flex items-center gap-2.5 rounded-xl border bg-card px-3.5 py-2.5 shadow-sm">
      <span className={cn('flex size-7 shrink-0 items-center justify-center rounded-md border bg-muted/40', tone)}>{icon}</span>
      <div className="min-w-0">
        <div className="text-[11px] text-muted-foreground">{label}</div>
        <div className={cn('text-sm font-semibold tabular-nums', tone)}>{value}</div>
      </div>
    </div>
  )
}

/**
 * 智能体管理（仅最高超管）：智能体注册表 + 绑定各智能体的超管（agent_admin）。
 * 创建智能体时若指定 owner_user_id，该用户会被授予 agent_admin 并绑定该智能体。
 */
export default function AgentsPage() {
  const navigate = useNavigate()
  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  /** 行内操作（启停/删除）进行中的智能体 ID */
  const [busyId, setBusyId] = useState('')

  // 创建弹窗
  const [creating, setCreating] = useState(false)
  const [form, setForm] = useState({ id: '', name: '', description: '', model: '', owner_user_id: '' })
  const [busy, setBusy] = useState(false)
  const [saveError, setSaveError] = useState('')

  const load = useCallback(() => {
    adminListAgents()
      .then((list) => {
        setAgents(list)
        setError('')
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  function openCreate() {
    setForm({ id: '', name: '', description: '', model: '', owner_user_id: '' })
    setSaveError('')
    setCreating(true)
  }

  async function submit() {
    const id = form.id.trim()
    const name = form.name.trim()
    if (!id) {
      setSaveError('智能体 ID 不能为空（仅限字母/数字/中划线，≤64 字符）')
      return
    }
    if (!/^[A-Za-z0-9-]{1,64}$/.test(id)) {
      setSaveError('智能体 ID 仅限字母/数字/中划线，≤64 字符')
      return
    }
    if (!name) {
      setSaveError('智能体名称不能为空')
      return
    }
    setBusy(true)
    setSaveError('')
    try {
      await adminCreateAgent({
        id,
        name,
        description: form.description.trim() || undefined,
        model: form.model.trim() || undefined,
        owner_user_id: form.owner_user_id.trim() || undefined,
      })
      setCreating(false)
      setNotice(`智能体「${name}」创建成功`)
      void load()
    } catch (e) {
      setSaveError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  async function toggleStatus(a: Agent) {
    const next: 0 | 1 = a.status === 1 ? 0 : 1
    setBusyId(a.id)
    try {
      await adminSetAgentStatus(a.id, next)
      setNotice(next === 1 ? `智能体「${a.name}」已启用` : `智能体「${a.name}」已停用`)
      void load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusyId('')
    }
  }

  async function removeAgent(a: Agent) {
    if (!window.confirm(`确认软删除智能体「${a.name}」（${a.id}）？删除后该域停止服务，历史会话保留。`)) return
    setBusyId(a.id)
    try {
      await adminDeleteAgent(a.id)
      setNotice(`智能体「${a.name}」已删除`)
      void load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusyId('')
    }
  }

  const enabledCount = agents.filter((a) => a.status === 1).length
  const disabledCount = agents.length - enabledCount

  return (
    <div className="mx-auto max-w-6xl p-6">
      {/* 页头 */}
      <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <div className="flex size-8 items-center justify-center rounded-lg bg-blue-500/15 text-blue-600 dark:text-blue-300">
              <ShieldCheck className="size-4.5" />
            </div>
            <h1 className="text-lg font-semibold tracking-tight">智能体管理</h1>
          </div>
          <p className="mt-1.5 max-w-xl text-xs leading-relaxed text-muted-foreground">
            智能体注册表：每个智能体拥有独立的资源域（技能 / MCP / 知识库），彼此不感知。
            可指定 owner_user_id 将某用户授予 agent_admin 并绑定为该智能体的超管。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={() => { setLoading(true); void load() }} disabled={loading}>
            <RefreshCw className={cn('size-4', loading && 'animate-spin')} /> 刷新
          </Button>
          <Button onClick={openCreate}>
            <Plus className="size-4" /> 新建智能体
          </Button>
        </div>
      </div>

      {error && (
        <div className="mb-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div>
      )}
      {notice && (
        <div className="mb-3 rounded-md border border-primary/30 bg-primary/5 px-3 py-2 text-sm text-primary">{notice}</div>
      )}

      {loading ? (
        <div className="flex justify-center py-16">
          <Loader2 className="size-5 animate-spin text-muted-foreground" />
        </div>
      ) : agents.length === 0 ? (
        <div className="rounded-xl border border-dashed bg-card/50 py-16 text-center">
          <Bot className="mx-auto size-8 text-muted-foreground/50" />
          <p className="mt-2 text-sm text-muted-foreground">暂无智能体。点击右上角「新建智能体」创建第一个。</p>
        </div>
      ) : (
        <>
          {/* 概览统计 */}
          <div className="mb-4 grid grid-cols-3 gap-3">
            <MiniStat icon={<Bot className="size-3.5" />} label="智能体总数" value={String(agents.length)} tone="text-foreground" />
            <MiniStat icon={<Play className="size-3.5" />} label="启用中" value={String(enabledCount)} tone="text-emerald-600 dark:text-emerald-300" />
            <MiniStat icon={<Square className="size-3.5" />} label="已停用" value={String(disabledCount)} tone="text-muted-foreground" />
          </div>

          <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
            <table className="w-full text-sm">
              <thead className="border-b bg-muted/50 text-left text-xs text-muted-foreground">
                <tr>
                  <th className="px-4 py-2.5 font-medium">智能体</th>
                  <th className="px-3 py-2.5 font-medium">描述</th>
                  <th className="px-3 py-2.5 font-medium">模型</th>
                  <th className="px-3 py-2.5 font-medium">超管（owner）</th>
                  <th className="px-3 py-2.5 font-medium">状态</th>
                  <th className="px-4 py-2.5 text-right font-medium">操作</th>
                </tr>
              </thead>
              <tbody>
                {agents.map((a) => {
                  const st = STATUS_STYLE[a.status] ?? STATUS_FALLBACK
                  const busy = busyId === a.id
                  return (
                    <tr
                      key={a.id}
                      onClick={() => navigate(`/admin/agents/${a.id}`)}
                      className="cursor-pointer border-b transition-colors last:border-0 hover:bg-accent/40"
                    >
                      <td className="px-4 py-2.5">
                        <div className="flex items-center gap-2.5">
                          <div className="flex size-8 shrink-0 items-center justify-center rounded-lg border bg-muted/40 text-sm" aria-hidden>
                            {a.avatar || a.name.charAt(0).toUpperCase()}
                          </div>
                          <div className="min-w-0">
                            <div className="truncate font-medium">{a.name}</div>
                            <div className="truncate font-mono text-[11px] text-muted-foreground">{a.id}</div>
                          </div>
                        </div>
                      </td>
                      <td className="max-w-[240px] truncate px-3 py-2.5 text-xs text-muted-foreground" title={a.description}>
                        {a.description || '-'}
                      </td>
                      <td className="px-3 py-2.5 font-mono text-xs">{a.model || '实例默认'}</td>
                      <td className="max-w-[160px] px-3 py-2.5">
                        <div className="flex items-center gap-1.5 truncate font-mono text-xs text-muted-foreground" title={a.owner_user_id}>
                          {a.owner_user_id ? <Users className="size-3 shrink-0" /> : null}
                          <span className="truncate">{a.owner_user_id || '未绑定'}</span>
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <Badge variant="outline" className={cn('text-[10px]', st.cls)}>
                          <span
                            className={cn('mr-1.5 inline-block size-1.5 rounded-full', a.status === 1 ? 'bg-emerald-500' : 'bg-muted-foreground/50')}
                          />
                          {st.label}
                        </Badge>
                      </td>
                      <td className="px-4 py-2.5">
                        <div className="flex items-center justify-end gap-1.5">
                          <Button
                            variant="ghost"
                            size="sm"
                            className="h-7 px-2 text-xs"
                            onClick={(e) => {
                              e.stopPropagation()
                              navigate(`/admin/agents/${a.id}`)
                            }}
                          >
                            <ChevronRight className="size-3.5" /> 详情
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            className="h-7 px-2 text-xs"
                            disabled={busy}
                            onClick={(e) => {
                              e.stopPropagation()
                              void toggleStatus(a)
                            }}
                          >
                            {a.status === 1 ? <Square className="size-3" /> : <Play className="size-3" />}
                            {a.status === 1 ? '停用' : '启用'}
                          </Button>
                          {a.id !== 'tutor' && (
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 px-2 text-xs text-destructive hover:text-destructive"
                              disabled={busy}
                              onClick={(e) => {
                                e.stopPropagation()
                                void removeAgent(a)
                              }}
                            >
                              <Trash2 className="size-3" /> 删除
                            </Button>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </>
      )}

      {/* 新建智能体弹窗 */}
      {creating && (
        <Modal
          title="新建智能体"
          subtitle="创建后该智能体拥有独立的资源域（技能 / MCP / 知识库 / 用户组）"
          footer={
            <>
              <Button variant="outline" onClick={() => setCreating(false)} disabled={busy}>
                取消
              </Button>
              <Button onClick={() => void submit()} disabled={busy}>
                {busy ? <Loader2 className="size-4 animate-spin" /> : '创建'}
              </Button>
            </>
          }
        >
          <div className="space-y-3">
            {saveError && <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{saveError}</div>}
            <div className="space-y-1.5">
              <Label htmlFor="ag-id">智能体 ID（唯一，创建后不可改）</Label>
              <Input
                id="ag-id"
                value={form.id}
                maxLength={64}
                placeholder="如：math / physics / chemistry"
                onChange={(e) => setForm((f) => ({ ...f, id: e.target.value }))}
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="ag-name">名称</Label>
              <Input
                id="ag-name"
                value={form.name}
                maxLength={50}
                placeholder="如：高等数学助教"
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="ag-desc">描述（可选，≤200 字）</Label>
              <Textarea
                id="ag-desc"
                value={form.description}
                maxLength={200}
                rows={2}
                placeholder="该智能体面向的用户与覆盖内容"
                onChange={(e) => setForm((f) => ({ ...f, description: e.target.value }))}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="ag-model">默认模型（可选）</Label>
              <Input
                id="ag-model"
                value={form.model}
                placeholder="如：deepseek-chat"
                onChange={(e) => setForm((f) => ({ ...f, model: e.target.value }))}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="ag-owner">智能体超管 user_id（可选）</Label>
              <Input
                id="ag-owner"
                value={form.owner_user_id}
                placeholder="可留空，稍后在智能体详情页绑定 / 更换"
                onChange={(e) => setForm((f) => ({ ...f, owner_user_id: e.target.value }))}
              />
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}
