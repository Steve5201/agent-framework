// ChatPage.test.tsx —— 超管专属门户游客拦截行为单测。
//
// 覆盖：'*' 域未登录（游客）禁止对话，仅渲染登录提示页；普通智能体域游客
// 正常对话；已登录超管访问 '*' 域不受拦截。子组件打桩，只验证页面级分支。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import ChatPage from './ChatPage'
import { useAuthStore } from '@/stores/auth'
import { useChatStore } from '@/stores/chat'

// 隔离网络层：ChatPage 挂载即经 initAgent → listSessions 拉会话列表
const apiMocks = vi.hoisted(() => ({ listSessions: vi.fn() }))
vi.mock('@/lib/api', () => ({ listSessions: apiMocks.listSessions }))
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

  it('管理端对话无记住的智能体：会话域回退管理端域（空串）', async () => {
    renderAt('/admin/chat')
    await screen.findByTestId('chat-input')
    expect(useChatStore.getState().agentId).toBe('')
    expect(apiMocks.listSessions).toHaveBeenCalledWith(1, 50, '')
  })

  it('管理端对话记住选中智能体：会话域回退到该智能体', async () => {
    localStorage.setItem('agent.last_agent', 'math')
    renderAt('/admin/chat')
    await screen.findByTestId('chat-input')
    expect(useChatStore.getState().agentId).toBe('math')
    expect(apiMocks.listSessions).toHaveBeenCalledWith(1, 50, 'math')
    // 移动端顶栏标题展示当前智能体域，便于确认归属
    expect(screen.getByText('管理端对话 · math')).toBeInTheDocument()
  })
})
