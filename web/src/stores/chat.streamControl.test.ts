// chat.streamControl.test.ts —— 流式生成控制（打断/停止/重新生成中断）行为单测。
//
// 覆盖本阶段两处体验修复：
//  1. 打断（用户停止）或流中错误后，后端已把部分内容落库（persistPartialOnError +
//     context.WithoutCancel），前端重拉历史回填 serverId，让被打断的气泡恢复
//     删除/重新生成/分支按钮（此前只剩复制）；
//  2. 重新生成进行中可停止：stopStreaming 只中止 regenerate 流、不预标记消息，
//     regenerateMessage 收尾重拉历史恢复原回答（后端已回滚截断）。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import type { HistoryMessage } from '@/types/api'

// —— 先 mock 全部外部依赖，再 import store ——
const apiMocks = vi.hoisted(() => ({
  fetchMessages: vi.fn(async (): Promise<HistoryMessage[]> => []),
  listSessions: vi.fn(async () => ({ sessions: [], total: 0 })),
}))

const sseMocks = vi.hoisted(() => {
  const st: {
    chat: { handlers: Record<string, (...a: unknown[]) => unknown> | null; resolve: (() => void) | null }
    regen: { handlers: Record<string, (...a: unknown[]) => unknown> | null; resolve: (() => void) | null }
  } = {
    chat: { handlers: null, resolve: null },
    regen: { handlers: null, resolve: null },
  }
  return {
    streamChat: vi.fn((_sid: string, _text: string, handlers: typeof st.chat.handlers, signal?: AbortSignal) =>
      new Promise<void>((resolve) => {
        st.chat.handlers = handlers
        st.chat.resolve = resolve
        signal?.addEventListener('abort', () => resolve())
      }),
    ),
    regenerateStream: vi.fn(
      (_sid: string, _mid: string, handlers: typeof st.regen.handlers, signal?: AbortSignal) =>
        new Promise<void>((resolve) => {
          st.regen.handlers = handlers
          st.regen.resolve = resolve
          signal?.addEventListener('abort', () => resolve())
        }),
    ),
    getChatHandlers: () => st.chat.handlers,
    getRegenHandlers: () => st.regen.handlers,
    getChatResolve: () => st.chat.resolve,
    getRegenResolve: () => st.regen.resolve,
    reset: () => {
      st.chat.handlers = null
      st.chat.resolve = null
      st.regen.handlers = null
      st.regen.resolve = null
    },
  }
})

vi.mock('@/lib/api', () => ({
  fetchMessages: apiMocks.fetchMessages,
  listSessions: apiMocks.listSessions,
}))
vi.mock('@/lib/sse', () => ({
  streamChat: sseMocks.streamChat,
  regenerateStream: sseMocks.regenerateStream,
}))
vi.mock('@/lib/localTools', () => ({
  LOCAL_TOOL_NAMES: new Set(['local_shell']),
  isTauri: vi.fn(() => false),
  runLocalShell: vi.fn(),
}))

import { useChatStore } from '@/stores/chat'

/** 构造 HistoryMessage 测试对象（reasoning/tool_call_id/tool_calls 由后端补齐）。 */
function historyMsg(partial: Omit<HistoryMessage, 'reasoning' | 'tool_call_id' | 'tool_calls'>): HistoryMessage {
  return { reasoning: '', tool_call_id: '', tool_calls: '', ...partial }
}

beforeEach(() => {
  vi.useRealTimers()
  vi.clearAllMocks()
  sseMocks.reset()
  useChatStore.setState({
    agentId: '',
    activeId: null,
    sessions: [],
    messages: [],
    sending: false,
    busySessions: [],
    aborter: null,
    regeneratingId: null,
    pendingLocalCall: null,
  })
})

describe('stopStreaming · 普通对话流', () => {
  it('中止请求并把未完成消息标记为 done（保留已收内容）', () => {
    const aborter = new AbortController()
    useChatStore.setState({
      sending: true,
      aborter,
      messages: [
        { id: 'u', role: 'user', content: 'hi', status: 'done' },
        { id: 'b', role: 'assistant', content: '部分内容', status: 'streaming' },
      ],
    })
    const abortSpy = vi.spyOn(aborter, 'abort')
    useChatStore.getState().stopStreaming()
    expect(abortSpy).toHaveBeenCalledTimes(1)
    const st = useChatStore.getState()
    expect(st.sending).toBe(false)
    expect(st.aborter).toBeNull()
    expect(st.messages[1].status).toBe('done')
    expect(st.messages[1].content).toBe('部分内容')
  })
})

