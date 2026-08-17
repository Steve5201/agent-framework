// OrchestrationBlock —— 助手气泡上方的"多智能体编排进度"折叠块（P4-G）。
//
// 渲染编排模式（mode=orchestrate）下各子任务的实时进度轨迹：
//   - research（研究资料）→ outline（设计大纲）→ content（撰写正文）→ review（审核校稿）
//   - 节点状态：running（进行中）/ completed（完成）/ failed（失败）/ skipped（跳过）
//
// P4-N：子任务"思考中 / 工具调用中"状态实时透传，按 TaskID 累积到对应节点：
//   - reasoning → 节点下思考区（灰色斜体小字，"思考中…"打字机）
//   - tool_start/tool_end → 工具履历徽标（当前工具 + 已完成工具），与正文分开渲染
// 思考/工具与正文同节点不同区块，互不混排；其它节点不受影响。
//
// 行为与 ThinkingBlock 一致（P4-K）：流式中保持展开展示实时进度，
// 编排完成后自动收起为"过程气泡"（用户可点击展开查看节点轨迹与失败原因）。
import { useEffect, useRef, useState } from 'react'
import { Braces, CheckCircle2, ChevronDown, ChevronRight, CircleDashed, Loader2, MinusCircle, Network, Sparkles, Wrench, XCircle } from 'lucide-react'
import type { OrchestrationTask } from '@/types/api'

/** 子任务 ID → 中文展示名（与后端内置教研角色池对应）。 */
const ROLE_LABELS: Record<string, string> = {
  research: '研究资料',
  outline: '设计大纲',
  content: '撰写正文',
  review: '审核校稿',
  worker: '执行任务',
}

function taskLabel(id: string): string {
  return ROLE_LABELS[id] ?? id
}

const STATUS_TEXT: Record<OrchestrationTask['status'], string> = {
  running: '进行中',
  completed: '完成',
  failed: '失败',
  skipped: '跳过',
}

function StatusIcon({ status }: { status: OrchestrationTask['status'] }) {
  switch (status) {
    case 'running':
      return <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-blue-500" />
    case 'completed':
      return <CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-emerald-500" />
    case 'failed':
      return <XCircle className="h-3.5 w-3.5 shrink-0 text-red-500" />
    case 'skipped':
      return <MinusCircle className="h-3.5 w-3.5 shrink-0 text-slate-400" />
    default:
      return <CircleDashed className="h-3.5 w-3.5 shrink-0 text-slate-300" />
  }
}

interface OrchestrationBlockProps {
  /** 子任务进度（按到达顺序；同一任务后到事件覆盖前一次状态） */
  tasks: OrchestrationTask[]
  /** 编排是否进行中（流式）：进行中显示整体 loading 与"正在分解任务"空态 */
  streaming?: boolean
}

export default function OrchestrationBlock({ tasks, streaming = false }: OrchestrationBlockProps) {
  const [collapsed, setCollapsed] = useState(!streaming)
  const prevStreaming = useRef(streaming)

  // 编排完成 + 对话完成 → 自动收起（用户仍可点击展开查看节点轨迹/失败原因）
  useEffect(() => {
    if (prevStreaming.current && !streaming) setCollapsed(true)
    prevStreaming.current = streaming
  }, [streaming])

  if (tasks.length === 0 && !streaming) return null

  const doneCount = tasks.filter((t) => t.status === 'completed').length
  const failedCount = tasks.filter((t) => t.status === 'failed').length
  // 标题随进度变化：流式中显示"进行中"；结束后显示完成计数，失败时附失败数
  // （失败原因展开后可看，避免错过关键告警）。
  let title = '多智能体编排'
  if (streaming) {
    title += ' · 进行中'
  } else if (tasks.length > 0) {
    title += ` · 完成 ${doneCount}/${tasks.length}`
    if (failedCount > 0) title += `（失败 ${failedCount}）`
  }

  return (
    <div className="mb-2 overflow-hidden rounded-md border border-border bg-muted/40">
      <button
        type="button"
        onClick={() => setCollapsed((c) => !c)}
        className="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-xs text-muted-foreground hover:bg-muted/60"
      >
        {collapsed ? <ChevronRight className="h-3.5 w-3.5 shrink-0" /> : <ChevronDown className="h-3.5 w-3.5 shrink-0" />}
        <Network className="h-3.5 w-3.5 shrink-0" />
        <span className="font-medium">{title}</span>
        {streaming && <Loader2 className="ml-auto h-3.5 w-3.5 shrink-0 animate-spin" />}
      </button>
      {!collapsed && (
        <div className="max-h-80 space-y-1 overflow-y-auto px-2.5 pb-2.5 pt-1">
          {tasks.map((t) => (
            <div key={t.id}>
              <div className="flex items-center gap-2 text-xs">
                <StatusIcon status={t.status} />
                <span className="text-foreground">{taskLabel(t.id)}</span>
                <span className="text-muted-foreground">{STATUS_TEXT[t.status]}</span>
                {/* P4-N 工具调用状态：当前工具 + 已完成工具履历 */}
                {(t.activeTool || (t.toolHistory && t.toolHistory.length > 0)) && (
                  <span className="flex shrink-0 items-center gap-1 text-[10px] text-amber-600">
                    {t.toolHistory?.map((name) => (
                      <span key={name} className="inline-flex items-center gap-0.5 rounded-sm bg-amber-500/10 px-1 py-0.5">
                        <Braces className="h-2.5 w-2.5" />
                        {name}
                      </span>
                    ))}
                    {t.activeTool && (
                      <span className="inline-flex items-center gap-0.5 rounded-sm bg-amber-500/20 px-1 py-0.5">
                        <Wrench className="h-2.5 w-2.5 animate-pulse" />
                        {t.activeTool}
                      </span>
                    )}
                  </span>
                )}
                {t.totalTokens ? (
                  <span className="ml-auto text-[10px] text-muted-foreground">{t.totalTokens} tok</span>
                ) : null}
              </div>
              {/* P4-N 思考区：子任务"思考中"打字机（灰色斜体，独立于正文区块） */}
              {t.reasoning ? (
                <div className="mt-0.5 flex max-h-20 items-start gap-1 overflow-y-auto pl-[22px] text-[11px] italic leading-relaxed text-slate-400">
                  <Sparkles className="mt-0.5 h-2.5 w-2.5 shrink-0" />
                  <span className="whitespace-pre-wrap">{t.reasoning}</span>
                </div>
              ) : null}
              {/* 子任务输出（P4-M）：task_content 增量实时累积，节点下小面板
                   打字机渲染（running 时滚动跟随，完成/失败后保留可回看）。 */}
              {t.content ? (
                <div className="mt-0.5 max-h-24 overflow-y-auto whitespace-pre-wrap pl-[22px] text-[11px] leading-relaxed text-muted-foreground">
                  {t.content}
                </div>
              ) : null}
              {/* 失败详情（P4-I）：任务失败时展示具体报错，便于用户判断根因
                   （超时/限流 429/上游 400/上下文超限等），hover 查看完整信息。 */}
              {t.status === 'failed' && t.error ? (
                <div
                  className="mt-0.5 pl-[22px] text-[11px] leading-relaxed text-red-600 line-clamp-3"
                  title={t.error}
                >
                  {t.error}
                </div>
              ) : null}
            </div>
          ))}
          {tasks.length === 0 && streaming && (
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <span className="inline-block h-3 w-1 animate-pulse rounded-sm bg-blue-400 align-middle" aria-hidden />
              正在分解任务…
            </div>
          )}
        </div>
      )}
    </div>
  )
}
