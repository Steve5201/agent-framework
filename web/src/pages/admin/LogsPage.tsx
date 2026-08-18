import { useCallback, useEffect, useState } from 'react'
import { adminListAgents, adminListLogs } from '@/lib/api'
import type { Agent, AuditLogEntry } from '@/types/api'
import { isSuperAdmin, ROLE_LABELS } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Loader2, RefreshCw, ScrollText, Search } from 'lucide-react'
import { cn } from '@/lib/utils'

const PAGE_SIZE = 50

/** 状态码 → 徽标（2xx 成功 / 4xx 参数与权限 / 5xx 服务端 / 其它） */
function statusStyle(status: number) {
  if (status >= 200 && status < 300) return 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-300'
  if (status >= 500) return 'bg-rose-500/10 text-rose-600 dark:text-rose-300'
  if (status >= 400) return 'bg-amber-500/10 text-amber-600 dark:text-amber-300'
  return 'bg-muted text-muted-foreground'
}

/** 角色徽标配色（与 UsersPage 一致） */
const ROLE_STYLE: Record<string, string> = {
  super_admin: 'bg-amber-500/10 text-amber-600 dark:text-amber-300',
  agent_admin: 'bg-blue-500/10 text-blue-600 dark:text-blue-300',
  admin: 'bg-sky-500/10 text-sky-600 dark:text-sky-300',
}

/** 动作 → 中文描述（未匹配时原样展示，保证信息不丢） */
function formatAction(action: string): string {
  const [module, verb, ...rest] = action.split('.')
  const verbLabels: Record<string, string> = {
    create: '创建',
    update: '更新',
    delete: '删除',
    upload: '上传',
    restore: '回滚',
    enabled: '启用/禁用',
    test: '测试',
  }
  const moduleLabels: Record<string, string> = {
    skills: '技能',
    mcp: 'MCP',
    kb: '知识库',
    users: '用户',
    agents: '智能体',
    logs: '日志',
  }
  const v = verbLabels[verb ?? ''] ?? verb
  const m = moduleLabels[module ?? ''] ?? module
  const sub = rest.length > 0 ? `·${rest.join('.')}` : ''
  return `${m} ${v}${sub}`.trim()
}

