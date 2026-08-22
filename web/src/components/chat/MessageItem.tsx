import type { ChatMessage } from '@/types/api'
import { Bot, User } from 'lucide-react'
import MessageActions from './MessageActions'
import ThinkingBlock from './ThinkingBlock'
import OrchestrationBlock from './OrchestrationBlock'
import CondenseNotice from './CondenseNotice'
import RichContent from './RichContent'
import ChatDocCard from './ChatDocCard'
import ImageMessageCard from './ImageMessageCard'
import { isDocMarker, isImageMarker } from './docMarker'

/** AI 头像：蓝色渐变方卡 + Bot 图标（参照 ui_chat.html agent-avatar）。 */
function AiAvatar() {
  return (
    <div
      className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-gradient-to-br from-blue-500 to-blue-600 text-white shadow-sm"
      aria-hidden
    >
      <Bot className="size-4.5" />
    </div>
  )
}

/** 用户头像：灰色方卡 + 用户图标。 */
function UserAvatar() {
  return (
    <div
      className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground"
      aria-hidden
    >
      <User className="size-4.5" />
    </div>
  )
}

/** 单条消息渲染（用户气泡 / 助手文本+工具过程 / 工具结果）。
 *  操作区（复制/删除本轮/重新生成/分支/版本切换）位于气泡下方，用户与助手共用。
 *  助手文本走 RichContent（Markdown 富文本 + 图表/SVG/媒体协议渲染，需求 9）。
 *  [文档] / [图片] 注入消息（模块二·上传文件）渲染为引用卡片。
 *  attachments：与文本同一气泡合并渲染的文件/图片标记消息（需求 3·气泡合并），
 *  由 MessageList 把「上传文件注入消息 + 紧随其后的用户文本」归并后传入。 */
export default function MessageItem({
  message,
  attachments = [],
}: {
  message: ChatMessage
  attachments?: ChatMessage[]
}) {
  if (message.role === 'tool') {
    return (
      <div className="msg-in px-4 py-1.5 sm:px-12">
        <div className="w-full max-w-[85%] sm:max-w-[65%]">
          <div className="flex items-start gap-2 rounded-lg bg-muted/60 px-3 py-2 text-xs text-muted-foreground">
            <span className="shrink-0 font-medium">工具结果</span>
            <code className="min-w-0 flex-1 break-all">{message.content}</code>
          </div>
        </div>
      </div>
    )
  }

  if (message.role === 'user') {
    const isDoc = isDocMarker(message.content)
    const isImage = isImageMarker(message.content)
    const hasText = message.content.trim().length > 0
    return (
      <div className="msg-in flex justify-end gap-3 px-4 py-2 sm:px-12">
        <div className="flex max-w-[90%] flex-col items-end gap-1.5 sm:max-w-[65%]">
          {/* 合并气泡的附件部分：上传文件/图片注入消息渲染为卡片（需求 3） */}
          {attachments.map((a) =>
            isDocMarker(a.content) ? (
              <ChatDocCard key={a.id} content={a.content} />
            ) : (
              <ImageMessageCard key={a.id} content={a.content} />
            ),
          )}
          {isDoc ? (
            <ChatDocCard content={message.content} />
          ) : isImage ? (
            <ImageMessageCard content={message.content} />
          ) : hasText ? (
            <div className="rounded-2xl rounded-tr-md bg-primary px-4 py-2.5 text-[15px] leading-relaxed text-primary-foreground shadow-sm whitespace-pre-wrap break-words md:text-sm md:leading-normal">
              {message.content}
            </div>
          ) : null}
          <MessageActions message={message} align="right" />
        </div>
        <UserAvatar />
      </div>
    )
  }

  // assistant
  return (
    <div className="msg-in flex justify-start gap-3 px-4 py-2 sm:px-12">
      <AiAvatar />
      <div className="flex max-w-[90%] min-w-0 flex-col sm:max-w-[65%]">
        {/* 思考过程折叠块：思考文本 + 工具调用/返回可视化（需求 9）。
         *  工具调没调、返回什么，由真实执行事件渲染，一眼可辨，杜绝幻觉。
         *  编排模式（tasks 非空）改渲染子任务进度轨迹，与思考过程互斥。 */}
        {message.tasks ? (
          <OrchestrationBlock tasks={message.tasks} streaming={message.status === 'streaming'} />
        ) : (
          <ThinkingBlock segments={message.thinking ?? []} streaming={message.status === 'streaming'} />
        )}
        {/* 上下文压缩提示条：历史回看该轮压缩过的节点（__condense_v1__ 记录） */}
        {message.condensed && <CondenseNotice info={message.condensed} />}
        {/* AI 回复白色气泡卡（参照 ui_chat.html message-bubble.ai：白底+细边框+左上小圆角尾巴） */}
        <div className="rounded-2xl rounded-tl-md border border-border bg-card px-4 py-3 shadow-sm">
          <div className="text-[15px] leading-relaxed break-words md:text-sm md:leading-normal" aria-live="polite">
            {message.content ? (
              <RichContent content={message.content} streaming={message.status === 'streaming'} />
            ) : message.thinking && message.thinking.length > 0 ? null : (
              <span className="text-muted-foreground">
                {message.status === 'streaming' ? '正在思考…' : '(空)'}
              </span>
            )}
            {message.status === 'streaming' && (
              <span
                className="ml-0.5 inline-block h-3.5 w-1.5 animate-pulse rounded-sm bg-primary align-middle"
                aria-hidden
              />
            )}
          </div>
        </div>
        <MessageActions message={message} align="left" />
      </div>
    </div>
  )
}
