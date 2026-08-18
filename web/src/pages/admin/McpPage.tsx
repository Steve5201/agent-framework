import { Fragment, useCallback, useEffect, useRef, useState } from 'react'
import {
  ApiError,
  adminCreateMcpServer,
  adminDeleteMcpServer,
  adminListMcpServers,
  adminSetMcpEnabled,
  adminTestMcpConfig,
  adminTestMcpServer,
  adminUpdateMcpServer,
  adminUploadMcpServer,
} from '@/lib/api'
import type { McpServer } from '@/types/api'
import AgentScopeSelect from '@/components/admin/AgentScopeSelect'
import { useAgentScope } from '@/components/admin/useAgentScope'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import {
  AlertTriangle,
  Archive,
  Braces,
  ChevronDown,
  FileJson,
  Loader2,
  Pencil,
  Plug,
  Plus,
  RefreshCw,
  Server,
  Sparkles,
  Trash2,
  Upload,
  Wrench,
} from 'lucide-react'
import { cn } from '@/lib/utils'

/** 工具确认级别下拉选项（与后端 mcp.parsePermission 对应，空 = L2） */
const PERMISSIONS: { value: string; label: string }[] = [
  { value: 'L0', label: 'L0 纯计算（无副作用，直接执行）' },
  { value: 'L1', label: 'L1 只读（读取文件/查询，不改状态）' },
  { value: 'L2', label: 'L2 写操作（修改状态，需用户确认）' },
  { value: 'L3', label: 'L3 危险（执行脚本/联网/删除，谨慎）' },
]

/** 简单弹窗（管理端复用）。点击遮罩不关闭——关闭只能由取消/保存按钮触发。 */
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
        className="flex max-h-[85vh] w-full max-w-2xl flex-col overflow-hidden rounded-xl border bg-background shadow-2xl"
        role="dialog"
        aria-modal="true"
      >
        <div className="border-b px-5 py-3.5">
          <div className="text-[15px] font-semibold">{title}</div>
          {subtitle && <div className="mt-0.5 text-xs text-muted-foreground">{subtitle}</div>}
        </div>
        <div className="flex-1 overflow-y-auto p-5">{children}</div>
        <div className="flex items-center justify-end gap-2 border-t bg-muted/30 px-5 py-3">{footer}</div>
      </div>
    </div>
  )
}

