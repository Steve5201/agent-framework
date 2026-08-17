import { describe, it, expect, vi, beforeEach } from 'vitest'
import { streamChat } from './sse'
import type { SSEDoneEvent } from '@/types/api'

// 部分 mock：保留 getApiBase 原实现，替换 refreshAccessToken 为可控桩。
vi.mock('./api', async (importOriginal) => {
  const mod = await importOriginal<typeof import('./api')>()
  return { ...mod, refreshAccessToken: vi.fn() }
})
import { refreshAccessToken } from './api'

/** 构造一个返回指定 SSE 分片的 Response。 */
function sseResponse(chunks: string[]): Response {
  const encoder = new TextEncoder()
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const c of chunks) controller.enqueue(encoder.encode(c))
      controller.close()
    },
  })
  return new Response(stream, {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream' },
  })
}

describe('streamChat（SSE 客户端）', () => {
  beforeEach(() => {
    // storage.ts 读取 localStorage；预置 access token
    localStorage.setItem('agent.access_token', 'test-token')
    vi.unstubAllGlobals()
    vi.mocked(refreshAccessToken).mockReset()
  })

  it('累积 delta 并在 done 事件携带统计', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      sseResponse([
        'data: {"type":"delta","content":"你好"}\n\n',
        // 模拟分片边界：同一事件拆两段到达
        'data: {"type":"delta","content":"，世',
        '界"}\n\n',
        ': keepalive\n\n',
        'event: done\ndata: {"type":"done","rounds":2,"tool_calls":1,"prompt_tokens":10,"completion_tokens":8,"total_tokens":18}\n\n',
      ]),
    )
    vi.stubGlobal('fetch', fetchMock)

    const deltas: string[] = []
    // 用 mock 捕获 done 事件，避免闭包赋值变量被 TS 控制流收窄
    const onDone = vi.fn<(e: SSEDoneEvent) => void>()
    await streamChat('1', 'hi', {
      onDelta: (d) => deltas.push(d),
      onDone,
    })

    expect(deltas.join('')).toBe('你好，世界')
    expect(onDone).toHaveBeenCalledOnce()
    const done = onDone.mock.calls[0][0]
    expect(done.rounds).toBe(2)
    expect(done.tool_calls).toBe(1)
    expect(done.total_tokens).toBe(18)
    // 请求目标与方法
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining('/v1/agent/sessions/1/chat/stream'),
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('分发 reasoning / tool_call / tool_result 事件（思考+工具过程可视化）', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      sseResponse([
        'data: {"type":"reasoning","content":"先想想"}\n\n',
        'data: {"type":"reasoning","content":"再查表"}\n\n',
        'data: {"type":"tool_call","tool_call_id":"call_1","name":"get_weather","arguments":"{\\"city\\":\\"北京\\"}"}\n\n',
        'data: {"type":"tool_result","tool_call_id":"call_1","name":"get_weather","content":"晴，25℃"}\n\n',
        'data: {"type":"delta","content":"北京今天晴"}\n\n',
      ]),
    )
    vi.stubGlobal('fetch', fetchMock)

    const reasoning: string[] = []
    const calls: Array<[string, string, string]> = []
    const results: Array<[string, string, string, string]> = []
    await streamChat('1', '北京天气？', {
      onReasoning: (c) => reasoning.push(c),
      onToolCall: (id, name, args) => calls.push([id, name, args]),
      onToolResult: (id, name, content, error) => results.push([id, name, content, error]),
    })

    expect(reasoning.join('')).toBe('先想想再查表')
    expect(calls).toEqual([['call_1', 'get_weather', '{"city":"北京"}']])
    expect(results).toEqual([['call_1', 'get_weather', '晴，25℃', '']])
  })

  it('tool_result 携带 error 时透传失败原因', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      sseResponse([
        'data: {"type":"tool_result","tool_call_id":"call_9","name":"calc","content":"","error":"除数为零"}\n\n',
      ]),
    )
    vi.stubGlobal('fetch', fetchMock)

    let got: [string, string, string, string] | null = null
    await streamChat('1', '1/0', {
      onToolResult: (id, name, content, error) => (got = [id, name, content, error]),
    })
    expect(got).toEqual(['call_9', 'calc', '', '除数为零'])
  })

  it('task_status 事件透传多智能体编排进度', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      sseResponse([
        'data: {"type":"task_status","task_type":"task_started","task_id":"research","status":"running"}\n\n',
        'data: {"type":"task_status","task_type":"task_finished","task_id":"research","status":"completed","total_tokens":123}\n\n',
        'data: {"type":"task_status","task_type":"run_failed","task_id":"","status":"","error":"计划失败"}\n\n',
      ]),
    )
    vi.stubGlobal('fetch', fetchMock)

    const events: Array<{ taskType: string; taskId: string; status: string; error: string; totalTokens: number }> = []
    await streamChat('1', '备课', { onTaskStatus: (e) => events.push(e) })

    expect(events).toEqual([
      { taskType: 'task_started', taskId: 'research', status: 'running', error: '', totalTokens: 0 },
      { taskType: 'task_finished', taskId: 'research', status: 'completed', error: '', totalTokens: 123 },
      { taskType: 'run_failed', taskId: '', status: '', error: '计划失败', totalTokens: 0 },
    ])
  })

  it('event: error 触发 onError（流中错误）', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(
        sseResponse([
          'data: {"type":"delta","content":"部分回复"}\n\n',
          'event: error\ndata: {"message":"上游限流，请稍后重试"}\n\n',
        ]),
      )
    vi.stubGlobal('fetch', fetchMock)

    let err: string | null = null
    await streamChat('1', 'hi', { onError: (m) => (err = m) })
    expect(err).toBe('上游限流，请稍后重试')
  })

  it('非 2xx 响应解析统一错误体并抛出', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ code: 'rate_limited', message: '请求过于频繁', request_id: 'rid-1' }), {
        status: 429,
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(streamChat('1', 'hi', {})).rejects.toThrow('请求过于频繁')
  })

  it('401 时刷新 token 并重试一次（自修复）', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response('', { status: 401, headers: { 'Content-Type': 'application/json' } }),
      )
      .mockResolvedValueOnce(
        sseResponse(['event: done\ndata: {"type":"done","total_tokens":5}\n\n']),
      )
    vi.mocked(refreshAccessToken).mockResolvedValue('new-token')
    vi.stubGlobal('fetch', fetchMock)

    const onDone = vi.fn<(e: SSEDoneEvent) => void>()
    await streamChat('1', 'hi', { onDone })

    expect(onDone).toHaveBeenCalledOnce()
    // 两次请求：首次带旧 token，401 后刷新并携带新 token 重试
    expect(fetchMock).toHaveBeenCalledTimes(2)
    const [first, second] = fetchMock.mock.calls
    const firstAuth = (first[1] as RequestInit).headers
    const secondAuth = (second[1] as RequestInit).headers
    expect(JSON.stringify(firstAuth)).toContain('Bearer test-token')
    expect(JSON.stringify(secondAuth)).toContain('Bearer new-token')
  })

  it('401 且刷新失败时抛错（不重试）', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response('', { status: 401, headers: { 'Content-Type': 'application/json' } }),
      )
    vi.mocked(refreshAccessToken).mockResolvedValue(null)
    vi.stubGlobal('fetch', fetchMock)

    await expect(streamChat('1', 'hi', {})).rejects.toThrow('访问令牌已失效，请重新登录')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('用户主动停止（AbortError）不触发 onError', async () => {
    const controller = new AbortController()
    const fetchMock = vi.fn().mockImplementation((_url: string, init: RequestInit) => {
      const stream = new ReadableStream<Uint8Array>({
        start(c) {
          // 模拟挂起；abort 时让读取以 AbortError 结束
          init.signal?.addEventListener('abort', () => {
            try {
              c.error(new DOMException('The operation was aborted.', 'AbortError'))
            } catch {
              /* 流已关闭 */
            }
          })
        },
        cancel() {
          /* noop */
        },
      })
      return Promise.resolve(new Response(stream, { status: 200 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    let err: string | null = null
    const p = streamChat('1', 'hi', { onError: (m) => (err = m) }, controller.signal)
    setTimeout(() => controller.abort(), 10)
    await p
    expect(err).toBeNull()
  })
})
