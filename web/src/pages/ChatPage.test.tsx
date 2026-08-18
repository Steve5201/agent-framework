// ChatPage.test.tsx —— 超管专属门户游客拦截行为单测。
//
// 覆盖：'*' 域未登录（游客）禁止对话，仅渲染登录提示页；普通智能体域游客
// 正常对话；已登录超管访问 '*' 域不受拦截。子组件打桩，只验证页面级分支。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import ChatPage from './ChatPage'
import { useAuthStore } from '@/stores/auth'
import { useChatStore } from '@/stores/chat'

// 隔离网络层：ChatPage 挂载即经 initAgent → listSessions 拉会话列表
const apiMocks = vi.hoisted(() => ({ listSessions: vi.fn(), checkAgentDomain: vi.fn() }))
vi.mock('@/lib/api', () => ({
  listSessions: apiMocks.listSessions,
  checkAgentDomain: apiMocks.checkAgentDomain,
}))
vi.mock('@/lib/sse', () => ({ streamChat: vi.fn(async () => {}) }))
vi.mock('@/lib/localTools', () => ({
  LOCAL_TOOL_NAMES: new Set(),
  isTauri: vi.fn(() => false),
  runLocalShell: vi.fn(),
}))

// 子组件打桩：本测试只关心页面级分支（禁止页 vs 正常对话布局）
vi.mock('@/components/chat/SessionSidebar', () => ({
  default: () => <aside data-testid="session-sidebar" />,
}))
vi.mock('@/components/chat/MessageList', () => ({
  default: () => <div data-testid="message-list" />,
}))
vi.mock('@/components/chat/ChatInput', () => ({
  default: ({ canConfigure }: { canConfigure: boolean }) => (
    <div data-testid="chat-input" data-configure={String(canConfigure)} />
  ),
}))
vi.mock('@/components/chat/LocalToolModal', () => ({ default: () => null }))

const renderAt = (path: string) =>
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/agent/:agentId" element={<ChatPage mode="agent" />} />
        <Route path="/admin/chat" element={<ChatPage mode="admin" />} />
        <Route path="*" element={<div />} />
      </Routes>
    </MemoryRouter>,
  )

describe('ChatPage · 超管专属门户游客拦截', () => {
  beforeEach(() => {
    localStorage.clear()
    apiMocks.listSessions.mockReset()
    apiMocks.listSessions.mockResolvedValue({ sessions: [], total: 0 })
    apiMocks.checkAgentDomain.mockReset()
    apiMocks.checkAgentDomain.mockResolvedValue({ exists: true, id: 'tutor', name: 'tutor', status: 1 })
    useAuthStore.setState({ user: null, status: 'guest' })
  })

  it('游客访问 /agent/* 禁止对话：仅展示登录提示页，不渲染对话界面', () => {
    renderAt('/agent/*')
    expect(screen.getByText('超管专属门户')).toBeInTheDocument()
    expect(screen.getByText(/该门户仅供最高超管登录使用/)).toBeInTheDocument()
    // 对话界面（输入框 / 消息列表 / 会话侧栏）均不渲染
    expect(screen.queryByTestId('chat-input')).not.toBeInTheDocument()
    expect(screen.queryByTestId('message-list')).not.toBeInTheDocument()
    expect(screen.queryByTestId('session-sidebar')).not.toBeInTheDocument()
    // 登录入口指向本域登录页 /login/*
    expect(screen.getByRole('link', { name: /登录/ })).toHaveAttribute('href', '/login/*')
  })

  it('游客访问普通智能体域正常对话（输入框渲染且为游客态）', async () => {
    renderAt('/agent/tutor')
    const input = await screen.findByTestId('chat-input')
    expect(input).toBeInTheDocument()
    // 游客：canConfigure=false（配置按钮区隐藏）
    expect(input.getAttribute('data-configure')).toBe('false')
    // 游客守卫：合法域发起校验但不踢回（listSessions 仍为 tutor 域）
    expect(apiMocks.checkAgentDomain).toHaveBeenCalledWith('tutor')
  })

  it('游客访问孤儿域（不存在）踢回默认门户', async () => {
    apiMocks.checkAgentDomain.mockResolvedValue({
      exists: false,
      id: 'mi',
      name: '',
      status: 0,
    })
    renderAt('/agent/mi')
    // 踢回 /agent/tutor：会话列表以默认门户域重新拉取
    await waitFor(() => expect(apiMocks.listSessions).toHaveBeenCalledWith(1, 50, 'tutor'))
  })

  it('游客访问停用域（status=0）踢回默认门户', async () => {
    apiMocks.checkAgentDomain.mockResolvedValue({
      exists: true,
      id: 'tutor',
      name: 'tutor',
      status: 0,
    })
    renderAt('/agent/tutor')
    await waitFor(() => expect(apiMocks.listSessions).toHaveBeenCalledWith(1, 50, 'tutor'))
  })

  it('游客访问合法非默认域（/agent/math）不被踢回', async () => {
    apiMocks.checkAgentDomain.mockResolvedValue({
      exists: true,
      id: 'math',
      name: 'math',
      status: 1,
    })
    renderAt('/agent/math')
    const input = await screen.findByTestId('chat-input')
    expect(input).toBeInTheDocument()
    expect(apiMocks.checkAgentDomain).toHaveBeenCalledWith('math')
    // 未踢回：会话列表仍保持 math 域
    expect(apiMocks.listSessions).toHaveBeenCalledWith(1, 50, 'math')
  })

  it('已登录超管访问 /agent/* 不受拦截，正常对话', () => {
    useAuthStore.setState({
      user: {
        id: '1',
        username: 'root',
        role: 'super_admin',
        tags: [{ key: 'agent', value: '*' }],
      } as never,
      status: 'authed',
    })
    renderAt('/agent/*')
    expect(screen.queryByText(/该门户仅供最高超管登录使用/)).not.toBeInTheDocument()
    expect(screen.getByTestId('chat-input')).toBeInTheDocument()
  })
})

