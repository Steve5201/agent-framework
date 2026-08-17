// KBDialog.test.tsx —— 知识库勾选弹窗单测。
//
// 覆盖：按会话域拉取知识库列表、既有 kb_ids 初始化勾选、保存透传 kb_ids
// 且保留其它配置、空列表与加载失败提示。交互为圆角开关（role="switch"）。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import KBDialog from './KBDialog'
import { useChatStore } from '@/stores/chat'

const apiMocks = vi.hoisted(() => ({
  listKbs: vi.fn(),
  updateSessionConfig: vi.fn(),
}))
vi.mock('@/lib/api', () => ({
  listKbs: apiMocks.listKbs,
  updateSessionConfig: apiMocks.updateSessionConfig,
}))
vi.mock('@/lib/sse', () => ({ streamChat: vi.fn(async () => {}) }))
vi.mock('@/lib/localTools', () => ({
  LOCAL_TOOL_NAMES: new Set(),
  isTauri: vi.fn(() => false),
  runLocalShell: vi.fn(),
}))

const kbs = [
  { id: 'kb_1', name: '高数上册', description: '课程讲义', doc_count: 12 },
  { id: 'kb_2', name: '物理实验', description: '', doc_count: 5 },
]

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  useChatStore.setState({ activeId: 's1' })
  apiMocks.listKbs.mockResolvedValue(kbs)
  apiMocks.updateSessionConfig.mockResolvedValue({ id: 's1', config: {} })
})

describe('KBDialog', () => {
  it('按会话域拉取列表并初始化勾选既有 kb_ids', async () => {
    render(<KBDialog agentId="tutor" sessionConfig={{ kb_ids: ['kb_1'] }} onClose={() => {}} />)
    expect(apiMocks.listKbs).toHaveBeenCalledWith('tutor')
    expect(await screen.findByText('高数上册')).toBeInTheDocument()
    expect(screen.getByText('物理实验')).toBeInTheDocument()
    expect(screen.getByText('12 文档')).toBeInTheDocument()
    const switches = screen.getAllByRole('switch')
    expect(switches[0].getAttribute('aria-checked')).toBe('true')
    expect(switches[1].getAttribute('aria-checked')).toBe('false')
  })

  it('保存透传 kb_ids 且保留既有配置', async () => {
    const onClose = vi.fn()
    render(
      <KBDialog
        agentId="tutor"
        sessionConfig={{ enabled_resources: ['search'], thinking: { enabled: true, reasoning_effort: 'high' }, kb_ids: ['kb_1'] }}
        onClose={onClose}
      />,
    )
    fireEvent.click(await screen.findByRole('switch', { name: '使用知识库 物理实验' })) // 追加勾选 kb_2
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() =>
      expect(apiMocks.updateSessionConfig).toHaveBeenCalledWith('s1', {
        enabled_resources: ['search'],
        thinking: { enabled: true, reasoning_effort: 'high' },
        kb_ids: ['kb_1', 'kb_2'],
        kb_ids_set: true,
      }),
    )
    expect(onClose).toHaveBeenCalled()
  })

  it('取消全部勾选后保存 kb_ids 为空数组（= 本会话不使用知识库检索）', async () => {
    const onClose = vi.fn()
    render(<KBDialog agentId="tutor" sessionConfig={{ kb_ids: ['kb_1'] }} onClose={onClose} />)
    fireEvent.click(await screen.findByRole('switch', { name: '使用知识库 高数上册' })) // 取消 kb_1
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() =>
      expect(apiMocks.updateSessionConfig).toHaveBeenCalledWith('s1', expect.objectContaining({ kb_ids: [] })),
    )
    expect(onClose).toHaveBeenCalled()
  })

  it('空列表给出提示', async () => {
    apiMocks.listKbs.mockResolvedValue([])
    render(<KBDialog agentId="tutor" onClose={() => {}} />)
    expect(await screen.findByText('当前智能体域暂无可选知识库')).toBeInTheDocument()
  })

  it('拉取失败显示错误且不保存', async () => {
    apiMocks.listKbs.mockRejectedValue(new Error('rag 未接入'))
    render(<KBDialog agentId="tutor" onClose={() => {}} />)
    expect(await screen.findByText('rag 未接入')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '保存' })).toBeDisabled()
  })
})