describe('stopStreaming · 重新生成流', () => {
  it('中止而不预标记消息，收尾重拉历史恢复原回答', async () => {
    useChatStore.setState({
      activeId: 's1',
      messages: [
        { id: 'u', role: 'user', content: '问题', status: 'done', serverId: 'm-user' },
        { id: 'm1', role: 'assistant', content: '原回答', status: 'done', serverId: 'm-bot' },
      ],
    })
    // 打断后后端已回滚截断 → 重拉历史仍返回原回答
    apiMocks.fetchMessages.mockResolvedValueOnce([
      historyMsg({ id: 'm-user', role: 'user', content: '问题', round_no: 1, version: 0, total_versions: 1 }),
      historyMsg({ id: 'm-bot', role: 'assistant', content: '原回答', round_no: 1, version: 0, total_versions: 1 }),
    ])

    const p = useChatStore.getState().regenerateMessage('m1')
    // 同步部分：进入流式占位（正文清空）
    expect(useChatStore.getState().regeneratingId).toBe('m1')
    const handlers = sseMocks.getRegenHandlers()!
    handlers.onDelta('新回答片段')

    const aborter = useChatStore.getState().aborter!
    const abortSpy = vi.spyOn(aborter, 'abort')
    useChatStore.getState().stopStreaming()
    expect(abortSpy).toHaveBeenCalledTimes(1)
    // 不预标记 done：恢复由 regenerateMessage 收尾统一完成
    expect(useChatStore.getState().messages.find((m) => m.id === 'm1')?.status).toBe('streaming')

    await p
    const st = useChatStore.getState()
    expect(st.regeneratingId).toBeNull()
    expect(st.aborter).toBeNull()
    const restored = st.messages.find((m) => m.serverId === 'm-bot')
    expect(restored?.content).toBe('原回答')
  })
})

