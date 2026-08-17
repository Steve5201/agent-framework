// MCPDialog.test.tsx —— MCP 连接弹窗单测。
//
// 覆盖：从 listTools 筛出 mcp_ 工具并按 server 分组展示、无 MCP 工具空态、
// 加载失败提示；角色分层——普通用户只读（开关禁用，不渲染保存按钮）、
// 管理员可会话级勾选并保存 mcp_servers（全选 = 空数组）。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import MCPDialog from './MCPDialog'
import { useAuthStore } from '@/stores/auth'
import { useChatStore } from '@/stores/chat'

const apiMocks = vi.hoisted(() => ({
  listTools: vi.fn(),
  updateSessionConfig: vi.fn(),
}))
vi.mock('@/lib/api', () => ({
  listTools: apiMocks.listTools,
  updateSessionConfig: apiMocks.updateSessionConfig,
}))
vi.mock('@/lib/sse', () => ({ streamChat: vi.fn(async () => {}) }))
vi.mock('@/lib/localTools', () => ({
  LOCAL_TOOL_NAMES: new Set(),
  isTauri: vi.fn(() => false),
  runLocalShell: vi.fn(),
}))

const tools = [
  { name: 'mcp_weather_now', description: '查询天气' },
  { name: 'mcp_weather_forecast', description: '查询预报' },
  { name: 'calculator', description: '计算器' }, // 非 MCP 工具应被过滤
  { name: 'mcp_git_status', description: '' },
]

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

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  useAuthStore.setState({ user: null, status: 'guest' })
  useChatStore.setState({ activeId: 's1' })
  apiMocks.listTools.mockResolvedValue(tools)
  apiMocks.updateSessionConfig.mockResolvedValue({ id: 's1', config: {} })
})

describe('MCPDialog', () => {
  it('筛出 mcp_ 工具并按 server 分组展示', async () => {
    render(<MCPDialog onClose={() => {}} />)
    expect(await screen.findByText('weather')).toBeInTheDocument()
    expect(screen.getByText('git')).toBeInTheDocument()
    expect(screen.getByText(/2 个工具/)).toBeInTheDocument()
    expect(screen.queryByText(/计算器/)).not.toBeInTheDocument()
    expect(screen.getByText(/共 3 个 MCP 工具/)).toBeInTheDocument()
  })

  it('无 MCP 工具时显示空态', async () => {
    apiMocks.listTools.mockResolvedValue([{ name: 'echo', description: '回声' }])
    render(<MCPDialog onClose={() => {}} />)
    expect(await screen.findByText('当前无可用 MCP 连接')).toBeInTheDocument()
  })

  it('工具加载失败显示错误', async () => {
    apiMocks.listTools.mockRejectedValue(new Error('network down'))
    render(<MCPDialog onClose={() => {}} />)
    expect(await screen.findByText('network down')).toBeInTheDocument()
  })

  it('普通用户只读：开关全部置为启用态且禁用，不渲染保存按钮', async () => {
    setUser('user', 'tutor')
    render(<MCPDialog onClose={() => {}} />)
    expect(await screen.findByText('weather')).toBeInTheDocument()
    const switches = screen.getAllByRole('switch')
    expect(switches.length).toBe(2)
    // 普通用户会话不设 mcp_servers → 全部已启用连接生效（只读全开）
    switches.forEach((sw) => expect(sw.getAttribute('aria-checked')).toBe('true'))
    switches.forEach((sw) => expect(sw).toBeDisabled())
    expect(screen.queryByRole('button', { name: '保存' })).not.toBeInTheDocument()
  })

  it('管理员可会话级勾选并保存 mcp_servers（子集）', async () => {
    const onClose = vi.fn()
    setUser('super_admin', '*')
    render(<MCPDialog sessionConfig={{}} onClose={onClose} />)
    expect(await screen.findByText('weather')).toBeInTheDocument()
    // 默认全选；取消 git → 只保留 weather
    fireEvent.click(screen.getByRole('switch', { name: '本会话启用 MCP git' }))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() =>
      expect(apiMocks.updateSessionConfig).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({ mcp_servers: ['weather'] }),
      ),
    )
    expect(onClose).toHaveBeenCalled()
  })

  it('管理员全选保存为空数组（= 全部已启用连接生效）', async () => {
    const onClose = vi.fn()
    setUser('super_admin', '*')
    // 会话已有子集配置 [weather]
    render(<MCPDialog sessionConfig={{ mcp_servers: ['weather'] }} onClose={onClose} />)
    await screen.findByText('weather')
    // 全选 git → 两个都勾上 = 全部生效
    fireEvent.click(screen.getByRole('switch', { name: '本会话启用 MCP git' }))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() =>
      expect(apiMocks.updateSessionConfig).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({ mcp_servers: [] }),
      ),
    )
    expect(onClose).toHaveBeenCalled()
  })

  it('会话已有 mcp_servers 子集时仅初始化勾选该子集', async () => {
    setUser('super_admin', '*')
    render(<MCPDialog sessionConfig={{ mcp_servers: ['git'] }} onClose={() => {}} />)
    await screen.findByText('weather')
    // 分组按 server 名排序：git < weather → switches[0]=git、[1]=weather
    const switches = screen.getAllByRole('switch')
    expect(switches[0].getAttribute('aria-checked')).toBe('true') // git 已勾
    expect(switches[1].getAttribute('aria-checked')).toBe('false') // weather 未勾
  })

  it('管理员全部取消保存为空数组 + mcp_servers_set=true（全不选）', async () => {
    const onClose = vi.fn()
    setUser('super_admin', '*')
    render(<MCPDialog sessionConfig={{ mcp_servers: ['weather', 'git'] }} onClose={onClose} />)
    await screen.findByText('weather')
    // 逐个取消到空 → 全不选
    fireEvent.click(screen.getByRole('switch', { name: '本会话启用 MCP weather' }))
    fireEvent.click(screen.getByRole('switch', { name: '本会话启用 MCP git' }))
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() =>
      expect(apiMocks.updateSessionConfig).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({ mcp_servers: [], mcp_servers_set: true }),
      ),
    )
    expect(onClose).toHaveBeenCalled()
  })

  it('会话 mcp_servers_set=true 且列表空时初始化为全不选（开关全关）', async () => {
    setUser('super_admin', '*')
    render(<MCPDialog sessionConfig={{ mcp_servers: [], mcp_servers_set: true }} onClose={() => {}} />)
    await screen.findByText('weather')
    const switches = screen.getAllByRole('switch')
    expect(switches.length).toBe(2)
    switches.forEach((sw) => expect(sw.getAttribute('aria-checked')).toBe('false'))
  })
})
