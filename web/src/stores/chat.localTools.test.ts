// chat.localTools.test.ts —— 阶段3·本地工具（External=true）前端适配逻辑单测。
//
// 覆盖：
//  1. 浏览器降级：收到本地工具调用 → 立即回填"请使用桌面客户端"失败结果
//     （避免 agent 挂起等待 120s）；
//  2. 桌面端允许：resolveLocalCall(true) → 本地执行 → 回填执行结果；
//  3. 桌面端拒绝：resolveLocalCall(false) → 回填"用户拒绝"失败结果。
import { describe, it, expect, vi, beforeEach } from 'vitest'

// —— 先 mock 全部外部依赖，再 import store ——

vi.mock('@/lib/sse', () => ({
  streamChat: vi.fn(async () => {}),
}))

vi.mock('@/lib/localTools', () => ({
  LOCAL_TOOL_NAMES: new Set(['local_shell']),
  isTauri: vi.fn(() => false),
  runLocalShell: vi.fn(),
}))

vi.mock('@/lib/api', () => ({
  listSessions: vi.fn(async () => ({ sessions: [], total: 0 })),
  listMessages: vi.fn(async () => ({ messages: [] })),
  getSession: vi.fn(async () => ({ session: null })),
  updateSessionConfig: vi.fn(async () => {}),
  renameSession: vi.fn(async () => {}),
  setActiveVersion: vi.fn(async () => {}),
  deleteSession: vi.fn(async () => {}),
  deleteMessage: vi.fn(async () => {}),
  createSession: vi.fn(async () => {}),
  createBranch: vi.fn(async () => {}),
  regenerate: vi.fn(async () => {}),
  submitToolResult: vi.fn(async () => {}),
}))

import { isTauri, runLocalShell } from '@/lib/localTools'
import { submitToolResult } from '@/lib/api'
import { useChatStore, handleLocalToolCall } from '@/stores/chat'

const mockIsTauri = vi.mocked(isTauri)
const mockRunLocalShell = vi.mocked(runLocalShell)
const mockSubmitToolResult = vi.mocked(submitToolResult)

beforeEach(() => {
  vi.clearAllMocks()
  mockIsTauri.mockReturnValue(false)
  useChatStore.setState({ pendingLocalCall: null })
})

describe('浏览器降级：本地工具调用', () => {
  it('浏览器无本地能力时立即回填失败结果，避免 agent 挂起', async () => {
    mockIsTauri.mockReturnValue(false)
    await handleLocalToolCall('s1', 'call_1', 'local_shell', '{"command":"git status"}')

    expect(mockSubmitToolResult).toHaveBeenCalledTimes(1)
    const [sessionId, toolCallId, content, isError] = mockSubmitToolResult.mock.calls[0]
    expect(sessionId).toBe('s1')
    expect(toolCallId).toBe('call_1')
    expect(content).toContain('请使用桌面客户端')
    expect(isError).toBe(true)
    // 浏览器不弹确认弹窗、不执行本地命令
    expect(useChatStore.getState().pendingLocalCall).toBeNull()
    expect(mockRunLocalShell).not.toHaveBeenCalled()
  })

  it('参数 JSON 解析失败时也按降级处理（不抛异常）', async () => {
    mockIsTauri.mockReturnValue(false)
    await expect(handleLocalToolCall('s1', 'call_2', 'local_shell', '不是JSON')).resolves.toBeUndefined()
    expect(mockSubmitToolResult).toHaveBeenCalledTimes(1)
  })
})

describe('桌面端：确认弹窗决策', () => {
  it('允许执行：本地执行成功 → 回填执行结果', async () => {
    mockIsTauri.mockReturnValue(true)
    mockRunLocalShell.mockResolvedValue({ content: '工作区干净', isError: false })

    useChatStore.setState({
      pendingLocalCall: { sessionId: 's1', toolCallId: 'call_1', name: 'local_shell', command: 'git status' },
    })
    await useChatStore.getState().resolveLocalCall(true)

    expect(mockRunLocalShell).toHaveBeenCalledWith('git status', undefined)
    expect(mockSubmitToolResult).toHaveBeenCalledWith('s1', 'call_1', '工作区干净', false)
    expect(useChatStore.getState().pendingLocalCall).toBeNull()
  })

  it('允许执行：本地执行失败 → 回填失败结果', async () => {
    mockIsTauri.mockReturnValue(true)
    mockRunLocalShell.mockResolvedValue({ content: '权限不足', isError: true })

    useChatStore.setState({
      pendingLocalCall: { sessionId: 's1', toolCallId: 'call_1', name: 'local_shell', command: 'rm -rf /tmp/x' },
    })
    await useChatStore.getState().resolveLocalCall(true)

    expect(mockSubmitToolResult).toHaveBeenCalledWith('s1', 'call_1', '权限不足', true)
  })

  it('拒绝执行：回填"用户拒绝"的失败结果，agent 据此调整策略', async () => {
    mockIsTauri.mockReturnValue(true)
    useChatStore.setState({
      pendingLocalCall: { sessionId: 's1', toolCallId: 'call_1', name: 'local_shell', command: 'rm -rf /' },
    })
    await useChatStore.getState().resolveLocalCall(false)

    expect(mockRunLocalShell).not.toHaveBeenCalled()
    expect(mockSubmitToolResult).toHaveBeenCalledWith('s1', 'call_1', '用户拒绝在本地执行该命令', true)
  })

  it('无挂起调用时 resolveLocalCall 为空操作', async () => {
    mockIsTauri.mockReturnValue(true)
    await useChatStore.getState().resolveLocalCall(true)
    expect(mockRunLocalShell).not.toHaveBeenCalled()
    expect(mockSubmitToolResult).not.toHaveBeenCalled()
  })

  it('桌面端收到本地工具调用 → 弹出确认弹窗（不立即回填）', async () => {
    mockIsTauri.mockReturnValue(true)
    await handleLocalToolCall('s1', 'call_1', 'local_shell', '{"command":"git status","cwd":"/tmp"}')

    expect(useChatStore.getState().pendingLocalCall).toEqual({
      sessionId: 's1',
      toolCallId: 'call_1',
      name: 'local_shell',
      command: 'git status',
      cwd: '/tmp',
    })
    expect(mockSubmitToolResult).not.toHaveBeenCalled()
  })
})
