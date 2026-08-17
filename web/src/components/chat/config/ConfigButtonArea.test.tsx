// ConfigButtonArea.test.tsx —— 配置按钮区单测。
//
// 覆盖：无活动会话时配置按钮仍可见（用户"直接输入开新聊"也能配置对话选项）；
// 点击会话依赖型配置项自动新建会话后弹窗；已有会话时不重复建会话。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import ConfigButtonArea from './ConfigButtonArea'
import { useChatStore } from '@/stores/chat'
import { useAuthStore } from '@/stores/auth'

const apiMocks = vi.hoisted(() => ({
  createSession: vi.fn(),
  listResources: vi.fn(),
}))
vi.mock('@/lib/api', () => ({
  createSession: apiMocks.createSession,
  listResources: apiMocks.listResources,
}))
vi.mock('@/lib/sse', () => ({ streamChat: vi.fn(async () => {}) }))
vi.mock('@/lib/localTools', () => ({
  LOCAL_TOOL_NAMES: new Set(),
  isTauri: vi.fn(() => false),
  runLocalShell: vi.fn(),
}))

function setUser(role: string, agentTag?: string) {
  useAuthStore.setState({
    user: {
      id: '1',
      username: 'u',
      role,
      tags: agentTag ? [{ key: 'agent', value: agentTag }] : [],
    } as never,
    status: 'authed',
  })
}

function makeSession(id: string) {
  return {
    id,
    user_id: '1',
    title: '新对话',
    created_at: '',
    updated_at: '',
    config: {},
  }
}

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  useChatStore.setState({
    agentId: '',
    sessions: [],
    sessionsTotal: 0,
    activeId: null,
    messages: [],
    sending: false,
  })
  setUser('user', 'tutor')
  apiMocks.createSession.mockResolvedValue(makeSession('s-new'))
  apiMocks.listResources.mockResolvedValue([])
})

describe('ConfigButtonArea', () => {
  it('无活动会话时仍渲染能力/思考/技能配置按钮（可直接输入开新聊）', () => {
    render(<ConfigButtonArea />)
    expect(screen.getByRole('button', { name: '能力开关' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '思考模式' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '技能' })).toBeInTheDocument()
    // 非超管不渲染切换智能体按钮
    expect(screen.queryByRole('button', { name: '切换智能体' })).not.toBeInTheDocument()
  })

  it('无活动会话点击会话依赖项：先自动新建会话，再打开配置弹窗', async () => {
    render(<ConfigButtonArea />)
    fireEvent.click(screen.getByRole('button', { name: '能力开关' }))
    await screen.findByRole('dialog')
    expect(apiMocks.createSession).toHaveBeenCalledTimes(1)
    expect(useChatStore.getState().activeId).toBe('s-new')
    // 弹窗内容正常渲染（资源加载完成后显示空列表文案）
    expect(await screen.findByText('当前无可用能力')).toBeInTheDocument()
  })

  it('已有活动会话时不重复建会话，直接打开弹窗', async () => {
    useChatStore.setState({ sessions: [makeSession('s0')], activeId: 's0' })
    render(<ConfigButtonArea />)
    fireEvent.click(screen.getByRole('button', { name: '思考模式' }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    expect(apiMocks.createSession).not.toHaveBeenCalled()
  })

  it('建会话失败时给出错误提示且不打开弹窗', async () => {
    const alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {})
    apiMocks.createSession.mockRejectedValue(new Error('network down'))
    render(<ConfigButtonArea />)
    fireEvent.click(screen.getByRole('button', { name: '技能' }))
    await vi.waitFor(() => expect(alertSpy).toHaveBeenCalledWith(expect.stringContaining('network down')))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    alertSpy.mockRestore()
  })
})
