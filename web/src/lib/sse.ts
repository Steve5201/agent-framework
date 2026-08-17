// ---------------------------------------------------------------------------
// SSE 客户端（用于流式对话）
//
// 为什么不用 EventSource？
//   EventSource 无法携带自定义 Authorization 头，而我们的 access token 走
//   Bearer 头；因此用 fetch + ReadableStream 手动解析 SSE 帧。
//
// gateway 事件格式（见 backend/internal/gatewaysvc/agent.go）：
//   data: {"type":"delta","content":"..."}   ← 文本增量（默认事件）
//   event: done
//   data: {"type":"done","rounds":1,...}      ← 流结束统计
//   event: error
//   data: {"message":"..."}                   ← 流中错误
//   : keepalive                               ← 心跳注释行（15s 无数据时）
// ---------------------------------------------------------------------------

import { getApiBase, refreshAccessToken } from './api'
import { getAccessToken } from './storage'
import { getGuestId } from './guest'
import { genUuid } from './uuid'
import type { SSEDoneEvent } from '@/types/api'

export interface StreamHandlers {
  /** 每段文本增量 */
  onDelta?: (content: string) => void
  /** 每段思考增量（DeepSeek reasoning_content） */
  onReasoning?: (content: string) => void
  /** 工具调用开始（参数已由后端拼装完整） */
  onToolCall?: (toolCallId: string, name: string, argumentsJson: string) => void
  /** 工具执行返回（error 非空表示失败） */
  onToolResult?: (toolCallId: string, name: string, content: string, error: string) => void
  /** 多智能体编排进度事件（mode=orchestrate）；content 为子任务输出增量（task_content）；
   *  kind 区分增量类型：text（正文）/ reasoning（思考）/ tool_start|tool_end（工具调用） */
  onTaskStatus?: (event: {
    taskType: string
    taskId: string
    status: string
    error: string
    content?: string
    kind?: string
    totalTokens: number
  }) => void
  /** 流结束（含 token 统计） */
  onDone?: (event: SSEDoneEvent) => void
  /** 流中错误 / 连接异常 */
  onError?: (message: string) => void
}

const IDLE_TIMEOUT_MS = 30_000 // 30s 无任何数据视为连接死亡（gateway 15s 心跳兜底）

/** 发起一次 SSE 请求（不校验状态码，交给调用方处理 401 重试等）。
 *  真实用户带 Bearer；游客（无令牌）带 X-Guest-ID。 */
async function doFetch(
  sessionId: string,
  content: string,
  token: string | null,
  guestId: string | null,
  signal?: AbortSignal,
): Promise<Response> {
  return fetch(`${getApiBase()}/v1/agent/sessions/${sessionId}/chat/stream`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(guestId ? { 'X-Guest-ID': guestId } : {}),
      'X-Request-Id': genUuid(),
      Accept: 'text/event-stream',
    },
    body: JSON.stringify({ content }),
    signal,
  })
}

/**
 * 发起一次 SSE 流式对话。
 * - 401（访问令牌过期）时自动刷新 token 并重试一次（自修复，不中断体验）；
 *   刷新失败则抛错，由调用方决定 UI 表现。
 * - 调用方通过 AbortController 主动停止（用户点"停止"）。
 * - 结束后 resolve；出错/停止走 onError 或正常 resolve（由调用方区分）。
 */
export async function streamChat(
  sessionId: string,
  content: string,
  handlers: StreamHandlers,
  signal?: AbortSignal,
): Promise<void> {
  let token = await getAccessToken()
  let guestId: string | null = null
  if (!token) {
    // 阶段2·游客模式：无令牌但有本地游客 ID 时以游客身份流式对话。
    guestId = getGuestId()
    if (!guestId) throw new Error('未登录')
  }

  let resp = await doFetch(sessionId, content, token, guestId, signal)

  // 401 自修复：仅真实用户刷新 access token 后重试一次；游客无刷新通道。
  if (resp.status === 401 && token) {
    const newToken = await refreshAccessToken()
    if (newToken) {
      token = newToken
      resp = await doFetch(sessionId, content, token, guestId, signal)
    } else {
      throw new Error('访问令牌已失效，请重新登录')
    }
  }

  if (!resp.ok) {
    // 非 2xx：读取统一错误体
    let message = `请求失败（HTTP ${resp.status}）`
    try {
      const body = await resp.json()
      if (body?.message) message = body.message
    } catch {
      /* 非 JSON 错误体，用兜底文案 */
    }
    throw new Error(message)
  }
  if (!resp.body) throw new Error('响应无数据流')

  await readSSEStream(resp.body, handlers)
}

/**
 * 发起一次 SSE 流式重新生成（POST /v1/agent/sessions/{id}/messages/{mid}/regenerate-stream）。
 * 事件格式与 streamChat 完全一致（delta/reasoning/tool_call/tool_result/task_status/done/error）；
 * 401 时同样自动刷新 token 重试一次。
 */
