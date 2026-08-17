import type { CondensedInfo } from '@/stores/chat'

/** 上下文压缩提示条（历史回看）：该轮发生过上下文压缩，收纳了早期消息。
 *  视觉参考 Trae「上下文已压缩」提示：弱化、可一眼识别、不打断正文阅读。
 *  仅历史回放（fromHistory 解析 __condense_v1__ system 记录）时出现。 */
export default function CondenseNotice({ info }: { info: CondensedInfo }) {
  return (
    <div
      data-testid="condense-notice"
      role="note"
      title={`上下文压缩记录：收纳 ${info.dropped} 条早期消息，会话累计第 ${info.count} 次压缩`}
      className="mb-2 inline-flex max-w-full items-center gap-1.5 rounded-md border border-dashed border-amber-500/50 bg-amber-500/5 px-2.5 py-1 text-xs text-amber-700"
    >
      {/* 压缩示意图标（两条折线收拢） */}
      <svg
        aria-hidden
        viewBox="0 0 16 16"
        className="h-3.5 w-3.5 shrink-0"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
      >
        <path d="M2 5.5h12" />
        <path d="M4 8.5h8" />
        <path d="M6 11.5h4" />
      </svg>
      <span>
        上下文已压缩：收纳 {info.dropped} 条早期消息
        {info.count > 0 && <span className="text-amber-500/80">（累计第 {info.count} 次）</span>}
      </span>
    </div>
  )
}
