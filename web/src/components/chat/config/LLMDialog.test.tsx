// LLMDialog.test.tsx —— 大模型选择弹窗单测（P3 反馈：只绑定确切模型 + 回退链）。
//
// 覆盖：
//   1. 会话绑定模型存在 → 直接显示该模型（修复"改完重开仍显示旧模型"）；
//   2. 绑定模型失效（被删除/禁用）→ 回退智能体默认配置大模型；
//   3. 智能体默认也没有 → 回退系统默认模型；
//   4. 保存时写死所选模型名（含系统默认也绑定，不再"选默认=不锁定"）。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import LLMDialog from './LLMDialog'
import { useAuthStore } from '@/stores/auth'
import { useChatStore } from '@/stores/chat'

const apiMocks = vi.hoisted(() => ({
  listPublicModels: vi.fn(),
  getAgentDefaults: vi.fn(),
  updateSessionConfig: vi.fn(),
}))
vi.mock('@/lib/api', () => ({
  listPublicModels: apiMocks.listPublicModels,
  getAgentDefaults: apiMocks.getAgentDefaults,
  updateSessionConfig: apiMocks.updateSessionConfig,
}))
vi.mock('@/lib/sse', () => ({ streamChat: vi.fn(async () => {}) }))
vi.mock('@/lib/localTools', () => ({
  LOCAL_TOOL_NAMES: new Set(),
  isTauri: vi.fn(() => false),
  runLocalShell: vi.fn(),
}))

// 列表：deepseek-chat 为系统默认；deepseek-r1 为普通可选模型。
const models = [
  { name: 'deepseek-chat', provider_name: 'DeepSeek', is_default: true },
  { name: 'deepseek-r1', provider_name: 'DeepSeek' },
]

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  useAuthStore.setState({ user: null, status: 'guest' })
  useChatStore.setState({ activeId: 's1' })
  apiMocks.listPublicModels.mockResolvedValue(models)
  apiMocks.getAgentDefaults.mockResolvedValue({})
  apiMocks.updateSessionConfig.mockResolvedValue({ id: 's1', config: {} })
})

describe('LLMDialog', () => {
  it('会话绑定模型存在时直接显示该模型，不触发智能体默认回退', async () => {
    render(<LLMDialog sessionConfig={{ model: 'deepseek-r1' }} agentId="tutor" onClose={() => {}} />)
    expect(await screen.findByText('deepseek-r1')).toBeInTheDocument()
    expect(apiMocks.getAgentDefaults).not.toHaveBeenCalled()
  })

  it('绑定模型失效时优先回退智能体默认配置大模型', async () => {
    // 会话绑定的模型已被删除/禁用 → 不在列表
    apiMocks.getAgentDefaults.mockResolvedValue({ model: 'deepseek-r1' })
    render(<LLMDialog sessionConfig={{ model: 'vanished-model' }} agentId="tutor" onClose={() => {}} />)
    // 回退到智能体默认（非系统默认的 r1，而非系统默认 chat）
    expect(await screen.findByText('deepseek-r1')).toBeInTheDocument()
    expect(apiMocks.getAgentDefaults).toHaveBeenCalledWith('tutor')
    // 系统默认徽章不应出现（选中的不是系统默认）
    await waitFor(() => expect(screen.queryByText('系统默认')).not.toBeInTheDocument())
  })

  it('智能体默认也没有时回退系统默认模型', async () => {
    apiMocks.getAgentDefaults.mockResolvedValue({})
    render(<LLMDialog sessionConfig={{ model: 'vanished-model' }} agentId="tutor" onClose={() => {}} />)
    expect(await screen.findByText('deepseek-chat')).toBeInTheDocument()
    expect(await screen.findByText('系统默认')).toBeInTheDocument()
  })

  it('未绑定模型时直接回退系统默认', async () => {
    render(<LLMDialog sessionConfig={{}} onClose={() => {}} />)
    expect(await screen.findByText('deepseek-chat')).toBeInTheDocument()
  })

  it('保存写死所选模型名（含系统默认也绑定）', async () => {
    const onClose = vi.fn()
    render(<LLMDialog sessionConfig={{ model: 'deepseek-r1' }} agentId="tutor" onClose={onClose} />)
    await screen.findByText('deepseek-r1')
    fireEvent.click(screen.getByRole('button', { name: /保存/ }))
    await waitFor(() =>
      expect(apiMocks.updateSessionConfig).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({ model: 'deepseek-r1' }),
      ),
    )
    expect(onClose).toHaveBeenCalled()
  })

  it('切换到系统默认后保存仍写死该模型名（不解除绑定）', async () => {
    const onClose = vi.fn()
    render(<LLMDialog sessionConfig={{ model: 'deepseek-r1' }} agentId="tutor" onClose={onClose} />)
    await screen.findByText('deepseek-r1')
    // 展开下拉，选择系统默认 deepseek-chat
    fireEvent.click(screen.getByRole('button', { name: /deepseek-r1|可选/ }))
    const option = await screen.findByRole('option', { name: /deepseek-chat/ })
    fireEvent.click(option)
    fireEvent.click(screen.getByRole('button', { name: /保存/ }))
    await waitFor(() =>
      expect(apiMocks.updateSessionConfig).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({ model: 'deepseek-chat' }),
      ),
    )
    expect(onClose).toHaveBeenCalled()
  })
})
