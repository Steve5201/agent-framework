import { useCallback, useEffect, useMemo, useState } from 'react'
import { adminDataOverview } from '@/lib/api'
import type { DataOverview, SessionAgentStat, UsageGroup } from '@/types/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import AdminChart from '@/components/charts/AdminChart'
import type { EChartsCoreOption } from 'echarts/core'
import {
  Activity,
  AlertTriangle,
  Bot,
  Coins,
  DollarSign,
  Flame,
  LayoutDashboard,
  Loader2,
  MessageSquare,
  RefreshCw,
  TrendingUp,
  Users,
} from 'lucide-react'
import { cn } from '@/lib/utils'

// ---------------------------------------------------------------------------
// 格式化工具
// ---------------------------------------------------------------------------

/** 千分位分隔 */
function formatNum(n: number): string {
  return n.toLocaleString('zh-CN')
}

/** 紧凑数值（图表轴与 Token 量级展示）：1.2k / 3.4M */
function formatCompact(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`
  return String(n)
}

/** 成本展示（后端字段为美元） */
function formatCost(n: number): string {
  return `$${n.toFixed(2)}`
}

/** YYYY-MM-DD → MM-DD（图表 x 轴精简） */
function shortDate(date: string): string {
  return date.length >= 10 ? date.slice(5) : date
}

/** 智能体域展示名：空 = 管理端域 */
function agentLabel(id: string): string {
  return id === '' ? '管理端域' : id
}

// ---------------------------------------------------------------------------
// 小组件
// ---------------------------------------------------------------------------

/** 汇总统计卡 */
function StatCard({
  label,
  value,
  hint,
  icon,
}: {
  label: string
  value: string
  hint?: string
  icon?: React.ReactNode
}) {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="flex items-center justify-between gap-2">
          <span className="truncate text-xs text-muted-foreground">{label}</span>
          {icon}
        </div>
        <div className="mt-1.5 truncate text-xl font-semibold tabular-nums">{value}</div>
        {hint && <div className="mt-0.5 truncate text-[11px] text-muted-foreground">{hint}</div>}
      </CardContent>
    </Card>
  )
}

/** 图表卡片（数据为空时渲染占位，避免空白坐标轴） */
function ChartCard({
  title,
  desc,
  empty,
  children,
}: {
  title: string
  desc?: string
  empty: boolean
  children: React.ReactNode
}) {
  return (
    <Card>
      <CardHeader className="p-4 pb-1">
        <CardTitle className="text-sm">{title}</CardTitle>
        {desc && <CardDescription className="text-[11px]">{desc}</CardDescription>}
      </CardHeader>
      <CardContent className="p-4 pt-1">
        {empty ? (
          <div className="flex h-[260px] items-center justify-center text-sm text-muted-foreground">
            该窗口内暂无数据
          </div>
        ) : (
          children
        )}
      </CardContent>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 页面
// ---------------------------------------------------------------------------

const DAY_OPTIONS = [7, 30, 90]

const TABS = [
  { key: 'overview', label: '运营总览', icon: LayoutDashboard },
  { key: 'agents', label: '智能体分析', icon: Bot },
  { key: 'cost', label: '成本速览', icon: Coins },
] as const

type TabKey = (typeof TABS)[number]['key']

/** 智能体合并排行行：会话统计（agent-service）+ 用量聚合（llm-gateway）按域合并 */
interface AgentRow {
  agent_id: string
  sessions: number
  calls: number
  total_tokens: number
  cost_usd: number
}

/**
 * 数据管理（运营分析台，super_admin 专属只读模块）：
 * 三 Tab——运营总览（会话/活跃/成本汇总 + 趋势）、智能体分析（域分布与排行）、
 * 成本速览（成本曲线与模型占比）。数据源 /v1/admin/data/overview 聚合
 * agent-service + llm-gateway + auth-service 三端只读数据，无任何写操作。
 */
export default function DataPage() {
  const [days, setDays] = useState(30)
  const [tab, setTab] = useState<TabKey>('overview')
  const [data, setData] = useState<DataOverview | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [sortKey, setSortKey] = useState<'sessions' | 'calls' | 'cost'>('sessions')

  const load = useCallback(() => {
    adminDataOverview(days)
      .then((d) => {
        setData(d)
        setError('')
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false))
  }, [days])

  // 初次加载：loading 初始为 true，请求完成后回落
  useEffect(() => {
    void load()
  }, [load])

  /** 刷新（事件回调内进入加载态，spinner + disabled 生效） */
  function refresh() {
    setLoading(true)
    void load()
  }

  // ---- 派生态（useMemo 稳定引用，避免图表 useMemo 依赖每次 render 变化）----
  const daySessions = useMemo(() => data?.sessions.days ?? [], [data])
  const windowSessions = daySessions.reduce((a, d) => a + d.sessions, 0)
  const todaySessions = daySessions.length > 0 ? daySessions[daySessions.length - 1].sessions : 0
  const summary = data?.usage.summary
  const daily = useMemo(() => data?.usage.daily ?? [], [data])
  const byModel = data?.usage.by_model ?? []
  const topUsers = (data?.usage.by_user ?? []).slice(0, 10)
  const userNames = data?.user_names ?? {}

  const successRate = summary && summary.calls > 0 ? ((summary.success / summary.calls) * 100).toFixed(1) : '-'
  const failRate = summary && summary.calls > 0 ? ((summary.failed / summary.calls) * 100).toFixed(1) : '-'

  /** Tab2 合并排行（会话统计 ∪ 用量聚合），按当前排序列降序 */
  const agentRows = useMemo<AgentRow[]>(() => {
    const map = new Map<string, AgentRow>()
    for (const a of data?.sessions.agents ?? []) {
      map.set(a.agent_id, { agent_id: a.agent_id, sessions: a.sessions, calls: 0, total_tokens: 0, cost_usd: 0 })
    }
    for (const g of data?.usage.by_agent ?? []) {
      const row = map.get(g.key) ?? { agent_id: g.key, sessions: 0, calls: 0, total_tokens: 0, cost_usd: 0 }
      row.calls += g.calls
      row.total_tokens += g.total_tokens
      row.cost_usd += g.cost_usd
      map.set(g.key, row)
    }
    return [...map.values()].sort((a, b) => {
      const av = sortKey === 'cost' ? a.cost_usd : a[sortKey]
      const bv = sortKey === 'cost' ? b.cost_usd : b[sortKey]
      return bv - av
    })
  }, [data, sortKey])

  // ---- 图表 option（数据直连构造）----
  const sessionOption = useMemo<EChartsCoreOption>(
    () => ({
      tooltip: { trigger: 'axis' },
      grid: { left: 8, right: 8, top: 24, bottom: 4, containLabel: true },
      xAxis: { type: 'category', boundaryGap: false, data: daySessions.map((d) => shortDate(d.date)) },
      yAxis: { type: 'value', minInterval: 1 },
      series: [
        {
          name: '新建会话',
          type: 'line',
          smooth: true,
          symbol: 'none',
          lineStyle: { width: 2 },
          areaStyle: { opacity: 0.12 },
          data: daySessions.map((d) => d.sessions),
        },
      ],
    }),
    [daySessions],
  )

  const dauOption = useMemo<EChartsCoreOption>(
    () => ({
      tooltip: { trigger: 'axis' },
      grid: { left: 8, right: 8, top: 24, bottom: 4, containLabel: true },
      xAxis: { type: 'category', boundaryGap: false, data: daily.map((d) => shortDate(d.date)) },
      yAxis: { type: 'value', minInterval: 1 },
      series: [
        {
          name: '活跃用户',
          type: 'line',
          smooth: true,
          symbol: 'none',
          lineStyle: { width: 2, color: '#f59e0b' },
          areaStyle: { opacity: 0.12 },
          data: daily.map((d) => d.dau),
        },
      ],
    }),
    [daily],
  )

  const agentPieOption = useMemo<EChartsCoreOption>(() => {
    const agents: SessionAgentStat[] = data?.sessions.agents ?? []
    return {
      tooltip: { trigger: 'item' },
      legend: { bottom: 0, type: 'scroll' },
      series: [
        {
          name: '会话分布',
          type: 'pie',
          radius: ['42%', '68%'],
          center: ['50%', '44%'],
          data: agents.map((a) => ({ name: agentLabel(a.agent_id), value: a.sessions })),
        },
      ],
    }
  }, [data])

  const costOption = useMemo<EChartsCoreOption>(
    () => ({
      tooltip: { trigger: 'axis', valueFormatter: (v: unknown) => formatCost(Number(v)) },
      grid: { left: 8, right: 8, top: 24, bottom: 4, containLabel: true },
      xAxis: { type: 'category', boundaryGap: false, data: daily.map((d) => shortDate(d.date)) },
      yAxis: { type: 'value' },
      series: [
        {
          name: '成本',
          type: 'line',
          smooth: true,
          symbol: 'none',
          lineStyle: { width: 2 },
          areaStyle: { opacity: 0.12 },
          data: daily.map((d) => d.cost_usd),
        },
      ],
    }),
    [daily],
  )

  const modelPieOption = useMemo<EChartsCoreOption>(() => {
    const groups: UsageGroup[] = data?.usage.by_model ?? []
    return {
      tooltip: { trigger: 'item' },
      legend: { bottom: 0, type: 'scroll' },
      series: [
        {
          name: '成本占比',
          type: 'pie',
          radius: ['42%', '68%'],
          center: ['50%', '44%'],
          data: groups.map((g) => ({ name: g.key || '未归类', value: g.cost_usd })),
        },
      ],
    }
  }, [data])

  const sortLabels: Record<'sessions' | 'calls' | 'cost', string> = {
    sessions: '会话数',
    calls: '调用数',
    cost: '成本',
  }
  const sortKeys = Object.keys(sortLabels) as ('sessions' | 'calls' | 'cost')[]

  return (
    <div className="mx-auto max-w-6xl p-6">
      {/* 页头 */}
      <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <div className="flex size-8 items-center justify-center rounded-lg bg-indigo-500/15 text-indigo-600 dark:text-indigo-300">
              <TrendingUp className="size-4.5" />
            </div>
            <h1 className="text-lg font-semibold tracking-tight">数据管理</h1>
          </div>
          <p className="mt-1.5 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            平台运营分析台（只读）：会话活跃度、智能体域反馈与用量成本速览。
            数据来自 agent-service / llm-gateway / auth-service 三端聚合，仅最高超管可见。
          </p>
        </div>
        <div className="flex items-center gap-2">
          {/* 窗口切换 */}
          <div className="flex items-center overflow-hidden rounded-lg border bg-card">
            {DAY_OPTIONS.map((d) => (
              <button
                key={d}
                type="button"
                onClick={() => setDays(d)}
                className={cn(
                  'h-8 px-3 text-xs transition-colors',
                  days === d ? 'bg-primary text-primary-foreground' : 'hover:bg-accent',
                )}
              >
                {d}天
              </button>
            ))}
          </div>
          <Button variant="outline" onClick={refresh} disabled={loading}>
            <RefreshCw className={cn('size-4', loading && 'animate-spin')} /> 刷新
          </Button>
        </div>
      </div>

      {error && (
        <div className="mb-3 flex items-center gap-1.5 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          <AlertTriangle className="size-4 shrink-0" /> <span>{error}</span>
        </div>
      )}

      {/* Tab 切换 */}
      <div className="mb-4 flex items-center gap-1 rounded-lg border bg-card p-1">
        {TABS.map((t) => {
          const Icon = t.icon
          return (
            <button
              key={t.key}
              type="button"
              onClick={() => setTab(t.key)}
              className={cn(
                'flex h-8 flex-1 items-center justify-center gap-1.5 rounded-md text-sm transition-colors',
                tab === t.key ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-accent',
              )}
            >
              <Icon className="size-4" /> {t.label}
            </button>
          )
        })}
      </div>

      {loading ? (
        <div className="flex justify-center py-16">
          <Loader2 className="size-5 animate-spin text-muted-foreground" />
        </div>
      ) : !data ? (
        <div className="rounded-xl border border-dashed bg-card/50 py-16 text-center">
          <p className="text-sm text-muted-foreground">暂无数据，请稍后重试。</p>
        </div>
      ) : (
        <>
          {/* ============ Tab1 运营总览 ============ */}
          {tab === 'overview' && (
            <div className="space-y-4">
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
                <StatCard
                  label="今日新建会话"
                  value={formatNum(todaySessions)}
                  hint={daySessions.length > 0 ? shortDate(daySessions[daySessions.length - 1].date) : '-'}
                  icon={<MessageSquare className="size-4 text-muted-foreground" />}
                />
                <StatCard
                  label="窗口内会话"
                  value={formatNum(windowSessions)}
                  hint={`${days} 天合计`}
                  icon={<Activity className="size-4 text-muted-foreground" />}
                />
                <StatCard
                  label="活跃用户"
                  value={formatNum(summary?.dau ?? 0)}
                  hint="窗口内去重"
                  icon={<Flame className="size-4 text-muted-foreground" />}
                />
                <StatCard
                  label="总调用"
                  value={formatNum(summary?.calls ?? 0)}
                  hint={`成功率 ${successRate}%`}
                  icon={<Users className="size-4 text-muted-foreground" />}
                />
                <StatCard
                  label="失败调用"
                  value={formatNum(summary?.failed ?? 0)}
                  hint={`占 ${failRate}%`}
                  icon={<AlertTriangle className="size-4 text-muted-foreground" />}
                />
                <StatCard
                  label="累计成本"
                  value={formatCost(summary?.cost_usd ?? 0)}
                  hint={`${formatCompact(summary?.total_tokens ?? 0)} tokens`}
                  icon={<DollarSign className="size-4 text-muted-foreground" />}
                />
              </div>

              <div className="grid gap-4 xl:grid-cols-2">
                <ChartCard title="每日新建会话" desc="sessions.created_at 按天分组（status=1 有效会话）" empty={daySessions.length === 0}>
                  <AdminChart option={sessionOption} height={260} />
                </ChartCard>
                <ChartCard title="每日活跃用户（DAU）" desc="当日成功调用去重 user_id" empty={daily.length === 0}>
                  <AdminChart option={dauOption} height={260} />
                </ChartCard>
              </div>

              <Card>
                <CardHeader className="p-4 pb-1">
                  <CardTitle className="text-sm">活跃用户 Top 10</CardTitle>
                  <CardDescription className="text-[11px]">
                    按调用量降序（用户名经 auth-service 回填；回填失败时展示 user_id）
                  </CardDescription>
                </CardHeader>
                <CardContent className="p-4 pt-1">
                  {topUsers.length === 0 ? (
                    <div className="py-10 text-center text-sm text-muted-foreground">该窗口内暂无调用</div>
                  ) : (
                    <div className="overflow-hidden rounded-lg border">
                      <table className="w-full text-sm">
                        <thead className="border-b bg-muted/50 text-left text-xs text-muted-foreground">
                          <tr>
                            <th className="px-3 py-2 font-medium">#</th>
                            <th className="px-3 py-2 font-medium">用户</th>
                            <th className="px-3 py-2 text-right font-medium">调用</th>
                            <th className="px-3 py-2 text-right font-medium">Token</th>
                            <th className="px-3 py-2 text-right font-medium">成本</th>
                          </tr>
                        </thead>
                        <tbody>
                          {topUsers.map((u, i) => {
                            const name = userNames[String(u.user_id)]
                            return (
                              <tr
                                key={u.user_id}
                                className="border-b transition-colors last:border-0 hover:bg-accent/40"
                              >
                                <td className="px-3 py-2 text-xs text-muted-foreground">{i + 1}</td>
                                <td className="px-3 py-2">
                                  <span className="font-medium">{name ?? `#${u.user_id}`}</span>
                                  {name && <span className="ml-1.5 font-mono text-[10px] text-muted-foreground">#{u.user_id}</span>}
                                </td>
                                <td className="px-3 py-2 text-right font-mono text-xs tabular-nums">
                                  {formatNum(u.calls)}
                                </td>
                                <td className="px-3 py-2 text-right font-mono text-xs tabular-nums text-muted-foreground">
                                  {formatCompact(u.total_tokens)}
                                </td>
                                <td className="px-3 py-2 text-right font-mono text-xs tabular-nums text-muted-foreground">
                                  {formatCost(u.cost_usd)}
                                </td>
                              </tr>
                            )
                          })}
                        </tbody>
                      </table>
                    </div>
                  )}
                </CardContent>
              </Card>
            </div>
          )}

          {/* ============ Tab2 智能体分析 ============ */}
          {tab === 'agents' && (
            <div className="space-y-4">
              <div className="grid gap-4 xl:grid-cols-2">
                <ChartCard title="会话按智能体域分布" desc="status=1 有效会话；'' = 管理端域" empty={(data.sessions.agents ?? []).length === 0}>
                  <AdminChart option={agentPieOption} height={260} />
                </ChartCard>
                <Card>
                  <CardHeader className="p-4 pb-1">
                    <CardTitle className="text-sm">智能体排行</CardTitle>
                    <CardDescription className="text-[11px]">
                      会话统计（agent-service）∪ 用量聚合（llm-gateway）按域合并，点击列头切换排序
                    </CardDescription>
                  </CardHeader>
                  <CardContent className="p-4 pt-1">
                    {agentRows.length === 0 ? (
                      <div className="flex h-[260px] items-center justify-center text-sm text-muted-foreground">
                        该窗口内暂无数据
                      </div>
                    ) : (
                      <div className="overflow-hidden rounded-lg border">
                        <table className="w-full text-sm">
                          <thead className="border-b bg-muted/50 text-left text-xs text-muted-foreground">
                            <tr>
                              <th className="px-3 py-2 font-medium">智能体域</th>
                              {sortKeys.map((k) => (
                                <th key={k} className="px-3 py-2 text-right font-medium">
                                  <button
                                    type="button"
                                    onClick={() => setSortKey(k)}
                                    className={cn(
                                      'inline-flex items-center gap-1 transition-colors hover:text-foreground',
                                      sortKey === k && 'text-foreground',
                                    )}
                                  >
                                    {sortLabels[k]}
                                    {sortKey === k && <span className="text-[9px]">▼</span>}
                                  </button>
                                </th>
                              ))}
                            </tr>
                          </thead>
                          <tbody>
                            {agentRows.map((r) => (
                              <tr key={r.agent_id} className="border-b transition-colors last:border-0 hover:bg-accent/40">
                                <td className="px-3 py-2">
                                  <Badge variant="outline" className="font-mono text-[10px]">
                                    {agentLabel(r.agent_id)}
                                  </Badge>
                                </td>
                                <td className="px-3 py-2 text-right font-mono text-xs tabular-nums">
                                  {formatNum(r.sessions)}
                                </td>
                                <td className="px-3 py-2 text-right font-mono text-xs tabular-nums">
                                  {formatNum(r.calls)}
                                </td>
                                <td className="px-3 py-2 text-right font-mono text-xs tabular-nums text-muted-foreground">
                                  {formatCost(r.cost_usd)}
                                </td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    )}
                  </CardContent>
                </Card>
              </div>
            </div>
          )}

          {/* ============ Tab3 成本速览 ============ */}
          {tab === 'cost' && (
            <div className="space-y-4">
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                <StatCard
                  label="累计成本"
                  value={formatCost(summary?.cost_usd ?? 0)}
                  hint={`${days} 天合计`}
                  icon={<DollarSign className="size-4 text-muted-foreground" />}
                />
                <StatCard
                  label="总 Token"
                  value={formatCompact(summary?.total_tokens ?? 0)}
                  hint="prompt + completion"
                  icon={<Activity className="size-4 text-muted-foreground" />}
                />
                <StatCard
                  label="总调用"
                  value={formatNum(summary?.calls ?? 0)}
                  hint={`成功 ${formatNum(summary?.success ?? 0)}`}
                  icon={<Users className="size-4 text-muted-foreground" />}
                />
                <StatCard
                  label="失败调用"
                  value={formatNum(summary?.failed ?? 0)}
                  hint={`占 ${failRate}%`}
                  icon={<AlertTriangle className="size-4 text-muted-foreground" />}
                />
              </div>

              <div className="grid gap-4 xl:grid-cols-2">
                <ChartCard title="每日成本" desc="usage_logs 按天累计成本" empty={daily.length === 0}>
                  <AdminChart option={costOption} height={260} />
                </ChartCard>
                <ChartCard title="成本按模型占比" desc="成本维度（非调用数），未归类 = 无模型记录" empty={byModel.length === 0}>
                  <AdminChart option={modelPieOption} height={260} />
                </ChartCard>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  )
}
