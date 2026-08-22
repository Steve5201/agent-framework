import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useParams } from 'react-router-dom'
import LoginPage from './LoginPage'
import { useAuthStore } from '@/stores/auth'

// 隔离网络层：表单校验与交互逻辑是纯前端行为
const apiMocks = vi.hoisted(() => ({
  login: vi.fn(),
  register: vi.fn(),
  mergeGuestSessions: vi.fn(),
  checkAgentDomain: vi.fn(),
}))
vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  login: apiMocks.login,
  register: apiMocks.register,
  mergeGuestSessions: apiMocks.mergeGuestSessions,
  checkAgentDomain: apiMocks.checkAgentDomain,
}))

// 记住密码：mock 掉凭据存取，断言勾选行为
const rememberMocks = vi.hoisted(() => ({
  saveRemembered: vi.fn(),
  loadRemembered: vi.fn(),
  clearRemembered: vi.fn(),
}))
vi.mock('@/lib/remember', () => rememberMocks)

// 需要 Routes 包装：/login/:agentId 是路由参数，否则 useParams 拿不到值。
// 门户化后登录页必须带 agentId（缺省由 App 路由重定向），默认路径固定 /login/tutor。
// Landed：捕获登录成功后的落地路由，断言跳转目标（方案 B：管理员落角色归属域）。
const Landed = () => {
  const { agentId } = useParams()
  return <div data-testid="landed" data-agent={agentId} />
}

const renderPage = (path = '/login/tutor') =>
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/login/:agentId" element={<LoginPage />} />
        <Route path="/agent/:agentId" element={<Landed />} />
        {/* 登录成功会 Navigate 到 /，通配兜底避免"无路由匹配"告警 */}
        <Route path="*" element={<div />} />
      </Routes>
    </MemoryRouter>,
  )

// 域守卫：非 '*' 门户先校验域存在才渲染表单，普通用例需等待表单出现
const renderAndWait = async (path = '/login/tutor') => {
  renderPage(path)
  await screen.findByLabelText('用户名')
}

describe('LoginPage', () => {
  beforeEach(() => {
    localStorage.clear()
    // zustand store 是模块级单例，重置登录态，避免上个用例登录成功后
    // 本用例渲染时被 <Navigate to="/"> 直接跳走（DOM 变空）。
    useAuthStore.setState({ user: null, status: 'loading' })
    apiMocks.login.mockReset()
    apiMocks.register.mockReset()
    apiMocks.mergeGuestSessions.mockReset()
    apiMocks.checkAgentDomain.mockReset()
    apiMocks.checkAgentDomain.mockResolvedValue({ exists: true, id: 'tutor', name: 'tutor', status: 1 })
    rememberMocks.saveRemembered.mockClear()
    rememberMocks.clearRemembered.mockClear()
    rememberMocks.loadRemembered.mockResolvedValue(null) // 默认无已存凭据
  })

  it('门户登录页渲染登录表单与门户标识', async () => {
    await renderAndWait()
    expect(screen.getByLabelText('用户名')).toBeInTheDocument()
    expect(screen.getByLabelText('密码')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '登录' })).toBeInTheDocument()
    // 地址即门户：标题与副标题均体现智能体 ID
    expect(screen.getByText('星云 · tutor')).toBeInTheDocument()
    expect(screen.getByText(/登录以继续对话（智能体 tutor）/)).toBeInTheDocument()
  })

  it('普通门户提供注册入口，注册提交带门户 ID', async () => {
    apiMocks.register.mockResolvedValue({ id: '2', username: 'alice' })
    apiMocks.login.mockResolvedValue({
      access_token: 'a',
      refresh_token: 'r',
      expires_in: 900,
      user: { id: '2', username: 'alice', role: 'user' },
    })
    await renderAndWait('/login/math')
    fireEvent.click(screen.getByText('去注册'))
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'Passw0rd1' } })
    fireEvent.change(screen.getByLabelText('确认密码'), { target: { value: 'Passw0rd1' } })
    fireEvent.click(screen.getByRole('button', { name: '注册并登录' }))
    await waitFor(() =>
      expect(apiMocks.register).toHaveBeenCalledWith('alice', 'Passw0rd1', 'math'),
    )
  })

  it('超管门户（*）隐藏注册入口与游客返回，仅提供登录', () => {
    renderPage('/login/*')
    expect(screen.getByText('星云 · 超管专属门户')).toBeInTheDocument()
    expect(screen.queryByText('去注册')).not.toBeInTheDocument()
    // 超管域游客禁止对话：登录页不再提供"以游客身份继续"
    expect(screen.queryByText('以游客身份继续（不登录）')).not.toBeInTheDocument()
  })

  it('无门户参数时重定向到默认智能体聊天页', () => {
    renderPage('/login')
    // Navigate 替换渲染为通配兜底空节点；断言未渲染登录表单即可
    expect(screen.queryByLabelText('用户名')).not.toBeInTheDocument()
  })

  it('空用户名提交给出具体错误提示', async () => {
    await renderAndWait()
    fireEvent.click(screen.getByRole('button', { name: '登录' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('请输入用户名')
  })

  it('密码过短给出具体错误提示', async () => {
    await renderAndWait()
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: '123' } })
    fireEvent.click(screen.getByRole('button', { name: '登录' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('密码至少 8 位，且须同时包含字母与数字')
  })

  it('注册模式下两次密码不一致给出具体错误提示', async () => {
    await renderAndWait('/login/tutor')
    fireEvent.click(screen.getByText('去注册'))
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'Passw0rd1' } })
    fireEvent.change(screen.getByLabelText('确认密码'), { target: { value: 'Passw0rd2' } })
    fireEvent.click(screen.getByRole('button', { name: '注册并登录' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('两次输入的密码不一致')
  })

  it('服务器地址设置区可见且可保存', async () => {
    await renderAndWait()
    const input = screen.getByLabelText('服务器地址')
    expect(input).toBeInTheDocument()
    fireEvent.change(input, { target: { value: 'http://10.0.0.2:8080' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(screen.getByText('已保存，立即生效')).toBeInTheDocument()
    expect(localStorage.getItem('agent.server_url')).toBe('http://10.0.0.2:8080')
  })

  it('登录成功后合并游客会话并清除游客 ID', async () => {
    localStorage.setItem('agent.guest_id', '550e8400-e29b-41d4-a716-446655440000')
    apiMocks.login.mockResolvedValue({
      access_token: 'a',
      refresh_token: 'r',
      expires_in: 900,
      user: { id: '1', username: 'alice', role: 'user' },
    })
    apiMocks.mergeGuestSessions.mockResolvedValue(3)
    await renderAndWait()
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'Passw0rd1' } })
    fireEvent.click(screen.getByRole('button', { name: '登录' }))
    await waitFor(() =>
      expect(apiMocks.mergeGuestSessions).toHaveBeenCalledWith('550e8400-e29b-41d4-a716-446655440000'),
    )
    // 合并完成后本地游客 ID 必须清除，避免后续以游客身份混入账号数据
    expect(localStorage.getItem('agent.guest_id')).toBeNull()
  })

  it('无本地游客 ID 时不调用合并接口', async () => {
    apiMocks.login.mockResolvedValue({
      access_token: 'a',
      refresh_token: 'r',
      expires_in: 900,
      user: { id: '1', username: 'alice', role: 'user' },
    })
    await renderAndWait()
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'Passw0rd1' } })
    fireEvent.click(screen.getByRole('button', { name: '登录' }))
    await waitFor(() => expect(apiMocks.login).toHaveBeenCalled())
    expect(apiMocks.mergeGuestSessions).not.toHaveBeenCalled()
  })
})