describe('sendMessage · 停止/出错后回填 serverId', () => {
  it('停止且后端已入库：重拉历史回填 serverId（恢复操作按钮）', async () => {
    vi.useFakeTimers()
    try {
      useChatStore.setState({ activeId: 's1' })
      // 首次拉取：本轮尚未落库（旧历史）；重试后：已落库（含部分回答）
      apiMocks.fetchMessages
        .mockResolvedValueOnce([
          historyMsg({ id: 'm-user', role: 'user', content: 'hello', round_no: 1, version: 0, total_versions: 1 }),
        ])
        .mockResolvedValue([
          historyMsg({ id: 'm-user', role: 'user', content: 'hello', round_no: 1, version: 0, total_versions: 1 }),
          historyMsg({ id: 'm-bot', role: 'assistant', content: '部分内容', round_no: 1, version: 0, total_versions: 1 }),
        ])

      const p = useChatStore.getState().sendMessage('hello')
      const handlers = sseMocks.getChatHandlers()!
      handlers.onDelta('部分')
      handlers.onDelta('内容')
      useChatStore.getState().stopStreaming()
      await vi.advanceTimersByTimeAsync(400)
      await p

      const st = useChatStore.getState()
      expect(st.sending).toBe(false)
      const bot = st.messages.at(-1)
      expect(bot?.serverId).toBe('m-bot')
      expect(bot?.content).toBe('部分内容')
    } finally {
      vi.useRealTimers()
    }
  })

  it('停止但后端未入库（极快停止）：重试后仍无该轮，保留本地气泡不消失', async () => {
    vi.useFakeTimers()
    try {
      useChatStore.setState({ activeId: 's1' })
      // 历史里只有 user（本轮 assistant 一直未落库）
      apiMocks.fetchMessages.mockResolvedValue([
        historyMsg({ id: 'm-user', role: 'user', content: 'hello', round_no: 1, version: 0, total_versions: 1 }),
      ])

      const p = useChatStore.getState().sendMessage('hello')
      const handlers = sseMocks.getChatHandlers()!
      handlers.onDelta('部分内容')
      useChatStore.getState().stopStreaming()
      // 停止场景重试 3 次（2 次 350ms 间隔）
      await vi.advanceTimersByTimeAsync(800)
      await p

      const bot = useChatStore.getState().messages.find((m) => m.role === 'assistant')
      expect(bot?.content).toBe('部分内容') // 本地内容保留
      expect(bot?.serverId).toBeUndefined() // 未入库则不给 serverId
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('sendMessage · 发送互斥', () => {
  it('重新生成进行中禁止再次发送（不重复开流）', async () => {
    useChatStore.setState({ activeId: 's1', regeneratingId: 'm1' })
    await useChatStore.getState().sendMessage('hello')
    expect(sseMocks.getChatHandlers()).toBeNull()
    expect(useChatStore.getState().messages).toHaveLength(0)
  })
})

describe('sendMessage · 编排任务事件按 kind 分流（P4-N）', () => {
  // 子任务思考/工具/正文增量各自累积，不串到其它节点、互不混排。
  it('reasoning/tool_start/tool_end/text 分流到对应字段', async () => {
    apiMocks.fetchMessages.mockResolvedValue([])
    useChatStore.setState({ activeId: 's1' })

    void useChatStore.getState().sendMessage('hello')
    const handlers = sseMocks.getChatHandlers()!

    // 两个并行子任务的事件交错到达：A 思考中 → B 调工具 → A 正文 → A 工具结束
    handlers.onTaskStatus({ taskType: 'task_started', taskId: 'content', status: 'running', error: '', content: undefined, totalTokens: 0 })
    handlers.onTaskStatus({ taskType: 'task_content', taskId: 'content', status: 'running', error: '', content: '先分析', kind: 'reasoning', totalTokens: 0 })
    handlers.onTaskStatus({ taskType: 'task_content', taskId: 'research', status: 'running', error: '', content: 'web_search', kind: 'tool_start', totalTokens: 0 })
    handlers.onTaskStatus({ taskType: 'task_content', taskId: 'content', status: 'running', error: '', content: '正文开始', kind: 'text', totalTokens: 0 })
    handlers.onTaskStatus({ taskType: 'task_content', taskId: 'research', status: 'running', error: '', content: 'web_search', kind: 'tool_end', totalTokens: 0 })
    handlers.onTaskStatus({ taskType: 'task_finished', taskId: 'research', status: 'completed', error: '', content: undefined, totalTokens: 100 })

    const bot = useChatStore.getState().messages.find((m) => m.role === 'assistant')
    const contentTask = bot?.tasks?.find((t) => t.id === 'content')
    const researchTask = bot?.tasks?.find((t) => t.id === 'research')

    expect(contentTask?.reasoning).toBe('先分析') // 思考独立累积
    expect(contentTask?.content).toBe('正文开始') // 正文独立累积，与思考不混排
    expect(contentTask?.activeTool).toBeUndefined() // 未调工具
    expect(researchTask?.activeTool).toBeUndefined() // tool_end 已清空当前工具
    expect(researchTask?.toolHistory).toEqual(['web_search']) // 工具履历
    expect(researchTask?.content).toBeUndefined() // 未收到正文
    expect(researchTask?.totalTokens).toBe(100) // 终态事件带 token
    expect(researchTask?.status).toBe('completed')
  })
})

describe('selectSession · 切换会话不打断后台流（完整落库）', () => {
  it('切走不 abort，后台流完成后不回填当前视图，sending 随活动会话重算', async () => {
    // s2 的历史：切过去后当前视图应显示 s2 内容
    apiMocks.fetchMessages.mockResolvedValue([
      historyMsg({ id: 'm-s2', role: 'user', content: 's2 问题', round_no: 1, version: 0, total_versions: 1 }),
    ])
    useChatStore.setState({ activeId: 's1' })

    // s1 发起流式生成
    const p = useChatStore.getState().sendMessage('s1 问题')
    expect(useChatStore.getState().busySessions).toEqual(['s1'])
    expect(useChatStore.getState().sending).toBe(true)

    // 切换会话：不触发 abort（流继续后台跑），sending 随新会话重算为 false
    await useChatStore.getState().selectSession('s2')
    expect(sseMocks.getChatHandlers()).not.toBeNull() // 流仍在
    expect(useChatStore.getState().activeId).toBe('s2')
    expect(useChatStore.getState().sending).toBe(false)
    expect(useChatStore.getState().busySessions).toEqual(['s1']) // 旧流仍在跑
    expect(useChatStore.getState().messages.map((m) => m.id)).toEqual(['m-s2'])

    // 后台流正常完成：只落库刷新列表，不回填/覆盖当前视图
    const handlers = sseMocks.getChatHandlers()!
    handlers.onDelta('完整回答内容')
    handlers.onDone()
    sseMocks.getChatResolve()?.()
    await p

    const st = useChatStore.getState()
    expect(st.sending).toBe(false)
    expect(st.busySessions).toEqual([])
    // 当前视图仍是 s2 历史，未被 s1 的完成回填覆盖
    expect(st.messages.map((m) => m.id)).toEqual(['m-s2'])
    expect(st.messages.find((m) => m.role === 'assistant')).toBeUndefined()
  })
})
