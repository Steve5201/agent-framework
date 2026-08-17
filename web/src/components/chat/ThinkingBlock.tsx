// ThinkingBlock —— 助手气泡上方的"思考过程"折叠块（需求 9）。
//
// 渲染模型"想→做→想"循环的完整过程：
//   - 思考文本（灰色，与回答正文明显区分）
//   - 工具调用（琥珀色块：工具名 + 参数）——真实发起调用才渲染
//   - 工具返回（绿色块：结果内容；失败为红色）——真实返回结果才渲染
//
// 行为：
//   - 流式中（streaming=true）保持展开，展示实时思考与工具过程；
//   - 思考完成 + 对话完成（streaming 变 false）自动收起，用户可点击展开。
import { useEffect, useRef, useState } from 'react'
import { Brain, ChevronDown, ChevronRight, Loader2 } from 'lucide-react'
import type { ThinkingSegment } from '@/types/api'

/** 参数 JSON 美化展示（解析失败则原样返回）。 */
function formatArgs(args: string): string {
  if (!args) return ''
  try {
    return JSON.stringify(JSON.parse(args), null, 2)
  } catch {
    return args
  }
}

/** 单个过程分段。 */
function Segment({ seg, active }: { seg: ThinkingSegment; active: boolean }) {
  if (seg.kind === 'tool-call') {
    return (
      <div className="rounded border border-amber-200 bg-amber-50 px-2.5 py-1.5">
        <div className="flex items-center gap-1 text-xs font-medium text-amber-800">
          <span className="rounded bg-amber-200/70 px-1.5 py-0.5 text-[10px] leading-4">工具调用</span>
          {seg.name}
        </div>
        {seg.arguments && (
          <pre className="msg-code-inline mt-1 overflow-x-auto text-[11px] text-amber-900">
            {formatArgs(seg.arguments)}
          </pre>
        )}
      </div>
    )
  }
  if (seg.kind === 'tool-result') {
    return (
      <div className="rounded border border-emerald-200 bg-emerald-50 px-2.5 py-1.5">
        <div className="flex items-center gap-1 text-xs font-medium text-emerald-800">
          <span className="rounded bg-emerald-200/70 px-1.5 py-0.5 text-[10px] leading-4">工具返回</span>
          {seg.name}
          {seg.error && <span className="text-red-600">（失败）</span>}
        </div>
        <pre
          className={`msg-code-inline mt-1 max-h-40 overflow-y-auto text-[11px] ${
            seg.error ? 'text-red-700' : 'text-emerald-900'
          }`}
        >
          {seg.content || '(空)'}
        </pre>
      </div>
    )
  }
  // 思考文本
  return (
    <div className="flex items-start gap-1 text-xs leading-relaxed text-muted-foreground">
      <span className="whitespace-pre-wrap break-words">{seg.content}</span>
      {active && <span className="inline-block h-3 w-1 animate-pulse rounded-sm bg-primary align-middle" aria-hidden />}
    </div>
  )
}

interface ThinkingBlockProps {
  /** 思考过程分段（按到达顺序：思考文本/工具调用/工具返回交错） */
  segments: ThinkingSegment[]
  /** 思考是否进行中（流式）：进行中保持展开，结束后自动收起 */
  streaming?: boolean
}

export default function ThinkingBlock({ segments, streaming = false }: ThinkingBlockProps) {
  const [collapsed, setCollapsed] = useState(!streaming)
  const prevStreaming = useRef(streaming)

  // 思考完成 + 对话完成 → 自动收起（用户仍可点击展开）
  useEffect(() => {
    if (prevStreaming.current && !streaming) setCollapsed(true)
    prevStreaming.current = streaming
  }, [streaming])

  const toolCount = segments.filter((s) => s.kind === 'tool-call').length
  const thinkingCount = segments.filter((s) => s.kind === 'text').length
  // 折叠块名称随内容动态变化：有思考文本 → "思考过程"；关闭思考但发生了
  // 工具调用 → "工具调用"（不再误导为思考）；都没有 → "过程"。
  const title =
    thinkingCount > 0
      ? toolCount > 0
        ? `思考过程 · ${toolCount} 次工具调用`
        : '思考过程'
      : toolCount > 0
        ? `工具调用 · ${toolCount} 次`
        : '过程'
  // 统一渲染：所有助手消息都带"过程"折叠块，保证逻辑一致——
  // 有思考则展示内容，无思考则展示空态占位（而非整块消失造成界面不一致）。
  // 流式中即使暂无分段也展示（提示正在思考）。

  return (
    <div className="mb-2 overflow-hidden rounded-md border border-border bg-muted/40">
      <button
        type="button"
        onClick={() => setCollapsed((c) => !c)}
        className="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-xs text-muted-foreground hover:bg-muted/60"
      >
        {collapsed ? <ChevronRight className="h-3.5 w-3.5 shrink-0" /> : <ChevronDown className="h-3.5 w-3.5 shrink-0" />}
        <Brain className="h-3.5 w-3.5 shrink-0" />
        <span className="font-medium">{title}</span>
        {streaming && <Loader2 className="ml-auto h-3.5 w-3.5 shrink-0 animate-spin" />}
      </button>
      {!collapsed && (
        <div className="max-h-80 space-y-1.5 overflow-y-auto px-2.5 pb-2.5 pt-1">
          {segments.map((seg, i) => (
            <Segment key={i} seg={seg} active={streaming && i === segments.length - 1} />
          ))}
          {segments.length === 0 && streaming && (
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <span className="inline-block h-3 w-1 animate-pulse rounded-sm bg-primary align-middle" aria-hidden />
              正在思考…
            </div>
          )}
          {segments.length === 0 && !streaming && (
            <div className="text-xs text-muted-foreground/70">本次未产生思考过程</div>
          )}
        </div>
      )}
    </div>
  )
}
