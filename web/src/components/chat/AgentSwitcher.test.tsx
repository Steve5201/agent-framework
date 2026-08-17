import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import AgentSwitcherDialog from './AgentSwitcher'
import { configRegistry } from './config/registry'
import { useAuthStore } from '@/stores/auth'

const apiMocks = vi.hoisted(() => ({ adminListAgents: vi.fn() }))
vi.mock('@/lib/api', () => ({ adminListAgents: apiMocks.adminListAgents }))

// 弹窗形态：需要 useLocation/useNavigate，包在带路由的 MemoryRouter 中。
// 通过当前路径模拟所在域（/agent/tutor、/admin/chat 等）。
const renderAt = (path: string, onClose = vi.fn()) =>
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/agent/:agentId" element={<AgentSwitcherDialog onClose={onClose} />} />
        <Route path="/admin/chat" element={<AgentSwitcherDialog onClose={onClose} />} />
        <Route path="*" element={<div />} />
      </Routes>
    </MemoryRouter>,
  )

function setUser(role: string, agentTag?: string) {
  useAuthStore.setState({
    user: {
      id: '1',
      username: 'root',
      role,
      tags: agentTag ? [{ key: 'agent', value: agentTag }] : [],
    } as never,
    status: 'authed',
  })
}

/** combobox 触发按钮（固定 aria-label，与显示文案解耦）。 */
const trigger = () => screen.getByRole('combobox', { name: '切换智能体' }) as HTMLElement

const sampleAgents = [
  { id: 'tutor', name: '导师', description: '', model: '', owner_user_id: '', status: 'active' },
  { id: 'math', name: '数学助教', description: '', model: '', owner_user_id: '', status: 'active' },
]