describe('ChatPage · 管理端对话会话域回退', () => {
  beforeEach(() => {
    localStorage.clear()
    apiMocks.listSessions.mockReset()
    apiMocks.listSessions.mockResolvedValue({ sessions: [], total: 0 })
    useAuthStore.setState({ user: null, status: 'guest' })
    useChatStore.setState({ agentId: '', sessions: [], sessionsTotal: 0, activeId: null, messages: [] })
  })

  it('管理端对话无记住的智能体：游客/普通用户会话域回退管理端域（空串）', async () => {
    renderAt('/admin/chat')
    await screen.findByTestId('chat-input')
    expect(useChatStore.getState().agentId).toBe('')
    expect(apiMocks.listSessions).toHaveBeenCalledWith(1, 50, '')
  })

  it('管理端对话无记住的智能体：已登录超管回退全门户域（*）', async () => {
    useAuthStore.setState({
      user: {
        id: '1',
        username: 'root',
        role: 'super_admin',
        tags: [{ key: 'agent', value: '*' }],
      } as never,
      status: 'authed',
    })
    renderAt('/admin/chat')
    await screen.findByTestId('chat-input')
    expect(useChatStore.getState().agentId).toBe('*')
    expect(apiMocks.listSessions).toHaveBeenCalledWith(1, 50, '*')
  })

  it('管理端对话无记住的智能体：绑定域管理员回退其归属域', async () => {
    useAuthStore.setState({
      user: {
        id: '2',
        username: 'am',
        role: 'agent_admin',
        tags: [{ key: 'agent', value: 'math' }],
      } as never,
      status: 'authed',
    })
    renderAt('/admin/chat')
    await screen.findByTestId('chat-input')
    expect(useChatStore.getState().agentId).toBe('math')
    expect(apiMocks.listSessions).toHaveBeenCalledWith(1, 50, 'math')
  })

  it('管理端对话记住选中智能体：会话域回退到该智能体', async () => {
    localStorage.setItem('agent.last_agent', 'math')
    renderAt('/admin/chat')
    await screen.findByTestId('chat-input')
    expect(useChatStore.getState().agentId).toBe('math')
    expect(apiMocks.listSessions).toHaveBeenCalledWith(1, 50, 'math')
    // 顶栏标题展示当前智能体域（桌面头部栏 + 移动端顶栏），便于确认归属
    expect(screen.getAllByText('管理端对话 · math').length).toBeGreaterThan(0)
  })
})