// ---------------------------------------------------------------------------
// 记住密码（记住凭据勾选 / 回填 / 清除）
// ---------------------------------------------------------------------------
describe('LoginPage 记住密码', () => {
  beforeEach(() => {
    localStorage.clear()
    useAuthStore.setState({ user: null, status: 'loading' })
    apiMocks.login.mockReset()
    apiMocks.checkAgentDomain.mockReset()
    apiMocks.checkAgentDomain.mockResolvedValue({ exists: true, id: 'tutor', name: 'tutor', status: 1 })
    rememberMocks.saveRemembered.mockClear()
    rememberMocks.clearRemembered.mockClear()
  })

  const fillLoginForm = () => {
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'Passw0rd1' } })
  }

  it('勾选记住密码并登录成功后保存凭据', async () => {
    apiMocks.login.mockResolvedValue({
      access_token: 'a',
      refresh_token: 'r',
      expires_in: 900,
      user: { id: '1', username: 'alice', role: 'user' },
    })
    rememberMocks.loadRemembered.mockResolvedValue(null)
    await renderAndWait()
    fillLoginForm()
    fireEvent.click(screen.getByRole('checkbox')) // 勾选记住密码
    fireEvent.click(screen.getByRole('button', { name: '登录' }))
    await waitFor(() =>
      expect(rememberMocks.saveRemembered).toHaveBeenCalledWith('tutor', 'alice', 'Passw0rd1'),
    )
    expect(localStorage.getItem('agent.remember_me')).toBe('1')
  })

  it('未勾选时登录成功则清除已存凭据', async () => {
    apiMocks.login.mockResolvedValue({
      access_token: 'a',
      refresh_token: 'r',
      expires_in: 900,
      user: { id: '1', username: 'alice', role: 'user' },
    })
    rememberMocks.loadRemembered.mockResolvedValue(null)
    await renderAndWait()
    fillLoginForm()
    fireEvent.click(screen.getByRole('button', { name: '登录' }))
    await waitFor(() => expect(rememberMocks.clearRemembered).toHaveBeenCalledWith('tutor'))
    expect(localStorage.getItem('agent.remember_me')).toBeNull()
  })

  it('有已存凭据时自动回填用户名密码并勾选', async () => {
    rememberMocks.loadRemembered.mockResolvedValue({ username: 'alice', password: 'Passw0rd1' })
    await renderAndWait()
    await waitFor(() => {
      expect((screen.getByLabelText('用户名') as HTMLInputElement).value).toBe('alice')
      expect((screen.getByLabelText('密码') as HTMLInputElement).value).toBe('Passw0rd1')
    })
    expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// 登录落地域（方案 B：管理员登录后不再一律跳 /admin/chat，按角色归属会话域）
// ---------------------------------------------------------------------------
describe('LoginPage 登录落地域', () => {
  beforeEach(() => {
    localStorage.clear()
    useAuthStore.setState({ user: null, status: 'loading' })
    apiMocks.login.mockReset()
    apiMocks.mergeGuestSessions.mockReset()
    apiMocks.mergeGuestSessions.mockResolvedValue(0)
    apiMocks.checkAgentDomain.mockReset()
    apiMocks.checkAgentDomain.mockResolvedValue({ exists: true, id: 'tutor', name: 'tutor', status: 1 })
    rememberMocks.loadRemembered.mockResolvedValue(null)
  })

  const fillAndSubmit = async (name: string) => {
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: name } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'Passw0rd1' } })
    fireEvent.click(screen.getByRole('button', { name: '登录' }))
  }

  const expectLanded = (agentId: string) =>
    waitFor(() =>
      expect(screen.getByTestId('landed').getAttribute('data-agent')).toBe(agentId),
    )

  it('超管登录后落地 /agent/*（全部域，非 /admin/chat）', async () => {
    apiMocks.login.mockResolvedValue({
      access_token: 'a',
      refresh_token: 'r',
      expires_in: 900,
      user: { id: '1', username: 'root', role: 'super_admin', tags: [{ key: 'agent', value: '*' }] },
    })
    renderPage('/login/*')
    await fillAndSubmit('root')
    await expectLanded('*')
  })

  it('绑定域管理员登录后落地其绑定智能体域', async () => {
    apiMocks.login.mockResolvedValue({
      access_token: 'a',
      refresh_token: 'r',
      expires_in: 900,
      user: { id: '2', username: 'am', role: 'agent_admin', tags: [{ key: 'agent', value: 'math' }] },
    })
    renderPage('/login/*')
    await fillAndSubmit('am')
    await expectLanded('math')
  })

  it('普通用户登录后落地对应智能体门户（行为不变）', async () => {
    apiMocks.login.mockResolvedValue({
      access_token: 'a',
      refresh_token: 'r',
      expires_in: 900,
      user: { id: '3', username: 'alice', role: 'user' },
    })
    await renderAndWait('/login/tutor')
    await fillAndSubmit('alice')
    await expectLanded('tutor')
  })
})