/** 表格型 KV 编辑器：文本区每行一条 key=value（可空列表）。 */
function KvEditor({
  value,
  onChange,
  label,
  rows,
}: {
  value: Record<string, string>
  onChange: (v: Record<string, string>) => void
  label: string
  rows: number
}) {
  const text = Object.entries(value ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')

  function onEdit(next: string) {
    const out: Record<string, string> = {}
    for (const line of next.split('\n')) {
      const i = line.indexOf('=')
      if (i <= 0) continue
      const k = line.slice(0, i).trim()
      if (k) out[k] = line.slice(i + 1)
    }
    onChange(out)
  }

  return (
    <div className="space-y-1">
      <Label>{label}</Label>
      <Textarea rows={rows} value={text} onChange={(e) => onEdit(e.target.value)} placeholder={'每行一条 key=value'} />
    </div>
  )
}

/** 由表单态构建提交体（做必填校验，错误抛 string）。 */
function buildPayload(form: McpServer): McpServer {
  const name = form.name.trim()
  if (!name) throw new Error('server 名不能为空')
  const payload: McpServer = {
    name,
    transport: form.transport,
    enabled: form.enabled,
    timeout_seconds: form.timeout_seconds || undefined,
    default_permission: form.default_permission || 'L2',
  }
  if (form.transport === 'stdio') {
    if (!form.command?.trim()) throw new Error('stdio 传输必须提供启动命令（command）')
    payload.command = form.command.trim()
    payload.args = (form.args ?? []).filter((a) => a.trim() !== '')
    payload.cwd = form.cwd?.trim() || undefined
    payload.env = form.env
  } else {
    if (!form.url?.trim()) throw new Error('http 传输必须提供 endpoint 地址（url）')
    payload.url = form.url.trim()
    payload.headers = form.headers
  }
  return payload
}

/** 校验单个 server 配置并归一化（标准格式下 name 用 key 注入）。 */
function validateMcpPayload(item: unknown, label: string, fallbackName = ''): McpServer {
  if (!item || typeof item !== 'object' || Array.isArray(item)) {
    throw new Error(`JSON 中 ${label} 不是合法的配置对象`)
  }
  const s = item as McpServer
  const name = (s.name ?? fallbackName ?? '').trim()
  if (!name) throw new Error(`${label} 缺少名字（标准 mcpServers 格式用 key 作为名字，无需写 name）`)
  const transport = s.transport || 'stdio'
  if (transport !== 'stdio' && transport !== 'http') throw new Error(`${label} 的 transport 只能为 "stdio" 或 "http"`)
  if (transport === 'stdio' && !s.command?.trim()) throw new Error(`${label} 的 stdio 传输必须提供 command`)
  if (transport === 'http' && !s.url?.trim()) throw new Error(`${label} 的 http 传输必须提供 url`)
  return { ...s, name, transport }
}

/** 解析 MCP 配置文本，兼容三种业界格式，返回 payload 数组：
 *  ① 单 server 对象（本项目表单/JSON 模式）
 *  ② JSON 数组
 *  ③ 标准对象 `{ "mcpServers": { name: { command/args/cwd… } } }`
 *     （Claude Desktop / trae / workbuddy；也接受无 mcpServers 包装的裸对象） */
function parseMcpConfig(text: string): McpServer[] {
  let obj: unknown
  try {
    obj = JSON.parse(text)
  } catch (e) {
    throw new Error(`JSON 解析失败：${(e as Error).message}`, { cause: e })
  }
  if (obj == null) throw new Error('JSON 为空')

  // ② 数组
  if (Array.isArray(obj)) {
    return (obj as unknown[]).map((item, i) => validateMcpPayload(item, `第 ${i + 1} 个 server`))
  }

  const record = obj as Record<string, unknown>
  // ③ 标准格式：mcpServers 包装
  if (typeof record.mcpServers === 'object' && record.mcpServers !== null) {
    return Object.entries(record.mcpServers as Record<string, unknown>).map(([name, cfg]) =>
      validateMcpPayload(cfg, `server「${name}」`, name),
    )
  }
  // ① 单 server 对象（含 name/transport/command/url 等字段）
  if ('name' in record || 'transport' in record || 'command' in record || 'url' in record) {
    return [validateMcpPayload(obj, 'server')]
  }
  // ③ 裸对象：name -> config
  return Object.entries(record).map(([name, cfg]) => validateMcpPayload(cfg, `server「${name}」`, name))
}

/** 解析 JSON 提交体为表单态（取第一个 server，校验必填）。 */
function parsePayload(text: string): McpServer {
  const list = parseMcpConfig(text)
  if (list.length === 0) throw new Error('JSON 中未包含任何 server 配置')
  return list[0]
}

export default function McpPage({ fixedAgentId }: { fixedAgentId?: string } = {}) {
  const { agentId, canScope, setAgentId, agents } = useAgentScope(fixedAgentId)
  const [servers, setServers] = useState<McpServer[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const [modal, setModal] = useState<'create' | 'edit' | 'upload' | null>(null)
  const [form, setForm] = useState<McpServer>({ name: '', transport: 'stdio', default_permission: 'L2' })
  // 表单 ↔ JSON 双模式：JSON 模式提交体直接编辑（Trae 式配置），表单模式结构化编辑
  const [mode, setMode] = useState<'form' | 'json'>('form')
  const [jsonText, setJsonText] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  /** 编辑态锁定名称：name 决定工具名前缀，一经创建不可修改 */
  const [editingName, setEditingName] = useState('')
  const jsonFileRef = useRef<HTMLInputElement>(null)

  // 未保存配置的"测试连接"结果
  const [testing, setTesting] = useState(false)
  /** 当前展开工具列表的 server 名（null = 全部收起） */
  const [expandedTools, setExpandedTools] = useState<string | null>(null)
  const [testInfo, setTestInfo] = useState<{ tools: { name: string; description?: string }[]; error: string } | null>(null)

  // 上传本地 MCP 代码
  const [uploadZip, setUploadZip] = useState<File | null>(null)
  const [uploadName, setUploadName] = useState('')
  const [uploadEntry, setUploadEntry] = useState('')
  const uploadFileRef = useRef<HTMLInputElement>(null)

  const load = useCallback(() => {
    adminListMcpServers(agentId)
      .then((list) => {
        setServers(list)
        setError('')
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false))
  }, [agentId])

  useEffect(() => {
    void load()
  }, [load])

  function toJson(s: McpServer): string {
    return JSON.stringify(buildPayload(s), null, 2)
  }

  function openCreate() {
    setForm({ name: '', transport: 'http', default_permission: 'L2', enabled: true })
    setMode('form')
    setJsonText('')
    setSaveError('')
    setTestInfo(null)
    setEditingName('')
    setModal('create')
  }

  function openEdit(s: McpServer) {
    setForm({ ...s })
    setJsonText('')
    setSaveError('')
    setTestInfo(null)
    setEditingName(s.name)
    setModal('edit')
    setMode('form')
  }

  function openUpload() {
    setUploadZip(null)
    setUploadName('')
    setUploadEntry('')
    setSaveError('')
    setModal('upload')
    if (uploadFileRef.current) uploadFileRef.current.value = ''
  }

  /** 表单 → JSON：以当前表单值生成提交体 */
  function switchToJson() {
    try {
      setJsonText(toJson(form))
      setMode('json')
      setSaveError('')
    } catch (e) {
      setSaveError((e as Error).message)
    }
  }

  /** JSON → 表单：解析并填充结构化字段 */
  function switchToForm() {
    try {
      setForm(parsePayload(jsonText))
      setMode('form')
      setSaveError('')
    } catch (e) {
      setSaveError((e as Error).message)
    }
  }

  /** 导入 JSON 配置文件：单 server → 预填表单；多 server → 批量创建 */
  function importJson(file: File) {
    const reader = new FileReader()
    reader.onload = () => {
      const text = String(reader.result ?? '')
      try {
        const list = parseMcpConfig(text)
        if (list.length > 1) {
          void createMany(list, '导入文件')
        } else {
          setForm(list[0])
          setJsonText(text)
          setMode('json')
          setSaveError('')
        }
      } catch (e) {
        setSaveError((e as Error).message)
      }
    }
    reader.onerror = () => setSaveError('读取文件失败')
    reader.readAsText(file)
  }

  /** 批量创建多个 server（JSON 导入/粘贴含多 server 时）。已存在则跳过并提示。 */
  async function createMany(list: McpServer[], source: string) {
    const names = list.map((s) => s.name)
    if (!window.confirm(`检测到 ${list.length} 个 MCP server（${names.join('、')}），将以「${source}」内容批量创建。`)) return
    setSaving(true)
    setSaveError('')
    try {
      const results: string[] = []
      for (const p of list) {
        try {
          await adminCreateMcpServer(p, agentId)
          results.push(p.name)
        } catch (e) {
          if (e instanceof ApiError && e.code === 'ALREADY_EXISTS') {
            results.push(`${p.name}（已存在，跳过）`)
          } else {
            throw e
          }
        }
      }
      setModal(null)
      void load()
      setNotice(`已创建：${results.join('、')}`)
    } catch (e) {
      setSaveError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  function collect(): McpServer {
    return mode === 'json' ? parsePayload(jsonText) : buildPayload(form)
  }

  async function save() {
    if (mode === 'json') {
      // JSON 里含多 server → 批量创建（如粘贴整个 mcp.json）。
      const list = parseMcpConfig(jsonText)
      if (list.length > 1) {
        await createMany(list, '粘贴的 JSON')
        return
      }
    }
    let payload: McpServer
    try {
      payload = collect()
    } catch (e) {
      setSaveError((e as Error).message)
      return
    }
    setSaving(true)
    setSaveError('')
    try {
      if (modal === 'create') {
        await adminCreateMcpServer(payload, agentId)
        setNotice(`MCP server「${payload.name}」已创建。启用前请先"测试连接"验证连通。`)
      } else {
        if (payload.name !== editingName) {
          throw new Error('server 名称一经创建不可修改（工具名前缀随之固定）')
        }
        await adminUpdateMcpServer(payload.name, payload, agentId)
        setNotice(`MCP server「${payload.name}」已保存`)
      }
      setModal(null)
      void load()
    } catch (e) {
      // 同名冲突：提示覆盖（= 全量更新）
      if (modal === 'create' && e instanceof ApiError && e.code === 'ALREADY_EXISTS') {
        if (window.confirm(`MCP server「${payload.name}」已存在。是否覆盖其配置？`)) {
          try {
            await adminUpdateMcpServer(payload.name, payload)
            setNotice(`MCP server「${payload.name}」已覆盖`)
            setModal(null)
            void load()
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

  /** 测试当前（尚未保存的）配置：实际连接并列出工具。 */
  async function testConfigNow() {
    let payload: McpServer
    try {
      payload = mode === 'json' ? parseMcpConfig(jsonText)[0] : buildPayload(form)
    } catch (e) {
      setSaveError((e as Error).message)
      return
    }
    setTesting(true)
    setTestInfo(null)
    setSaveError('')
    try {
      const r = await adminTestMcpConfig(payload)
      setTestInfo(r)
    } catch (e) {
      setTestInfo({ tools: [], error: (e as Error).message })
    } finally {
      setTesting(false)
    }
  }

  /** 测试已保存的 server（真实连接 + 工具发现）。 */
  async function testServer(s: McpServer) {
    try {
      const r = await adminTestMcpServer(s.name, agentId)
      if (r.error) {
        setNotice(`「${s.name}」连接失败：${r.error}`)
      } else {
        setNotice(
          `「${s.name}」连接成功，发现 ${r.tools.length} 个工具${r.tools.length ? `：${r.tools.map((t) => t.name).join('、')}` : ''}`,
        )
        setExpandedTools(s.name)
      }
      void load()
    } catch (e) {
      alert(`测试失败：${(e as Error).message}`)
    }
  }

  async function saveUpload() {
    if (!uploadZip) {
      setSaveError('请选择 zip 文件')
      return
    }
    setSaving(true)
    setSaveError('')
    try {
      const cfg = await adminUploadMcpServer(uploadZip, uploadName.trim(), uploadEntry.trim())
      setNotice(`本地 MCP「${cfg.name}」上传成功（${cfg.command} ${cfg.args?.[0] ?? ''}）。请在列表中"测试/启用"验证连通。`)
      setModal(null)
      void load()
    } catch (e) {
      // 同名冲突：默认拒绝覆盖，需确认后带 overwrite=true 重试（与技能上传行为一致）。
      if (e instanceof ApiError && e.code === 'ALREADY_EXISTS') {
        if (window.confirm((e as Error).message + '\n\n确认后将继续上传并覆盖现有代码与配置，是否继续？')) {
          try {
            const cfg = await adminUploadMcpServer(uploadZip, uploadName.trim(), uploadEntry.trim(), true, agentId)
            setNotice(`本地 MCP「${cfg.name}」已覆盖现有代码与配置（${cfg.command} ${cfg.args?.[0] ?? ''}）。`)
            setModal(null)
            void load()
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

  async function remove(s: McpServer) {
    if (!window.confirm(`删除 MCP server「${s.name}」？其全部工具将从 agent 移除。`)) return
    try {
      await adminDeleteMcpServer(s.name)
      void load()
    } catch (e) {
      alert(`删除失败：${(e as Error).message}`)
    }
  }

  async function toggleEnabled(s: McpServer) {
    const enabling = s.enabled !== false
    try {
      const cfg = await adminSetMcpEnabled(s.name, !enabling, agentId)
      if (cfg.discovery_error) {
        setNotice(`「${s.name}」已启用但连接异常：${cfg.discovery_error}`)
      } else if (!enabling && cfg.discovered_tools?.length) {
        setNotice(`「${s.name}」已启用，发现 ${cfg.discovered_tools.length} 个工具`)
      }
      void load()
    } catch (e) {
      alert(`${enabling ? '启用' : '禁用'}失败：${(e as Error).message}`)
    }
  }

  return (
    <div className="mx-auto max-w-6xl p-4 sm:p-6 xl:p-8">
      {/* 页头 */}
      <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2.5">
            <div className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-blue-500/15 text-blue-600 dark:text-blue-300">
              <Server className="size-4.5" />
            </div>
            <p className="max-w-2xl text-xs leading-relaxed text-muted-foreground">
              接入外部 MCP Server，保存后 agent 热加载生效。启用是真实动作——会实际连接并发现工具，连不上会报错。
              支持表单 / JSON / 标准 <code className="font-mono">mcpServers</code> 格式，也可上传本地 MCP 代码包。
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <AgentScopeSelect agentId={agentId} agents={agents} onChange={setAgentId} />
          <Button variant="outline" size="sm" onClick={() => { setLoading(true); void load() }} disabled={loading}>
            <RefreshCw className={cn('size-3.5', loading && 'animate-spin')} /> 刷新
          </Button>
          <Button variant="outline" onClick={openUpload}>
            <Upload className="size-4" /> 上传本地 MCP
          </Button>
          <Button onClick={openCreate}>
            <Plus className="size-4" /> 新建 Server
          </Button>
        </div>
      </div>

      {/* 非超管管理员固定资源域提示 */}
      {!canScope && (
        <div className="mb-3 rounded-md border border-muted bg-muted/30 px-3 py-1.5 text-xs text-muted-foreground">
          当前管理的是智能体「{agentId}」的资源（由账号归属决定）。
        </div>
      )}

      {error && <div className="mb-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div>}
      {notice && (
        <div className="mb-3 flex items-center gap-1.5 rounded-md border border-primary/30 bg-primary/5 px-3 py-2 text-sm text-primary">
          <Sparkles className="size-4 shrink-0" /> <span>{notice}</span>
        </div>
      )}

      {loading ? (
        <div className="flex justify-center py-16">
          <Loader2 className="size-5 animate-spin text-muted-foreground" />
        </div>
      ) : servers.length === 0 ? (
        <div className="rounded-xl border border-dashed bg-card/50 py-16 text-center">
          <div className="mx-auto mb-2 flex size-10 items-center justify-center rounded-full bg-muted text-muted-foreground">
            <Server className="size-5" />
          </div>
          <p className="text-sm text-muted-foreground">暂无 MCP Server。点击右上角「上传本地 MCP」或「新建 Server」接入第一个工具源。</p>
        </div>
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/50 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 font-medium">名称</th>
                <th className="px-3 py-2.5 font-medium">传输</th>
                <th className="px-3 py-2.5 font-medium">端点 / 命令</th>
                <th className="px-3 py-2.5 font-medium">工具</th>
                <th className="px-3 py-2.5 font-medium">权限</th>
                <th className="px-3 py-2.5 font-medium">启用</th>
                <th className="px-4 py-2.5 text-right font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {servers.map((s) => (
                <Fragment key={s.name}>
                <tr
                  className={cn(
                    'group border-b transition-colors last:border-0 hover:bg-accent/40',
                    s.enabled === false && 'opacity-60',
                  )}
                >
                  <td className="px-4 py-2.5">
                    <div className="flex items-center gap-2.5">
                      <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-blue-500/15 text-blue-600 dark:text-blue-300">
                        <Server className="size-4" />
                      </span>
                      <span className="font-medium">{s.name}</span>
                    </div>
                  </td>
                  <td className="px-3 py-2.5">
                    <Badge
                      variant="outline"
                      className={cn(
                        'font-mono text-[11px]',
                        (s.transport || 'stdio') === 'http'
                          ? 'border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-300'
                          : 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-300',
                      )}
                    >
                      {s.transport || 'stdio'}
                    </Badge>
                  </td>
                  <td className="max-w-[220px] truncate px-3 py-2.5 font-mono text-xs text-muted-foreground" title={s.url || s.command}>
                    {s.url || (s.cwd ? `${s.command} (${s.cwd})` : s.command) || '-'}
                  </td>
                  <td className="px-3 py-2.5">
                    {s.discovery_error ? (
                      <Badge variant="destructive" className="gap-1 text-[11px]" title={s.discovery_error}>
                        <AlertTriangle className="size-3" /> 连接失败
                      </Badge>
                    ) : s.discovered_tools?.length ? (
                      <button
                        type="button"
                        onClick={() => setExpandedTools(expandedTools === s.name ? null : s.name)}
                        className="flex items-center gap-1 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
                        title={expandedTools === s.name ? '收起工具列表' : '展开工具列表'}
                      >
                        <Wrench className="size-3 text-emerald-500" />
                        {s.discovered_tools.length} 个工具
                        <ChevronDown
                          className={cn('size-3 transition-transform', expandedTools === s.name && 'rotate-180')}
                        />
                      </button>
                    ) : (
                      <span className="text-[11px] text-muted-foreground/50">未测试</span>
                    )}
                  </td>
                  <td className="px-3 py-2.5 font-mono text-xs">{s.default_permission || 'L2'}</td>
                  <td className="px-3 py-2.5">
                    <button
                      type="button"
                      role="switch"
                      aria-checked={s.enabled !== false}
                      title={s.enabled === false ? '点击启用（实际连接验证）' : '点击禁用（agent 将移除其工具）'}
                      onClick={() => void toggleEnabled(s)}
                      className={cn(
                        'relative h-5 w-9 cursor-pointer rounded-full transition-colors',
                        s.enabled === false ? 'bg-muted' : 'bg-emerald-500',
                      )}
                    >
                      <span
                        className={cn(
                          'absolute left-0.5 top-0.5 size-4 rounded-full bg-background shadow transition-transform',
                          s.enabled === false ? 'translate-x-0' : 'translate-x-4',
                        )}
                      />
                    </button>
                  </td>
                  <td className="px-4 py-2.5 text-right">
                    <div className="flex justify-end gap-0.5 opacity-70 transition-opacity group-hover:opacity-100">
                      <Button variant="ghost" size="icon" title="测试连接（实际连接并发现工具）" onClick={() => void testServer(s)}>
                        <Plug className="size-4" />
                      </Button>
                      <Button variant="ghost" size="icon" title="编辑" onClick={() => openEdit(s)}>
                        <Pencil className="size-4" />
                      </Button>
                      <Button variant="ghost" size="icon" title="删除" className="text-destructive" onClick={() => void remove(s)}>
                        <Trash2 className="size-4" />
                      </Button>
                    </div>
                  </td>
                </tr>
                {expandedTools === s.name && (
                  <tr className="border-b bg-muted/30 last:border-0">
                    <td colSpan={7} className="px-6 py-3">
                      <div className="space-y-2">
                        {s.discovered_tools?.map((t) => (
                          <div key={t.name} className="flex items-start gap-3">
                            <code className="shrink-0 rounded bg-background px-1.5 py-0.5 font-mono text-[11px] text-blue-600 dark:text-blue-300">
                              {t.name}
                            </code>
                            <span className="text-xs leading-5 text-muted-foreground">
                              {t.description || '（无介绍）'}
                            </span>
                          </div>
                        ))}
                        {!s.discovered_tools?.length && (
                          <span className="text-xs text-muted-foreground/60">
                            该 server 未测试或未发现工具，请先「测试连接」。
                          </span>
                        )}
                      </div>
                    </td>
                  </tr>
                )}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* 新建 / 编辑弹窗 */}
      {modal && (modal === 'create' || modal === 'edit') && (
        <Modal
          title={modal === 'create' ? '新建 MCP Server' : `编辑 MCP Server：${form.name}`}
          subtitle="支持 stdio（本地进程）与 http（远程 Streamable HTTP）；可先「测试连接」再保存"
          footer={
            <>
              {saveError && <span className="mr-auto max-w-[50%] truncate text-xs text-destructive">{saveError}</span>}
              <Button variant="outline" onClick={() => setModal(null)} disabled={saving}>
                取消
              </Button>
              <Button onClick={() => void save()} disabled={saving}>
                {saving ? <Loader2 className="size-4 animate-spin" /> : null} 保存
              </Button>
            </>
          }
        >
          {/* 模式切换 + JSON 导入 */}
          <div className="mb-3 flex items-center justify-between">
            <div className="flex items-center gap-1 rounded-md border p-0.5 text-xs">
              <button
                type="button"
                onClick={() => mode === 'json' && switchToForm()}
                className={`cursor-pointer rounded px-2 py-1 ${mode === 'form' ? 'bg-muted font-medium' : 'text-muted-foreground'}`}
              >
                表单模式
              </button>
              <button
                type="button"
                onClick={() => mode === 'form' && switchToJson()}
                className={`cursor-pointer rounded px-2 py-1 ${mode === 'json' ? 'bg-muted font-medium' : 'text-muted-foreground'}`}
              >
                <Braces className="mr-1 inline size-3" />
                JSON 模式
              </button>
            </div>
            <Button variant="outline" size="sm" className="h-7 gap-1 text-xs" onClick={() => jsonFileRef.current?.click()}>
              <FileJson className="size-3.5" /> 导入 JSON
            </Button>
            <input
              ref={jsonFileRef}
              type="file"
              accept=".json,.mcp.json"
              className="hidden"
              onChange={(e) => {
                const f = e.target.files?.[0]
                if (f) importJson(f)
                e.target.value = ''
              }}
            />
          </div>

          {mode === 'json' ? (
            <div className="space-y-1">
              <Label>配置 JSON</Label>
              <Textarea
                value={jsonText}
                rows={22}
                spellCheck={false}
                className="font-mono text-xs"
                onChange={(e) => {
                  setJsonText(e.target.value)
                  setSaveError('')
                }}
              />
              <div className="rounded-lg bg-muted/50 p-3 text-xs leading-relaxed text-muted-foreground">
                <p>
                  <span className="font-medium text-foreground">兼容三种格式：</span>
                  单 server 对象 / JSON 数组 / 标准{' '}
                  <code className="font-mono">mcpServers</code> 对象（Claude·trae·workbuddy）。含多 server 时保存即批量创建。
                </p>
                <pre className="mt-2 overflow-x-auto rounded bg-background/70 p-2 font-mono text-[11px]">
{`{
  "mcpServers": {
    "journal-crawler": {
      "command": "d:\\\\PyCharm\\\\projects\\\\Soup\\\\.venv\\\\Scripts\\\\python.exe",
      "args": ["d:\\\\PyCharm\\\\projects\\\\Soup\\\\mcp_server.py"],
      "cwd": "d:\\\\PyCharm\\\\projects\\\\Soup"
    }
  }
}`}
                </pre>
                <p className="mt-2">单 server 也可：{'{"name":"github","transport":"stdio","command":"npx","args":["-y","@modelcontextprotocol/server-github"],"default_permission":"L2"}'}</p>
              </div>
            </div>
          ) : (
            <div className="space-y-3">
              <div className="space-y-1">
                <Label htmlFor="mcp-name">名称</Label>
                <Input
                  id="mcp-name"
                  value={form.name ?? ''}
                  maxLength={50}
                  disabled={modal === 'edit'}
                  placeholder="如 weather（字母/数字/下划线/连字符）"
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                />
                <p className="text-xs text-muted-foreground">名称一经创建不可修改，工具名前缀将随之固定。</p>
              </div>

              {modal === 'create' ? (
                <>
                  {/* 新建仅支持远程 http 模式；本地 MCP 走"上传本地 MCP" */}
                  <div className="flex items-center gap-2 rounded-lg border border-sky-500/30 bg-sky-500/10 px-3 py-2 text-xs text-sky-700 dark:text-sky-300">
                    <Server className="size-3.5 shrink-0" />
                    <span>
                      传输方式：<span className="font-mono font-medium">http</span>（远程 Streamable HTTP）。本地 MCP 请用右上角「上传本地 MCP」。
                    </span>
                  </div>
                  <div className="space-y-1">
                    <Label htmlFor="mcp-url">端点 URL</Label>
                    <Input
                      id="mcp-url"
                      value={form.url ?? ''}
                      placeholder="如 https://mcp.example.com/mcp"
                      onChange={(e) => setForm({ ...form, url: e.target.value })}
                    />
                  </div>
                  <KvEditor
                    label="请求头 headers"
                    value={form.headers ?? {}}
                    rows={3}
                    onChange={(headers) => setForm({ ...form, headers })}
                  />
                </>
              ) : form.transport === 'stdio' ? (
                <>
                  {/* 编辑本地（上传的）MCP：代码字段只读，更新请重新上传 */}
                  <div className="flex items-start gap-1.5 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
                    <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                    <span>
                      这是通过「上传本地 MCP」创建的本地 server。更新代码请重新上传；此处仅可修改权限 / 超时 / 启用。
                    </span>
                  </div>
                  <div className="space-y-1">
                    <Label>启动命令</Label>
                    <Input value={form.command ?? ''} disabled />
                  </div>
                  <div className="space-y-1">
                    <Label>工作目录 cwd</Label>
                    <Input value={form.cwd ?? ''} disabled />
                    <p className="text-xs text-muted-foreground">相对服务器工作目录解析，保证子进程能读到上传的代码。</p>
                  </div>
                  <div className="space-y-1">
                    <Label>命令参数 args</Label>
                    <Textarea rows={3} value={(form.args ?? []).join('\n')} disabled />
                  </div>
                </>
              ) : (
                <>
                  <div className="space-y-1">
                    <Label htmlFor="mcp-url">端点 URL</Label>
                    <Input
                      id="mcp-url"
                      value={form.url ?? ''}
                      placeholder="如 https://mcp.example.com/mcp"
                      onChange={(e) => setForm({ ...form, url: e.target.value })}
                    />
                  </div>
                  <KvEditor
                    label="请求头 headers"
                    value={form.headers ?? {}}
                    rows={3}
                    onChange={(headers) => setForm({ ...form, headers })}
                  />
                </>
              )}

              <div className="space-y-1">
                <Label htmlFor="mcp-timeout">单次调用超时（秒，0 = 跟随上游）</Label>
                <Input
                  id="mcp-timeout"
                  type="number"
                  min={0}
                  value={form.timeout_seconds ?? 0}
                  onChange={(e) =>
                    setForm({ ...form, timeout_seconds: e.target.value === '' ? undefined : Number(e.target.value) })
                  }
                />
              </div>

              <div className="space-y-1">
                <Label htmlFor="mcp-permission">默认权限级别</Label>
                <select
                  id="mcp-permission"
                  value={form.default_permission || 'L2'}
                  onChange={(e) => setForm({ ...form, default_permission: e.target.value })}
                  className="h-9 w-full rounded-md border bg-background px-3 text-sm"
                >
                  {PERMISSIONS.map((p) => (
                    <option key={p.value} value={p.value}>
                      {p.label}
                    </option>
                  ))}
                </select>
              </div>

              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={form.enabled !== false}
                  onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
                  className="size-4"
                />
                启用该 server（取消勾选 = 禁用，agent 不注册其工具）
              </label>

              {/* 测试连接（保存前验证） */}
              <div className="rounded-lg border bg-muted/30 p-3">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-medium text-muted-foreground">测试连接（实际连接并发现工具）</span>
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-7 gap-1 text-xs"
                    onClick={() => void testConfigNow()}
                    disabled={testing}
                  >
                    {testing ? <Loader2 className="size-3 animate-spin" /> : <Plug className="size-3" />} 测试连接
                  </Button>
                </div>
                {testInfo && (
                  <div className="mt-2">
                    {testInfo.error ? (
                      <div className="flex items-start gap-1.5 text-xs text-destructive">
                        <AlertTriangle className="mt-0.5 size-3 shrink-0" />
                        <span className="break-all">连接失败：{testInfo.error}</span>
                      </div>
                    ) : testInfo.tools.length > 0 ? (
                      <div className="text-xs text-muted-foreground">
                        <span className="text-emerald-600 dark:text-emerald-400">连接成功</span>，发现 {testInfo.tools.length} 个工具：
                        <span className="mt-1 block break-all font-mono text-[11px]">{testInfo.tools.join('、')}</span>
                      </div>
                    ) : (
                      <div className="text-xs text-muted-foreground">连接成功，但未发现任何工具。</div>
                    )}
                  </div>
                )}
              </div>
            </div>
          )}
        </Modal>
      )}

      {/* 上传本地 MCP 代码弹窗 */}
      {modal === 'upload' && (
        <Modal
          title="上传本地 MCP（zip 代码包）"
          subtitle="把开发好的 MCP 上传到服务器本地运行，自动注册为 stdio server"
          footer={
            <>
              {saveError && <span className="mr-auto max-w-[50%] truncate text-xs text-destructive">{saveError}</span>}
              <Button variant="outline" onClick={() => setModal(null)} disabled={saving}>
                取消
              </Button>
              <Button onClick={() => void saveUpload()} disabled={saving || !uploadZip}>
                {saving ? <Loader2 className="size-4 animate-spin" /> : null} 上传并注册
              </Button>
            </>
          }
        >
          <div className="space-y-4">
            <div className="space-y-1">
              <Label>名称（可选，默认取 zip 文件名）</Label>
              <Input
                value={uploadName}
                maxLength={50}
                placeholder="如 journal-crawler（字母/数字/下划线/连字符）"
                onChange={(e) => setUploadName(e.target.value)}
              />
            </div>

            <label
              className={cn(
                'flex cursor-pointer flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed px-6 py-8 text-center transition-colors',
                uploadZip ? 'border-blue-500/40 bg-blue-500/5' : 'border-border hover:border-blue-500/40 hover:bg-accent/40',
              )}
            >
              <input
                ref={uploadFileRef}
                type="file"
                accept=".zip"
                className="hidden"
                onChange={(e) => {
                  setUploadZip(e.target.files?.[0] ?? null)
                  setSaveError('')
                }}
              />
              {uploadZip ? (
                <>
                  <Archive className="size-6 text-blue-500" />
                  <span className="text-sm font-medium">{uploadZip.name}</span>
                  <span className="text-xs text-muted-foreground">{(uploadZip.size / 1024).toFixed(1)} KB · 点击重新选择</span>
                </>
              ) : (
                <>
                  <Upload className="size-6 text-muted-foreground" />
                  <span className="text-sm font-medium">选择 MCP 代码 zip 包</span>
                  <span className="text-xs text-muted-foreground">含 main.py / server.py / index.js 等入口文件</span>
                </>
              )}
            </label>

            <div className="space-y-1">
              <Label>入口文件（可选，默认自动检测）</Label>
              <Input
                value={uploadEntry}
                placeholder="如 main.py / server.py / index.js"
                onChange={(e) => setUploadEntry(e.target.value)}
              />
            </div>

            <div className="space-y-1 rounded-lg bg-muted/50 p-3 text-xs leading-relaxed text-muted-foreground">
              <p>
                <span className="font-medium text-foreground">自动注册：</span>
                解压到服务器 <code className="font-mono">mcp-servers/&lt;名称&gt;/</code>，按入口后缀决定解释器（py→python3，js→node，sh→sh），
                注册为 stdio server（command=解释器，args=[入口]，cwd=代码目录）。zip ≤ 50MB。
              </p>
              <p>
                <span className="font-medium text-foreground">启用即验证：</span>
                上传后到列表点"测试/启用"——会真实连接并列出工具；服务器无法启动会报错（需要容器内有对应解释器与依赖）。
              </p>
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}