export async function regenerateStream(
  sessionId: string,
  messageId: string,
  handlers: StreamHandlers,
  signal?: AbortSignal,
): Promise<void> {
  let token = await getAccessToken()
  let guestId: string | null = null
  if (!token) {
    guestId = getGuestId()
    if (!guestId) throw new Error('未登录')
  }

  const url = `${getApiBase()}/v1/agent/sessions/${sessionId}/messages/${messageId}/regenerate-stream`
  const doRegenerateFetch = (tk: string | null, gid: string | null) =>
    fetch(url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(tk ? { Authorization: `Bearer ${tk}` } : {}),
        ...(gid ? { 'X-Guest-ID': gid } : {}),
        'X-Request-Id': genUuid(),
        Accept: 'text/event-stream',
      },
      signal,
    })

  let resp = await doRegenerateFetch(token, guestId)
  if (resp.status === 401 && token) {
    const newToken = await refreshAccessToken()
    if (newToken) {
      token = newToken
      resp = await doRegenerateFetch(token, guestId)
    } else {
      throw new Error('访问令牌已失效，请重新登录')
    }
  }

  if (!resp.ok) {
    let message = `请求失败（HTTP ${resp.status}）`
    try {
      const body = await resp.json()
      if (body?.message) message = body.message
    } catch {
      /* 非 JSON 错误体，用兜底文案 */
    }
    throw new Error(message)
  }
  if (!resp.body) throw new Error('响应无数据流')

  await readSSEStream(resp.body, handlers)
}

/**
 * 公共 SSE 读流循环：逐块解析事件并分发，带空闲超时检测。
 * 由 streamChat / regenerateStream 共用。
 */
async function readSSEStream(body: ReadableStream<Uint8Array>, handlers: StreamHandlers): Promise<void> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let lastActivity = Date.now()

  // 空闲超时检测：若超过 IDLE_TIMEOUT_MS 没有任何字节到达，判定连接死亡。
  const idleTimer = setInterval(() => {
    if (Date.now() - lastActivity > IDLE_TIMEOUT_MS) {
      clearInterval(idleTimer)
      void reader.cancel()
      handlers.onError?.('连接超时，请重试')
    }
  }, 5000)

  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      lastActivity = Date.now()
      buffer += decoder.decode(value, { stream: true })

      // SSE 事件以空行分隔（兼容 \n\n 与 \r\n\r\n）
      const parts = buffer.split(/\r?\n\r?\n/)
      buffer = parts.pop() ?? ''
      for (const part of parts) {
        dispatchEvent(part, handlers)
      }
    }
  } catch (err) {
    if ((err as Error).name === 'AbortError') return // 用户主动停止，不算错误
    handlers.onError?.((err as Error).message || '流式连接中断')
  } finally {
    clearInterval(idleTimer)
  }
}

/** 解析并分发单个 SSE 事件块。 */
function dispatchEvent(block: string, handlers: StreamHandlers): void {
  let eventName = ''
  const dataLines: string[] = []

  for (const raw of block.split(/\r?\n/)) {
    const line = raw.trimEnd()
    if (line.startsWith(':')) continue // 注释行（心跳 keepalive）
    if (line.startsWith('event:')) {
      eventName = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  if (dataLines.length === 0) return

  const data = dataLines.join('\n')
  if (eventName === 'error') {
    try {
      const parsed = JSON.parse(data) as { message?: string }
      handlers.onError?.(parsed.message || '流式对话出错')
    } catch {
      handlers.onError?.(data)
    }
    return
  }
  if (eventName === 'done') {
    try {
      handlers.onDone?.(JSON.parse(data) as SSEDoneEvent)
    } catch {
      handlers.onDone?.({ type: 'done', rounds: 0, tool_calls: 0, prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 })
    }
    return
  }
  // 默认事件：delta / reasoning / tool_call / tool_result / task_status
  try {
    const parsed = JSON.parse(data) as {
      type?: string
      content?: string
      name?: string
      arguments?: string
      error?: string
      tool_call_id?: string
      task_type?: string
      task_id?: string
      status?: string
      kind?: string
      total_tokens?: number
    }
    if (parsed.type === 'delta' && typeof parsed.content === 'string') {
      handlers.onDelta?.(parsed.content)
    } else if (parsed.type === 'reasoning' && typeof parsed.content === 'string') {
      handlers.onReasoning?.(parsed.content)
    } else if (parsed.type === 'tool_call' && typeof parsed.name === 'string') {
      handlers.onToolCall?.(parsed.tool_call_id ?? '', parsed.name, parsed.arguments ?? '')
    } else if (parsed.type === 'tool_result' && typeof parsed.name === 'string') {
      handlers.onToolResult?.(parsed.tool_call_id ?? '', parsed.name, parsed.content ?? '', parsed.error ?? '')
    } else if (parsed.type === 'task_status') {
      handlers.onTaskStatus?.({
        taskType: parsed.task_type ?? '',
        taskId: parsed.task_id ?? '',
        status: parsed.status ?? '',
        error: parsed.error ?? '',
        content: parsed.content,
        kind: parsed.kind,
        totalTokens: parsed.total_tokens ?? 0,
      })
    }
  } catch {
    // 非 JSON 数据忽略（如调试输出）
  }
}
