// chat.createSession.test.ts —— 建会话的智能体域处理单测。
//
// 背景：超管全门户聊天域（agentId='*'）只是"跨域展示/切换"的聊天域，
// 不是可归属的会话域——后端 validateAgentID 白名单拒绝 '*'，曾导致
// 超管在 '/agent/*' 点"新建会话"无反应。store 层必须回退默认域。
import { describe, it, expect, vi, beforeEach } from 'vitest'

const apiMocks = vi.hoisted(() => ({
  createSession: vi.fn(async () => ({
    id: 's1',
    user_id: '1',
    title: '新对话',
    created_at: '',
    updated_at: '',
    config: {},
  })),
  listSessions: vi.fn(async () => ({ sessions: [], total: 0 })),
}))
vi.mock('@/lib/api', () => ({
  createSession: apiMocks.createSession,
  listSessions: apiMocks.listSessions,
}))
vi.mock('@/lib/sse', () => ({ streamChat: vi.fn(async () => {}) }))
vi.mock('@/lib/localTools', () => ({
  LOCAL_TOOL_NAMES: new Set(),
  isTauri: vi.fn(() => false),
  runLocalShell: vi.fn(),
}))

import { useChatStore } from '@/stores/chat'
import { ALL_AGENT_ID, DEFAULT_AGENT_ID } from '@/lib/roles'

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

describe('createSession · 智能体域处理', () => {
  it(`agentId='*'（超管全门户域）时回退默认域 ${DEFAULT_AGENT_ID} 创建`, async () => {
    useChatStore.setState({ agentId: ALL_AGENT_ID })
    await useChatStore.getState().createSession()
    expect(apiMocks.createSession).toHaveBeenCalledWith(undefined, DEFAULT_AGENT_ID)
    expect(useChatStore.getState().activeId).toBe('s1')
    expect(useChatStore.getState().sessions).toHaveLength(1)
  })

  it('具体智能体域原样透传', async () => {
    useChatStore.setState({ agentId: 'math' })
    await useChatStore.getState().createSession()
    expect(apiMocks.createSession).toHaveBeenCalledWith(undefined, 'math')
  })

  it('管理端域（空串）原样透传', async () => {
    useChatStore.setState({ agentId: '' })
    await useChatStore.getState().createSession()
    expect(apiMocks.createSession).toHaveBeenCalledWith(undefined, '')
  })
})