// ---------------------------------------------------------------------------
// 域守卫（阶段3·多租户门户化）：/login/:agentId 直连孤儿/停用域不渲染表单
// ---------------------------------------------------------------------------
describe('LoginPage 域守卫', () => {
  beforeEach(() => {
    localStorage.clear()
    useAuthStore.setState({ user: null, status: 'loading' })
    apiMocks.checkAgentDomain.mockReset()
    rememberMocks.loadRemembered.mockResolvedValue(null)
  })

  it('孤儿域（不存在）不渲染登录表单，提示门户不存在', async () => {
    apiMocks.checkAgentDomain.mockResolvedValue({
      exists: false,
      id: 'mi',
      name: '',
      status: 0,
    })
    renderPage('/login/mi')
    expect(await screen.findByText('门户不存在或已停用')).toBeInTheDocument()
    expect(screen.queryByLabelText('用户名')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '返回默认门户' })).toBeInTheDocument()
  })

  it('已停用域同样拦截表单渲染', async () => {
    apiMocks.checkAgentDomain.mockResolvedValue({
      exists: true,
      id: 'tutor',
      name: 'tutor',
      status: 0,
    })
    renderPage('/login/tutor')
    expect(await screen.findByText('门户不存在或已停用')).toBeInTheDocument()
    expect(screen.queryByLabelText('用户名')).not.toBeInTheDocument()
  })

  it('域校验失败（网络异常）不阻断表单，后端仍有硬校验兜底', async () => {
    apiMocks.checkAgentDomain.mockRejectedValue(new Error('network down'))
    renderPage('/login/tutor')
    expect(await screen.findByLabelText('用户名')).toBeInTheDocument()
  })

  it('超管门户（*）跳过域校验且不发起请求', async () => {
    renderPage('/login/*')
    expect(screen.getByLabelText('用户名')).toBeInTheDocument()
    expect(apiMocks.checkAgentDomain).not.toHaveBeenCalled()
  })
})