/** 操作审计日志页：查询各智能体域的管理端写操作（阶段4·日志管理）。 */
export default function LogsPage() {
  const me = useAuthStore((s) => s.user)
  const superAdmin = isSuperAdmin(me?.role)

  const [logs, setLogs] = useState<AuditLogEntry[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  // 过滤条件（仅在点击"查询"时生效）
  const [agentFilter, setAgentFilter] = useState('') // 空 = 全部域（仅超管有意义）
  const [actionFilter, setActionFilter] = useState('')
  const [userFilter, setUserFilter] = useState('')
  const [queryAgent, setQueryAgent] = useState('')
  const [queryAction, setQueryAction] = useState('')
  const [queryUser, setQueryUser] = useState('')

  // 超管可选域列表
  const [agents, setAgents] = useState<Agent[]>([])

  // 超管拉取智能体候选（仅超管需要下拉；失败不影响查询）。
  useEffect(() => {
    if (!superAdmin) return
    let cancelled = false
    adminListAgents()
      .then((list) => {
        if (!cancelled) setAgents(list)
      })
      .catch(() => {
        /* 下拉留空，仍可用默认查询 */
      })
    return () => {
      cancelled = true
    }
  }, [superAdmin])

  const load = useCallback(() => {
    adminListLogs({
      agent_id: queryAgent || undefined,
      action: queryAction.trim() || undefined,
      user_id: queryUser.trim() || undefined,
      page,
      page_size: PAGE_SIZE,
    })
      .then((resp) => {
        setLogs(resp.logs)
        setTotal(resp.total)
        setError('')
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false))
  }, [queryAgent, queryAction, queryUser, page])

  useEffect(() => {
    void load()
  }, [load])

  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  /** 点击查询：条件生效并回到第一页。 */
  function applyFilter() {
    setLoading(true)
    setQueryAgent(agentFilter)
    setQueryAction(actionFilter)
    setQueryUser(userFilter)
    setPage(1)
  }

  return (
    <div className="mx-auto max-w-6xl p-6">
      {/* 页头 */}
      <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <div className="flex size-8 items-center justify-center rounded-lg bg-blue-500/15 text-blue-600 dark:text-blue-300">
              <ScrollText className="size-4.5" />
            </div>
            <h1 className="text-lg font-semibold tracking-tight">操作日志</h1>
          </div>
          <p className="mt-1.5 max-w-xl text-xs leading-relaxed text-muted-foreground">
            审计管理端写操作（技能 / MCP / 知识库 / 用户 / 智能体）：谁在何时对哪个智能体做了什么。
            日志按智能体域隔离——{superAdmin ? '最高超管可查看全部域' : '本账号仅能查看所属智能体组'}。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={() => { setLoading(true); void load() }} disabled={loading}>
            <RefreshCw className={cn('size-4', loading && 'animate-spin')} /> 刷新
          </Button>
        </div>
      </div>

      {error && (
        <div className="mb-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div>
      )}

      {/* 过滤栏 */}
      <div className="mb-3 flex flex-wrap items-end gap-3 rounded-lg border bg-card p-3">
        {superAdmin ? (
          <div className="space-y-1">
            <Label className="text-xs text-muted-foreground">智能体域</Label>
            <select
              value={agentFilter}
              onChange={(e) => setAgentFilter(e.target.value)}
              aria-label="智能体域"
              className="h-8 rounded-md border bg-background px-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
            >
              <option value="">全部域</option>
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}（{a.id}）
                </option>
              ))}
            </select>
          </div>
        ) : (
          <div className="flex h-8 items-center rounded-md border border-muted bg-muted/30 px-3 text-xs text-muted-foreground">
            智能体域：{me?.tags?.find((t) => t.key === 'agent')?.value ?? 'tutor'}（账号归属固定）
          </div>
        )}
        <div className="space-y-1">
          <Label className="text-xs text-muted-foreground">动作</Label>
          <Input
            className="h-8 w-44"
            placeholder="如 skills / mcp.update"
            value={actionFilter}
            onChange={(e) => setActionFilter(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') applyFilter()
            }}
          />
        </div>
        <div className="space-y-1">
          <Label className="text-xs text-muted-foreground">操作者 ID</Label>
          <Input
            className="h-8 w-36"
            placeholder="如 3"
            value={userFilter}
            onChange={(e) => setUserFilter(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') applyFilter()
            }}
          />
        </div>
        <Button size="sm" onClick={applyFilter}>
          <Search className="size-3.5" /> 查询
        </Button>
      </div>

      {/* 列表 */}
      {loading ? (
        <div className="flex justify-center py-16">
          <Loader2 className="size-5 animate-spin text-muted-foreground" />
        </div>
      ) : logs.length === 0 ? (
        <div className="rounded-xl border border-dashed bg-card/50 py-16 text-center">
          <p className="text-sm text-muted-foreground">
            暂无日志{queryAgent ? `（域「${queryAgent}」）` : ''}
            {queryAction ? `（动作「${queryAction}」）` : ''}。管理端产生写操作后即可在此看到审计记录。
          </p>
        </div>
      ) : (
        <div className="overflow-hidden rounded-xl border bg-card">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/50 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 font-medium">时间</th>
                <th className="px-3 py-2.5 font-medium">操作者</th>
                <th className="px-3 py-2.5 font-medium">角色</th>
                <th className="px-3 py-2.5 font-medium">动作</th>
                <th className="px-3 py-2.5 font-medium">目标域</th>
                <th className="px-3 py-2.5 font-medium">请求</th>
                <th className="px-3 py-2.5 font-medium">状态</th>
                <th className="px-4 py-2.5 font-medium">耗时</th>
              </tr>
            </thead>
            <tbody>
              {logs.map((e) => (
                <tr key={`${e.request_id ?? ''}-${e.ts}`} className="border-b transition-colors last:border-0 hover:bg-accent/40">
                  <td className="whitespace-nowrap px-4 py-2 font-mono text-xs">
                    {new Date(e.ts).toLocaleString('zh-CN', { hour12: false })}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs">#{e.user_id}</td>
                  <td className="px-3 py-2">
                    <Badge
                      variant="outline"
                      className={cn('text-[10px]', ROLE_STYLE[e.role] ?? 'bg-muted text-muted-foreground')}
                    >
                      {ROLE_LABELS[e.role] ?? e.role}
                    </Badge>
                  </td>
                  <td className="px-3 py-2">
                    <div className="text-xs font-medium">{formatAction(e.action)}</div>
                    <div className="font-mono text-[10px] text-muted-foreground">{e.action}</div>
                  </td>
                  <td className="px-3 py-2 font-mono text-xs">{e.target_agent}</td>
                  <td className="max-w-[220px] truncate px-3 py-2 font-mono text-[10px] text-muted-foreground" title={`${e.method} ${e.path}`}>
                    {e.method} {e.path}
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant="outline" className={cn('text-[10px]', statusStyle(e.status))}>
                      {e.status}
                    </Badge>
                  </td>
                  <td className="px-4 py-2 font-mono text-xs text-muted-foreground">{e.latency_ms ?? '-'}ms</td>
                </tr>
              ))}
            </tbody>
          </table>
          {/* 分页 */}
          <div className="flex items-center justify-between border-t px-4 py-2.5 text-xs text-muted-foreground">
            <span>
              共 {total} 条 · 第 {page}/{totalPages} 页
            </span>
            <div className="flex items-center gap-1">
              <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => { setLoading(true); setPage((p) => p - 1) }}>
                上一页
              </Button>
              <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={() => { setLoading(true); setPage((p) => p + 1) }}>
                下一页
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