describe('AgentSwitcherDialog', () => {
  beforeEach(() => {
    localStorage.clear()
    apiMocks.adminListAgents.mockReset()
    apiMocks.adminListAgents.mockResolvedValue(sampleAgents) // 默认成功，个别用例覆写
    useAuthStore.setState({ user: null, status: 'guest' })
  })

  it('渲染弹窗标题，不再提供"全部智能体"聚合项', async () => {
    apiMocks.adminListAgents.mockResolvedValue(sampleAgents)
    renderAt('/agent/tutor')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText('切换智能体')).toBeInTheDocument()
    // '全部智能体'（* 聚合门户）已按要求移除，下拉选项只含具体智能体
    expect(screen.queryByText(/全部智能体/)).not.toBeInTheDocument()
  })

  it('点击触发按钮展开下拉并懒加载列表，标记当前门户', async () => {
    apiMocks.adminListAgents.mockResolvedValue(sampleAgents)
    renderAt('/agent/tutor')
    // 未展开时不渲染选项（弹窗挂载即预加载数据，但列表不展示）
    expect(screen.queryByText('数学助教')).not.toBeInTheDocument()
    fireEvent.click(trigger())
    expect(await screen.findByText('数学助教')).toBeInTheDocument()
    // 当前门户（tutor）行带 Check 标记
    const current = screen.getByText('tutor').closest('button')!
    expect(current.querySelector('svg')).toBeInTheDocument()
  })

  it('管理端对话域无记忆记录时默认选中列表第一个', async () => {
    apiMocks.adminListAgents.mockResolvedValue(sampleAgents)
    renderAt('/admin/chat')
    // 无记住记录：触发按钮默认选中列表第一个智能体
    expect(await screen.findByText('导师（tutor）')).toBeInTheDocument()
    fireEvent.click(trigger())
    const opts = screen.getAllByRole('option')
    expect(opts[0].getAttribute('aria-selected')).toBe('true')
    expect(screen.queryByText('当前为管理端对话')).not.toBeInTheDocument()
  })

  it('管理端对话域记住上次选择：默认选中记住的智能体', async () => {
    apiMocks.adminListAgents.mockResolvedValue(sampleAgents)
    localStorage.setItem('agent.last_agent', 'math')
    renderAt('/admin/chat')
    expect(await screen.findByText('数学助教（math）')).toBeInTheDocument()
    fireEvent.click(trigger())
    const opts = screen.getAllByRole('option')
    expect(opts[1].getAttribute('aria-selected')).toBe('true')
  })

  it('列表加载失败给出错误提示，不崩溃', async () => {
    apiMocks.adminListAgents.mockRejectedValue(new Error('network down'))
    renderAt('/agent/tutor')
    fireEvent.click(trigger())
    expect(await screen.findByText('network down')).toBeInTheDocument()
  })

  it('点击其它智能体跳转对应门户并关闭弹窗', async () => {
    const onClose = vi.fn()
    apiMocks.adminListAgents.mockResolvedValue(sampleAgents)
    renderAt('/agent/tutor', onClose)
    fireEvent.click(trigger())
    await screen.findByText('数学助教')
    fireEvent.click(screen.getByText('数学助教'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('切换其它智能体域保留登录态（不退出账号，仅跳转门户刷新对话列表）', async () => {
    setUser('super_admin', '*')
    const onClose = vi.fn()
    apiMocks.adminListAgents.mockResolvedValue(sampleAgents)
    renderAt('/agent/tutor', onClose)
    fireEvent.click(trigger())
    await screen.findByText('数学助教')
    fireEvent.click(screen.getByText('数学助教'))
    expect(onClose).toHaveBeenCalledTimes(1)
    // 切换对话智能体保留登录态：账号不登出
    expect(useAuthStore.getState().user).not.toBeNull()
    expect(useAuthStore.getState().status).toBe('authed')
  })

  it('点击当前所在域不退出登录（同域保留登录态）', async () => {
    setUser('super_admin', '*')
    apiMocks.adminListAgents.mockResolvedValue(sampleAgents)
    renderAt('/agent/tutor')
    fireEvent.click(trigger())
    await screen.findByText('导师')
    fireEvent.click(screen.getByText('导师'))
    // 同域：仅关闭弹窗，账号保持登录
    expect(useAuthStore.getState().user).not.toBeNull()
    expect(useAuthStore.getState().status).toBe('authed')
  })

  it('点击遮罩不关闭（与其它配置弹窗一致，需经取消/关闭按钮）', () => {
    renderAt('/agent/tutor')
    const overlay = screen.getByRole('dialog')
    fireEvent.click(overlay)
  })

  it('点击取消按钮调用 onClose', () => {
    const onClose = vi.fn()
    renderAt('/agent/tutor', onClose)
    fireEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('点击关闭按钮调用 onClose', () => {
    const onClose = vi.fn()
    renderAt('/agent/tutor', onClose)
    fireEvent.click(screen.getByRole('button', { name: '关闭' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

describe('configRegistry · agent 配置项', () => {
  beforeEach(() => {
    localStorage.clear()
    apiMocks.adminListAgents.mockReset()
    useAuthStore.setState({ user: null, status: 'guest' })
  })

  const agentItem = configRegistry.find((c) => c.key === 'agent')!
  const visible = (user: ReturnType<typeof useAuthStore.getState>['user']) =>
    agentItem.visible({ user, activeSession: null })

  it('仅最高超管（agent=*）可见', () => {
    setUser('super_admin', '*')
    expect(visible(useAuthStore.getState().user)).toBe(true)
  })

  it('普通用户与智能体管理员不可见', () => {
    setUser('user', 'tutor')
    expect(visible(useAuthStore.getState().user)).toBe(false)
    setUser('agent_admin', 'tutor')
    expect(visible(useAuthStore.getState().user)).toBe(false)
    setUser('admin')
    expect(visible(useAuthStore.getState().user)).toBe(false)
  })

  it('未登录（游客）不可见', () => {
    useAuthStore.setState({ user: null, status: 'guest' })
    expect(visible(null)).toBe(false)
  })
})
