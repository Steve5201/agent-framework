import { type ReactNode, useEffect, useRef } from 'react'
import { Sparkles } from 'lucide-react'
import { useChatStore } from '@/stores/chat'
import type { ChatMessage } from '@/types/api'
import MessageItem from './MessageItem'
import { isDocMarker, isImageMarker } from './docMarker'

/** 是否为文件/图片注入标记消息（可作为附件与后续用户文本同气泡渲染）。 */
function isAttachment(msg: ChatMessage): boolean {
  return msg.role === 'user' && (isDocMarker(msg.content) || isImageMarker(msg.content))
}

/** 消息列表：内容变化自动滚动到底部（新 token / 切换会话）。
 *  气泡合并（需求 3）：上传文件/图片的注入标记消息与紧随其后的用户文本消息
 *  归并为「一个用户气泡」——附件卡片在上、文本在下。纯文件（无文本）时
 *  标记消息单独渲染为附件卡片。合并以「标记消息后紧邻普通用户文本」为准，
 *  不会跨 assistant 误合并。 */
export default function MessageList() {
  const messages = useChatStore((s) => s.messages)
  const bottomRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    // jsdom 无 scrollIntoView（测试环境）：可选调用防御。
    bottomRef.current?.scrollIntoView?.({ behavior: 'auto' })
  }, [messages])

  if (messages.length === 0) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 text-center">
        <div className="flex size-14 items-center justify-center rounded-full bg-accent text-primary">
          <Sparkles className="size-6" />
        </div>
        <div className="text-2xl font-semibold">开始一段新对话</div>
        <div className="max-w-sm text-sm text-muted-foreground">
          在下方输入问题，智能体将调用工具为你解答（如计算器、回显）。
        </div>
      </div>
    )
  }

  // 渲染分组：把 [文档]/[图片] 标记消息暂存为「待合并附件」，遇到紧邻的
  // 普通用户文本时合并为一个气泡；遇到 assistant/tool 前先 flush。
  const items: ReactNode[] = []
  let pending: ChatMessage[] = []
  for (const msg of messages) {
    if (isAttachment(msg)) {
      pending.push(msg)
      continue
    }
    if (msg.role === 'user') {
      items.push(
        <MessageItem key={msg.id} message={msg} attachments={pending.length > 0 ? pending : undefined} />,
      )
      pending = []
      continue
    }
    // assistant / tool：先输出待合并的附件（纯文件场景），再渲染本条。
    for (const a of pending) items.push(<MessageItem key={a.id} message={a} />)
    pending = []
    items.push(<MessageItem key={msg.id} message={msg} />)
  }
  // 收尾：仍挂起的标记消息（历史以文件结尾）单独渲染。
  for (const a of pending) items.push(<MessageItem key={a.id} message={a} />)

  return (
    <div className="flex w-full flex-col pb-4">
      {items}
      <div ref={bottomRef} />
    </div>
  )
}
