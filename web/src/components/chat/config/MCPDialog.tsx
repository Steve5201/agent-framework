import { useEffect, useMemo, useState } from 'react'
import { Loader2, Lock, Plug, Wrench, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { SessionConfig, ToolInfo } from '@/types/api'
import { listTools } from '@/lib/api'
import { useChatStore } from '@/stores/chat'
import { useAuthStore } from '@/stores/auth'
import { isAdminRole } from '@/lib/roles'
import { mergeSessionConfig } from '@/lib/sessionConfig'
import Toggle from './Toggle'

/** MCP server 分组视图：管理端启用的 server → 其下工具列表。 */
interface McpServerGroup {
  /** server token（来自工具名 mcp_<server>_<tool> 的净化名） */
  name: string
  tools: Array<{ name: string; description: string }>
}

/** 按 mcp_<server>_<tool> 前缀解析工具名为 server 分组。 */
function groupMcpTools(tools: ToolInfo[]): McpServerGroup[] {
  const groups = new Map<string, McpServerGroup>()
  for (const t of tools) {
    const m = /^mcp_([^_]+)_/.exec(t.name)
    if (!m) continue
    const server = m[1]
    let g = groups.get(server)
    if (!g) {
      g = { name: server, tools: [] }
      groups.set(server, g)
    }
    g.tools.push({ name: t.name, description: t.description })
  }
  return [...groups.values()].sort((a, b) => a.name.localeCompare(b.name))
}

/**
 * MCPDialog 对话配置区"MCP"（P2-E 管理员会话级配置）。
 *
 * 角色分层（按用户要求）：
 *   - 管理员（super_admin / agent_admin / admin）：可在此会话级勾选启用的
 *     MCP server，改动实时保存到本会话配置（mcp_servers）；空 = 全部生效。
 *   - 普通用户：只读。使用管理员在管理端启用的全部 MCP（会话不设
 *     mcp_servers = 后端自动放行全部已启用 server 的工具），不可实时改动。
 *
 * 可用性检测：仅"管理端启用且连接成功"的 server 才会注册工具并出现在
 * 列表中（listTools 轮询刷新，弹窗打开期间管理端启停实时反映）。
 */
interface Props {
  /** 当前智能体域：切换智能体后 MCP server/工具列表跟随刷新（空 = 实例全量） */
  agentId?: string
  sessionConfig?: SessionConfig
  onClose: () => void
}

export default function MCPDialog({ agentId, sessionConfig, onClose }: Props) {
  const updateConfig = useChatStore((s) => s.updateConfig)
  const activeId = useChatStore((s) => s.activeId)
  const user = useAuthStore((s) => s.user)
  const isAdmin = isAdminRole(user?.role)

  const [servers, setServers] = useState<McpServerGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  // 首载：拉取工具列表 → 按 server 分组 → 一次性初始化勾选。
  // 管理员勾选 = 会话配置的 mcp_servers（∩ 当前可用 server）；空 = 全选。
  useEffect(() => {
    let cancelled = false
    void (async () => {
      // 异步路径内进入加载态：避免 effect 同步 setState 级联渲染
      setLoading(true)
      setError('')
      try {
        const tools = await listTools(agentId)
        if (cancelled) return
        const groups = groupMcpTools(tools)
        setServers(groups)
        const names = groups.map((g) => g.name)
        // 勾选初始化按"生效配置"展示（管理员/普通用户一致）：
        //   - mcp_servers 非空 → 白名单；
        //   - mcp_servers_set=true 且空 → 显式全不选（管理员锁定，只读展示）；
        //   - 未设置 → 全部已启用 server。
        if (sessionConfig?.mcp_servers?.length) {
          setSelected(sessionConfig.mcp_servers.filter((n) => names.includes(n)))
        } else if (sessionConfig?.mcp_servers_set === true) {
          setSelected([])
        } else {
          setSelected(names)
        }
      } catch (e) {
        if (!cancelled) setError((e as Error).message)
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isAdmin, agentId])

  // 轮询：管理端新增/停用 MCP server 实时反映（不覆盖当前勾选）
  useEffect(() => {
    let cancelled = false
    const timer = window.setInterval(() => {
      listTools(agentId)
        .then((tools) => {
          if (cancelled) return
          const groups = groupMcpTools(tools)
          const keep = new Set(groups.map((g) => g.name))
          setSelected((s) => s.filter((n) => keep.has(n)))
          setServers(groups)
          setError('')
        })
        .catch(() => {})
    }, 3000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [agentId])

  const allSelected = servers.length > 0 && servers.every((s) => selected.includes(s.name))

  const toggle = (name: string) =>
    setSelected((prev) => (prev.includes(name) ? prev.filter((n) => n !== name) : [...prev, name]))

  const handleSave = async () => {
    if (!activeId || !isAdmin) return
    setSaving(true)
    setSaveError('')
    try {
      // 全选 → mcp_servers=[] + set=false（后端语义 = 全部已启用 server 生效）。
      // 部分/全不选 → set=true 锁定选择：全不选时空数组 = 本会话不装配任何 MCP 工具。
      // 其余配置经 mergeSessionConfig 全量保留（全量替换需带回）。
      const config = mergeSessionConfig(sessionConfig, {
        mcp_servers: allSelected ? [] : selected,
        mcp_servers_set: !allSelected,
      })
      await updateConfig(activeId, config)
      onClose()
    } catch (e) {
      setSaveError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const mcpCount = useMemo(() => servers.reduce((n, g) => n + g.tools.length, 0), [servers])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="dialog" aria-modal="true">
      <div className="w-full max-w-md rounded-lg border bg-background p-5 shadow-lg">
        <div className="mb-4 flex items-center justify-between">
          <h2 className="flex items-center gap-2 text-base font-semibold">
            <Plug className="h-4 w-4" /> MCP 连接
          </h2>
          <Button type="button" variant="ghost" size="icon" onClick={onClose} aria-label="关闭">
            <X />
          </Button>
        </div>

        <p className="mb-3 flex items-start gap-1.5 text-xs text-muted-foreground">
          {isAdmin ? (
            <>
              <Wrench className="mt-0.5 h-3 w-3 shrink-0" />
              管理员可对本会话勾选启用的 MCP 连接（改动实时生效）；全选 = 使用全部已启用连接，仅勾选部分 = 本会话只使用选中的连接，全部取消 = 本会话不装配任何 MCP 工具。
            </>
          ) : (
            <>
              <Lock className="mt-0.5 h-3 w-3 shrink-0" />
              当前为只读：本会话使用管理员在管理端启用的全部 MCP 连接，不可在对话界面改动。
            </>
          )}
        </p>

        {loading ? (
          <div className="flex items-center gap-2 py-6 text-xs text-muted-foreground">
            <Loader2 className="h-3 w-3 animate-spin" /> 加载中…
          </div>
        ) : (
          <div className="mb-4">
            <div className="mb-2 flex items-center justify-between">
              <h3 className="text-sm font-medium">已启用的 MCP 连接（{servers.length}）</h3>
              {isAdmin && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-6 px-2 text-xs"
                  onClick={() => setSelected(allSelected ? [] : servers.map((s) => s.name))}
                >
                  {allSelected ? '全部取消' : '全选'}
                </Button>
              )}
            </div>
            <ul className="max-h-72 space-y-1 overflow-y-auto">
              {servers.length === 0 && (
                <li className="py-1 text-xs text-muted-foreground">当前无可用 MCP 连接</li>
              )}
              {servers.map((s) => (
                <li key={s.name} className="flex items-center justify-between rounded border px-3 py-2">
                  <div className="min-w-0">
                    <p className="text-sm font-medium">{s.name}</p>
                    <p className="truncate text-xs text-muted-foreground">
                      {s.tools.length} 个工具
                      {s.tools[0] && ` · ${s.tools[0].name}`}
                    </p>
                  </div>
                  <Toggle
                    checked={selected.includes(s.name)}
                    onChange={() => toggle(s.name)}
                    disabled={!isAdmin}
                    aria-label={`${isAdmin ? '本会话启用' : '只读查看'} MCP ${s.name}`}
                  />
                </li>
              ))}
            </ul>
            {mcpCount > 0 && <p className="mt-2 text-xs text-muted-foreground">共 {mcpCount} 个 MCP 工具</p>}
          </div>
        )}

        {error && <p className="mb-3 text-xs text-destructive">{error}</p>}
        {saveError && <p className="mb-3 text-xs text-destructive">{saveError}</p>}

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose} disabled={saving}>
            取消
          </Button>
          {isAdmin && (
            <Button type="button" onClick={handleSave} disabled={saving || loading || !!error}>
              {saving ? '保存中…' : '保存'}
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}
