// MessageList.test.tsx —— 气泡合并逻辑单测（需求 3：文件/图片与用户文本同气泡）。
//
// MessageItem 打桩：只透出 message.id 与 attachments（合并传入的附件 id 列表），
// 便于断言「哪条消息把哪些注入标记消息合并进同一气泡」。
import { describe, expect, it, beforeEach, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import MessageList from './MessageList'
import { useChatStore } from '@/stores/chat'
import type { ChatMessage } from '@/types/api'

vi.mock('./MessageItem', () => ({
  default: ({ message, attachments }: { message: ChatMessage; attachments?: ChatMessage[] }) => (
    <div
      data-testid="msg-item"
      data-id={message.id}
      data-merge={(attachments ?? []).map((a) => a.id).join(',')}
    >
      {message.content}
    </div>
  ),
}))

const docMsg = (id: string): ChatMessage => ({
  id,
  role: 'user',
  content:
    '[文档] 简介.md（解析 1 段，全文已保存至工作区 users/9/chat-files/3/简介.md，读全文用 file_ops read 该相对路径）\n\n正文',
  status: 'done',
})
const imgMsg = (id: string): ChatMessage => ({
  id,
  role: 'user',
  content: '[图片] a.png（已保存至工作区 users/9/chat-files/3/a.png）',
  status: 'done',
})
const textMsg = (id: string, content = '看一下'): ChatMessage => ({
  id,
  role: 'user',
  content,
  status: 'done',
})
const asstMsg = (id: string): ChatMessage => ({
  id,
  role: 'assistant',
  content: '已回复',
  status: 'done',
})

const renderWith = (messages: ChatMessage[]) => {
  useChatStore.setState({ messages })
  render(<MessageList />)
}

describe('MessageList · 气泡合并（需求 3）', () => {
  beforeEach(() => {
    useChatStore.setState({ messages: [] })
  })

  it('上传文件注入消息 + 紧随其后的用户文本 → 合并为一个气泡', () => {
    renderWith([docMsg('m1'), textMsg('m2'), asstMsg('m3')])
    const items = screen.getAllByTestId('msg-item')
    // 合并后：1 个合并气泡（文本 m2 携带附件 m1）+ 1 条助手消息
    expect(items).toHaveLength(2)
    // 文本气泡携带附件 m1
    expect(items[0].dataset.id).toBe('m2')
    expect(items[0].dataset.merge).toBe('m1')
    // 助手消息正常渲染
    expect(items[1].dataset.id).toBe('m3')
  })

  it('多文件 + 文本 → 全部附件合并进同一气泡', () => {
    renderWith([docMsg('d1'), imgMsg('i1'), textMsg('t1'), asstMsg('a1')])
    const items = screen.getAllByTestId('msg-item')
    expect(items[0].dataset.id).toBe('t1')
    expect(items[0].dataset.merge).toBe('d1,i1')
  })

  it('纯文件（无文本）→ 附件单独渲染，不吞并助手消息', () => {
    renderWith([docMsg('d1'), asstMsg('a1')])
    const items = screen.getAllByTestId('msg-item')
    // 两个独立条目：附件消息本身 + 助手消息
    expect(items).toHaveLength(2)
    expect(items[0].dataset.id).toBe('d1')
    expect(items[0].dataset.merge).toBe('')
    expect(items[1].dataset.id).toBe('a1')
  })

  it('先文本后附件（跨轮独立上传）→ 不误合并', () => {
    renderWith([textMsg('t1'), asstMsg('a1'), docMsg('d1'), asstMsg('a2')])
    const items = screen.getAllByTestId('msg-item')
    expect(items).toHaveLength(4)
    expect(items[0].dataset.merge).toBe('')
    expect(items[2].dataset.merge).toBe('')
  })

  it('历史以文件结尾（无后续文本）→ 附件单独渲染', () => {
    renderWith([asstMsg('a1'), docMsg('d1')])
    const items = screen.getAllByTestId('msg-item')
    expect(items).toHaveLength(2)
    expect(items[1].dataset.id).toBe('d1')
    expect(items[1].dataset.merge).toBe('')
  })
})
