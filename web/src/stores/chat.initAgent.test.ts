// chat.initAgent.test.ts —— initAgent 挂载/重挂载会话列表拉取行为单测。
//
// 背景：桌面端重开（webview 重新加载）后 chat store 是全新内存态，agentId
// 回到空串。旧逻辑 `if (get().agentId === agentId) return` 只在"作用域变化"
// 时拉取列表，若组件重挂载时 agentId 恰好已相同（或开发期 HMR 场景）会短路
// 留下空列表，导致"重开应用后对话列表不自动刷新"。修复后同域重挂载必拉取。
import { describe, it, expect, vi, beforeEach } from 'vitest'

const apiMocks = vi.hoisted(() => ({
  listSessions: vi.fn(async () => ({ sessions: [], total: 0 })),
}))
vi.mock('@/lib/api', () => ({
  listSessions: apiMocks.listSessions,
}))
vi.mock('@/lib/sse', () => ({ streamChat: vi.fn(async () => {}) }))
vi.mock('@/lib/localTools', () => ({
  LOCAL_TOOL_NAMES: new Set(),
  isTauri: vi.fn(() => false),
  runLocalShell: vi.fn(),
}))

import { useChatStore } from '@/stores/chat'

beforeEach(() => {
  vi.clearAllMocks()
  useChatStore.setState({
    agentId: '',
    sessions: [],
    sessionsTotal: 0,
    activeId: null,
    messages: [],
  })
})

describe('initAgent · 挂载/重挂载', () => {
  it('首次挂载：设置作用域并拉取该域会话列表', async () => {
    useChatStore.getState().initAgent('tutor')
    expect(useChatStore.getState().agentId).toBe('tutor')
    expect(apiMocks.listSessions).toHaveBeenCalledTimes(1)
    expect(apiMocks.listSessions).toHaveBeenCalledWith(1, 50, 'tutor')
  })

  it('同域重挂载（桌面端重开/路由重进）：不再短路，仍拉取列表', async () => {
    useChatStore.setState({ agentId: 'tutor' }) // 模拟重挂载时 agentId 已相同
    useChatStore.getState().initAgent('tutor')
    expect(apiMocks.listSessions).toHaveBeenCalledTimes(1)
    expect(useChatStore.getState().agentId).toBe('tutor')
  })

  it('切换作用域：清空当前会话后拉取新域列表', async () => {
    useChatStore.getState().initAgent('tutor')
    apiMocks.listSessions.mockClear()
    useChatStore.getState().initAgent('math')
    expect(useChatStore.getState().agentId).toBe('math')
    expect(apiMocks.listSessions).toHaveBeenCalledWith(1, 50, 'math')
  })
})
