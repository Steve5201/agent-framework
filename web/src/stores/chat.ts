import { create } from 'zustand'
import type { ChatMessage, ChatDocUploadResult, HistoryMessage, OrchestrationTask, Session, SessionConfig, ThinkingSegment } from '@/types/api'
import {
  createBranch as apiCreateBranch,
  createSession as apiCreateSession,
  deleteMessage as apiDeleteMessage,
  deleteSession as apiDeleteSession,
  fetchMessages,
  listSessions,
  renameSession as apiRenameSession,
  setActiveVersion as apiSetActiveVersion,
  updateSessionConfig as apiUpdateSessionConfig,
  submitToolResult,
  uploadChatDocument as apiUploadChatDocument,
} from '@/lib/api'
import { streamChat, regenerateStream } from '@/lib/sse'
import { LOCAL_TOOL_NAMES, isTauri, runLocalShell } from '@/lib/localTools'
import { ALL_AGENT_ID, DEFAULT_AGENT_ID, getHomeScope } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth'
import { genUuid } from '@/lib/uuid'

const uid = () => genUuid()

/** 解析后端 tool_calls JSON（schema.ToolCall 序列化：arguments 为内嵌原始 JSON）。
 *  返回 {id,name,arguments}，arguments 统一转字符串。 */
interface ParsedCall {
  id: string
  name: string
  arguments: string
}
function parseToolCalls(raw: string): ParsedCall[] {
  try {
    const parsed = JSON.parse(raw) as { id?: string; name?: string; arguments?: unknown }[]
    return parsed
      .filter((c) => c.name)
      .map((c) => ({
        id: c.id ?? '',
        name: c.name!,
        arguments: typeof c.arguments === 'string' ? c.arguments : JSON.stringify(c.arguments ?? {}),
      }))
  } catch {
    return [] // 解析失败仅丢失工具过程展示
  }
}

/** 编排过程摘要消息的前缀（后端 orchestrate.go orchestrationSummaryMarker）。 */
const ORCH_SUMMARY_MARKER = '__orch_v1__'

/** 上下文压缩记录消息的前缀（后端 agentsvc/condense.go condensationMarker）。
 *  一次压缩落一条 system 消息，前端历史回看时渲染提示条。 */
const CONDENSE_MARKER = '__condense_v1__'

/** 一次上下文压缩的展示信息（后端 CondenseInfo 序列化）。 */
export interface CondensedInfo {
  /** 本次压缩收纳进摘要的消息条数 */
  dropped: number
  /** 会话累计压缩次数 */
  count: number
}

/**
 * 解析上下文压缩记录（system 消息）→ 展示信息。
 * 非压缩记录（无前缀 / JSON 解析失败 / 缺字段）返回 null。
 */
function parseCondenseSummary(content: string): CondensedInfo | null {
  if (!content.startsWith(CONDENSE_MARKER)) return null
  try {
    const parsed = JSON.parse(content.slice(CONDENSE_MARKER.length)) as {
      dropped?: unknown
      count?: unknown
    }
    if (typeof parsed.dropped !== 'number' || typeof parsed.count !== 'number') return null
    return { dropped: parsed.dropped, count: parsed.count }
  } catch {
    return null // 记录损坏时仅丢失提示条，不影响正文
  }
}

/** 合并同一轮的多条压缩记录（dropped 累计、count 取最新）。 */
function mergeCondense(prev: CondensedInfo | undefined, next: CondensedInfo): CondensedInfo {
  if (!prev) return next
  return { dropped: prev.dropped + next.dropped, count: next.count }
}

/** 编排摘要 JSON 中的任务字段（后端 buildOrchestrationSummary 输出）。 */
interface OrchSummaryTask {
  id: string
  role?: string
  status: string
  error?: string
  /** 子任务输出（后端截断版，前端历史渲染回看，不进入模型上下文） */
  output?: string
  tokens?: number
  duration_ms?: number
}

/**
 * 解析编排过程摘要（system 消息）→ 前端编排任务节点（历史渲染用）。
 * 非编排摘要（无前缀 / JSON 解析失败）返回 null。
 */
function parseOrchSummary(content: string): OrchestrationTask[] | null {
  if (!content.startsWith(ORCH_SUMMARY_MARKER)) return null
  try {
    const parsed = JSON.parse(content.slice(ORCH_SUMMARY_MARKER.length)) as {
      v?: number
      tasks?: OrchSummaryTask[]
    }
    if (!parsed?.tasks?.length) return null
    return parsed.tasks.map((t) => ({
      id: t.id,
      status: (['completed', 'failed', 'skipped', 'running'].includes(t.status)
        ? t.status
        : 'skipped') as OrchestrationTask['status'],
      ...(t.error ? { error: t.error } : {}),
      ...(t.output ? { content: t.output } : {}),
      ...(t.tokens ? { totalTokens: t.tokens } : {}),
    }))
  } catch {
    return null // 摘要损坏时仅丢失过程展示，不影响正文
  }
}

/**
 * 编排进度事件合入任务列表（P4-M 子任务输出实时渲染；P4-N 思考/工具分流）。
 * task_content 增量按 kind 分流累积，不改节点终态（节点保持 running）：
 *   - text      → content（正文打字机）
 *   - reasoning → reasoning（思考中）
 *   - tool_start→ activeTool（当前工具）；tool_end → 追加 toolHistory 并清空 activeTool
 * 其余事件（task_started/task_finished）更新节点状态/错误/token。
 * 返回新数组（不可变更新，供 React 状态使用）。
 */
function mergeTaskStatus(
  tasks: OrchestrationTask[] | undefined,
  ev: { taskId: string; status?: string; error?: string; totalTokens?: number; content?: string; kind?: string },
): OrchestrationTask[] {
  const base = tasks ?? []
  const idx = base.findIndex((t) => t.id === ev.taskId)
  const patch: Partial<OrchestrationTask> = {}
  // 按 kind 分流增量（默认 text 保持旧行为，兼容旧后端/测试）
  if (ev.content) {
    switch (ev.kind) {
      case 'reasoning':
        patch.reasoning = (base[idx]?.reasoning ?? '') + ev.content
        break
      case 'tool_start':
        patch.activeTool = ev.content
        break
      case 'tool_end': {
        patch.activeTool = undefined
        const hist = base[idx]?.toolHistory ?? []
        if (!hist.includes(ev.content)) patch.toolHistory = [...hist, ev.content]
        break
      }
      default:
        patch.content = (base[idx]?.content ?? '') + ev.content
    }
  }
  if (idx < 0) {
    // 增量先于 started 到达（理论罕见）：兜底建 running 节点并带初始增量。
    return [
      ...base,
      {
        id: ev.taskId,
        status: (ev.status as OrchestrationTask['status']) || 'running',
        ...(ev.error ? { error: ev.error } : {}),
        ...patch,
        ...(ev.totalTokens ? { totalTokens: ev.totalTokens } : {}),
      },
    ]
  }
  const cur = base[idx]
  const next: OrchestrationTask = {
    ...cur,
    ...patch,
    status: (ev.status as OrchestrationTask['status']) || cur.status,
    ...(ev.error ? { error: ev.error } : {}),
    ...(ev.totalTokens ? { totalTokens: ev.totalTokens } : {}),
  }
  return [...base.slice(0, idx), next, ...base.slice(idx + 1)]
}

/**
 * 历史消息 → 前端消息模型。
 *
 * 工具过程合并：一轮对话在后端是 4 条消息（user → assistant(带tool_calls)
 * → tool → assistant 最终回答），渲染层希望它是"一个思考过程 + 一个回答"：
 * 中间 assistant 的思考内容 + 工具调用指令，与 tool 消息的工具返回，
 * 按顺序合并进最终 assistant 消息的 thinking 分段（与流式事件到达顺序一致）。
 *
 * 编排轮（mode=orchestrate）：后端额外存一条 system 摘要消息（__orch_v1__ 前缀，
 * 承载子任务终态/错误/token），历史回放时解析为编排任务块，附到同轮最终
 * assistant 消息上渲染（与流式 OrchestrationBlock 视觉一致）。
 *
 * 上下文压缩轮：后端在该轮末尾落一条 system 记录（__condense_v1__ 前缀），
 * 历史回放时解析为压缩提示条（收纳条数/累计次数），附到该轮 assistant 消息上。
 */
export function fromHistory(msgs: HistoryMessage[]): ChatMessage[] {
  const out: ChatMessage[] = []
  let pending: ThinkingSegment[] = []
  let toolNames: string[] = []
  let callIdToName = new Map<string, string>()
  let orchTasks: OrchestrationTask[] | undefined

  const flush = () => {
    if (pending.length > 0 || toolNames.length > 0) {
      // 兜底：历史以中间消息结尾（异常数据），不丢过程信息
      out.push({
        id: uid(),
        role: 'assistant',
        content: '',
        status: 'done',
        thinking: pending.length ? pending : undefined,
        toolNames: toolNames.length ? toolNames : undefined,
      })
      pending = []
      toolNames = []
    }
  }

  for (const msg of msgs) {
    if (msg.role === 'system') {
      // 上下文压缩记录：解析为展示信息，附到上一轮 assistant 消息上
      // （后端 persistRound 把记录追加在该轮末尾，迭代时该轮回答已出队）。
      const cond = parseCondenseSummary(msg.content)
      if (cond) {
        for (let i = out.length - 1; i >= 0; i--) {
          if (out[i].role === 'assistant') {
            out[i] = { ...out[i], condensed: mergeCondense(out[i].condensed, cond) }
            break
          }
        }
        continue
      }
      // 编排过程摘要：解析为任务块，待同轮 assistant 消息承载渲染。
      const tasks = parseOrchSummary(msg.content)
      if (tasks) {
        orchTasks = tasks
        // 编排轮无思考/工具过程，清空待合并段避免串扰。
        pending = []
        toolNames = []
        callIdToName = new Map()
      }
      continue
    }
    if (msg.role === 'user') {
      flush()
      out.push({
        id: msg.id || uid(),
        serverId: msg.id || undefined,
        role: 'user',
        content: msg.content || '',
        status: 'done',
        roundNo: msg.round_no,
        version: msg.version,
        totalVersions: msg.total_versions,
      })
      continue
    }
    if (msg.role === 'tool') {
      // 工具返回：按工具名分组展示（名字来自前面 assistant 的 tool_calls 映射）
      pending.push({
        kind: 'tool-result',
        name: callIdToName.get(msg.tool_call_id) || '工具',
        content: msg.content || '',
      })
      continue
    }
    // assistant
    const calls = parseToolCalls(msg.tool_calls)
    const names = calls.map((c) => c.name)
    if (names.length > 0) {
      // 中间 assistant（决定调用工具）：思考 + 工具调用分段
      if (msg.reasoning) pending.push({ kind: 'text', content: msg.reasoning })
      for (const c of calls) {
        if (c.id) callIdToName.set(c.id, c.name)
        pending.push({ kind: 'tool-call', name: c.name, arguments: c.arguments })
      }
      toolNames.push(...names)
      continue
    }
    // 最终 assistant 回答：合并本轮的思考过程
    const thinking: ThinkingSegment[] = [...pending]
    if (msg.reasoning) thinking.push({ kind: 'text', content: msg.reasoning })
    const content = msg.content || ''
    // 空气泡过滤：正文、思考、工具过程与编排任务全部为空才跳过（防御性清理脏数据）。
    if (content === '' && thinking.length === 0 && toolNames.length === 0 && !orchTasks?.length) {
      pending = []
      toolNames = []
      callIdToName = new Map()
      orchTasks = undefined
      continue
    }
    out.push({
      id: msg.id || uid(),
      serverId: msg.id || undefined,
      role: 'assistant',
      content,
      status: 'done',
      thinking: thinking.length ? thinking : undefined,
      toolNames: toolNames.length ? toolNames : undefined,
      tasks: orchTasks?.length ? orchTasks : undefined,
      roundNo: msg.round_no,
      version: msg.version,
      totalVersions: msg.total_versions,
    })
    pending = []
    toolNames = []
    callIdToName = new Map()
    orchTasks = undefined
  }
  flush()
  return out
}

/** 一条等待用户决定（允许/拒绝）的本地工具调用（阶段3·桌面确认弹窗）。 */
export interface LocalPendingCall {
  sessionId: string
  toolCallId: string
  name: string
  command: string
  cwd?: string
}

/** 处理一条本地工具（External=true）调用：
 *   - 浏览器：无本地执行能力，立即回填失败结果，agent 据此给出降级答复；
 *   - 桌面端：弹出确认弹窗（pendingLocalCall），用户决定后执行并回填。
 *  导出供单测直接验证两条分支。 */
export async function handleLocalToolCall(
  sessionId: string,
  toolCallId: string,
  name: string,
  argsJson: string,
): Promise<void> {
  let command = ''
  let cwd: string | undefined
  try {
    const parsed = JSON.parse(argsJson) as { command?: unknown; cwd?: unknown }
    if (typeof parsed.command === 'string') command = parsed.command
    if (typeof parsed.cwd === 'string') cwd = parsed.cwd
  } catch {
    // 参数解析失败：command 保持空串，按"无法执行"处理
  }

  if (!isTauri()) {
    // 浏览器降级：立即回填失败，避免 agent 挂起等待 120s
    void submitToolResult(
      sessionId,
      toolCallId,
      `浏览器环境无法执行本地工具 ${name}，请使用桌面客户端（Tauri 版）`,
      true,
    )
    return
  }
  useChatStore.setState({ pendingLocalCall: { sessionId, toolCallId, name, command, cwd } })
}

interface ChatState {
  sessions: Session[]
  sessionsTotal: number
  sessionsLoading: boolean
  activeId: string | null
  messages: ChatMessage[]
  /** 当前会话作用域：'' = 管理端域；'<id>' = 对应智能体域（阶段2·独立地址）。 */
  agentId: string
  /** 当前活动会话是否有流式对话/重新生成进行中（用于发送/停止按钮状态） */
  sending: boolean
  /** 正在流式生成（sendMessage/regenerateMessage）的会话 id 列表：
   *  切换会话不再打断生成，后台流继续跑直到完整落库，sending 只反映
   *  当前活动会话。 */
  busySessions: string[]
  aborter: AbortController | null
  /** 正在重新生成的某条消息 ID（按钮 loading 态） */
  regeneratingId: string | null
  /** 移动端侧栏开关 */
  sidebarOpen: boolean
  /** 待用户决定的本地工具调用（桌面确认弹窗；null = 无） */
  pendingLocalCall: LocalPendingCall | null

  /** 切换到指定智能体域：作用域变化时清空活动会话并重新拉取列表。 */
  initAgent: (agentId: string) => void
  /** 登录态变化时强制重拉当前域会话（游客会话 ≠ 登录用户会话） */
  resetScope: (agentId: string) => void
  loadSessions: () => Promise<void>
  selectSession: (id: string) => Promise<void>
  createSession: () => Promise<string>
  deleteSession: (id: string) => Promise<void>
  /** 删除一轮完整对话；删空后会话自动消失。 */
  deleteMessage: (id: string) => Promise<void>
  /** 复制消息纯文本内容到剪贴板。 */
  copyMessage: (id: string) => Promise<void>
  /** 重新生成该轮回答（旧版本保留，可切换）。 */
  regenerateMessage: (id: string) => Promise<void>
  /** 切换该轮活跃版本。 */
  switchVersion: (id: string, version: number) => Promise<void>
  /** 基于该轮创建分支会话并跳转。 */
  branchMessage: (id: string) => Promise<void>
  /** 重命名会话。 */
  renameSession: (id: string, title: string) => Promise<void>
  /** 更新会话配置（工具权限 / 思考模式）。 */
  updateConfig: (id: string, config: SessionConfig) => Promise<void>
  /**
   * 上传文档供智能体理解（模块二）：无活动会话时先创建；上传成功后重拉历史，
   * 让后端注入的 [文档] 消息出现在对话流中（可继续追问）。
   */
  uploadDocument: (file: File) => Promise<ChatDocUploadResult>
  sendMessage: (content: string) => Promise<void>
  stopStreaming: () => void
  setSidebarOpen: (open: boolean) => void
  /** 用户对本地工具调用做出决定：允许则本地执行并回填，拒绝则回填失败结果。 */
  resolveLocalCall: (allow: boolean) => Promise<void>
}

export const useChatStore = create<ChatState>()((set, get) => ({
  sessions: [],
  sessionsTotal: 0,
  sessionsLoading: false,
  activeId: null,
  messages: [],
  /** 当前会话作用域：'' = 管理端域；'<id>' = 对应智能体域 */
  agentId: '',
  sending: false,
  busySessions: [],
  aborter: null,
  regeneratingId: null,
  sidebarOpen: false,
  pendingLocalCall: null,

  initAgent: (agentId) => {
    // 挂载/重挂载（桌面端重开、路由重进）时总是拉取该域会话列表。
    // 同域重复挂载不再短路跳过：桌面端 webview 重新加载后 chat store 是全新
    // 内存态，短路会留下空列表（曾致"重开应用后列表不自动刷新"）。
    // 域切换/重挂载会清空整个会话域，后台跨域流已无意义：中止后再切换。
    if (get().busySessions.length > 0 || get().regeneratingId) get().stopStreaming()
    set({ agentId, activeId: null, messages: [], sending: false })
    void get().loadSessions()
  },

  resetScope: (agentId) => {
    // 登录/登出切换后强制重拉：游客会话与登录用户会话不同，
    // 且登出后必须清空旧会话列表（避免"退出后列表还在"）。
    if (get().busySessions.length > 0 || get().regeneratingId) get().stopStreaming()
    set({ agentId, activeId: null, messages: [], sessions: [], sessionsTotal: 0, sending: false })
    void get().loadSessions()
  },

  loadSessions: async () => {
    set({ sessionsLoading: true })
    try {
      const data = await listSessions(1, 50, get().agentId)
      set({ sessions: data.sessions, sessionsTotal: data.total })
    } catch {
      // 列表失败不阻塞聊天；保持旧列表
    } finally {
      set({ sessionsLoading: false })
    }
  },

  selectSession: async (id) => {
    // 切换会话不再中止正在生成的流：后台继续跑直到完整落库（切回该会话时
    // 拉到的历史即完整回答）。sending 重算——新会话若恰好也在后台生成则
    // 保持 true，否则为 false（后台旧流完成时由 finally 再次重算）。
    set({
      activeId: id,
      sidebarOpen: false,
      messages: [],
      sending: get().busySessions.includes(id),
    })
    // 并行刷新会话列表：其它端（如桌面/浏览器）新建的会话这里也能看到
    void get().loadSessions()
    try {
      const history = await fetchMessages(id)
      const msgs = fromHistory(history)
      set({ messages: msgs })
    } catch {
      set({ messages: [] }) // 历史拉取失败则从空开始
    }
  },

  createSession: async () => {
    // 超管全门户聊天域（'*'）只是"跨域展示/切换"的聊天域，不是可归属的
    // 会话域：创建会话必须具体到某个智能体域，否则后端 validateAgentID
    // 会拒绝（曾导致超管在 '/agent/*' 新建会话按钮无反应）。这里回退
    // 默认域；列表查询 '*' = 全部域（ListSessions 语义）不受影响。
    // 管理端域（''）同理：无归属的孤儿域，按角色归属域回退（超管 → * →
    // 默认域、agent_admin/admin → 绑定域），不再产生 agent_id='' 的会话。
    const raw = get().agentId
    let agentId = raw === ALL_AGENT_ID ? DEFAULT_AGENT_ID : raw
    if (!agentId) {
      agentId = getHomeScope(useAuthStore.getState().user)
      if (agentId === ALL_AGENT_ID) agentId = DEFAULT_AGENT_ID
    }
    const session = await apiCreateSession(undefined, agentId)
    set((s) => ({
      sessions: [session, ...s.sessions],
      sessionsTotal: s.sessionsTotal + 1,
      activeId: session.id,
      messages: [],
      sidebarOpen: false,
    }))
    return session.id
  },

  deleteSession: async (id) => {
    await apiDeleteSession(id)
    set((s) => {
      const sessions = s.sessions.filter((x) => x.id !== id)
      const activeId = s.activeId === id ? null : s.activeId
      const messages = s.activeId === id ? [] : s.messages
      return { sessions, sessionsTotal: Math.max(0, s.sessionsTotal - 1), activeId, messages }
    })
  },

  /** 删除一轮完整对话（该轮 user + assistant + 工具对全删）。
   *  删除后重拉历史，保证后端连带删除的工具结果同步消失；
   *  若会话已空（后端自动软删），从列表移除并清空活动会话。 */
  deleteMessage: async (id) => {
    const sessionId = get().activeId
    const msg = get().messages.find((m) => m.id === id)
    if (!sessionId || !msg?.serverId) return
    await apiDeleteMessage(sessionId, msg.serverId)
    // 删除后拉取最新历史，保证连带删除的工具结果同步消失。
    // 注意：删空最后一轮时后端会自动软删会话，此处 fetch 会 404——
    // 这是"会话已空"的正常信号，直接按空会话处理，不要当作错误抛出。
    const history = await fetchMessages(sessionId).catch(() => null)
    const msgs = history ? fromHistory(history) : []
    set({ messages: msgs })
    if (msgs.length === 0) {
      // 会话被删空 → 后端已自动软删，前端同步移除
      set((s) => ({
        sessions: s.sessions.filter((x) => x.id !== sessionId),
        sessionsTotal: Math.max(0, s.sessionsTotal - 1),
        activeId: null,
        messages: [],
      }))
    }
  },

  copyMessage: async (id) => {
    const msg = get().messages.find((m) => m.id === id)
    if (!msg || !msg.content) return
    await navigator.clipboard.writeText(msg.content)
  },

  /**
   * 重新生成该轮回答（SSE 流式）：后端隐藏旧版本并流式产出新版本。
   * 本地先把目标消息切换为"流式占位"，逐事件渲染正文/思考/工具/编排进度；
   * 收到 done（后端已按版本语义落库）后重拉历史回填 serverId/version。
   * 失败/中断时后端已回滚截断，同样重拉历史恢复原回答。
   */
  regenerateMessage: async (id) => {
    const sessionId = get().activeId
    const msg = get().messages.find((m) => m.id === id)
    if (!sessionId || !msg?.serverId || get().regeneratingId) return
    // 该会话后台流仍在跑（切换会话后生成未打断）：同会话后端串行，禁止重入
    if (get().busySessions.includes(sessionId)) return
    set({ regeneratingId: id, sending: true, busySessions: [...get().busySessions, sessionId] })
    // 快照原回答：重新生成被打断时后端回滚（旧版本 隐藏→恢复）是异步的，
    // 若重拉历史仍看不到原回答，用快照兜底还原，保证该轮回答不从界面消失。
    const original = msg

    // 编排模式预置 tasks 容器（否则渲染思考过程折叠块）。
    const session = get().sessions.find((s) => s.id === sessionId)
    const orchestrate = session?.config?.mode === 'orchestrate'
    set({
      messages: get().messages.map((m) =>
        m.id === id
          ? { ...m, content: '', thinking: [], ...(orchestrate ? { tasks: [] } : {}), status: 'streaming' }
          : m,
      ),
    })

    const patchBot = (patch: Partial<ChatMessage>) => {
      set({ messages: get().messages.map((m) => (m.id === id ? { ...m, ...patch } : m)) })
    }
    const appendReasoning = (delta: string) => {
      set({
        messages: get().messages.map((m) => {
          if (m.id !== id) return m
          const t = m.thinking ?? []
          const last = t[t.length - 1]
          if (last?.kind === 'text') {
            return { ...m, thinking: [...t.slice(0, -1), { kind: 'text', content: last.content + delta }] }
          }
          return { ...m, thinking: [...t, { kind: 'text', content: delta }] }
        }),
      })
    }
    const appendThinking = (seg: ThinkingSegment) => {
      set({
        messages: get().messages.map((m) =>
          m.id === id ? { ...m, thinking: [...(m.thinking ?? []), seg] } : m,
        ),
      })
    }

    const aborter = new AbortController()
    set({ aborter })
    let completed = false // 收到 done（新版本已落库）才算正常结束
    let errored = false // 流中错误（后端已回滚）
    try {
      await regenerateStream(sessionId, msg.serverId, {
        onDelta: (delta) =>
          patchBot({ content: (get().messages.find((m) => m.id === id)?.content ?? '') + delta }),
        onReasoning: (delta) => appendReasoning(delta),
        onToolCall: (toolCallId, name, args) => {
          appendThinking({ kind: 'tool-call', name, arguments: args })
          // 本地工具（External=true）：进入确认/降级流程并回填结果
          if (LOCAL_TOOL_NAMES.has(name)) {
            void handleLocalToolCall(sessionId, toolCallId, name, args)
          }
        },
        onToolResult: (_toolCallId, name, content, error) =>
          appendThinking({ kind: 'tool-result', name, content, error: Boolean(error) }),
        onTaskStatus: (ev) => {
          if (!ev.taskId) return
          set({
            messages: get().messages.map((m) =>
              m.id !== id ? m : { ...m, tasks: mergeTaskStatus(m.tasks, ev) },
            ),
          })
        },
        onDone: () => {
          completed = true
          patchBot({ status: 'done' })
        },
        onError: (message) => {
          // 后端已回滚旧版本；正文全空时用后端提示占位，随后统一重拉恢复。
          errored = true
          const cur = get().messages.find((m) => m.id === id)
          patchBot({
            status: 'done',
            ...(cur?.content || (cur?.thinking?.length ?? 0) > 0
              ? {}
              : { content: message || '重新生成失败，已恢复原回答。' }),
          })
        },
      }, aborter.signal)

      // 结束后重拉历史：成功时回填新版本（serverId/roundNo/version/total_versions），
      // 失败/中断时后端已回滚，重拉恢复原回答。
      // 若期间用户切走了会话，不再用旧会话历史覆盖当前视图。
      if (get().activeId === sessionId) {
        // 用户主动停止：回滚（旧版本 隐藏→恢复）是异步的，直接拉可能拉到
        // 中间态——该轮回答凭空消失。等待 + 按原回答兜底重试，最多 3 次。
        const stopped = !completed && !errored
        const match = (list: ChatMessage[]) =>
          original.content !== '' &&
          list.some((m) => m.role === 'assistant' && m.content && m.content === original.content)
        let msgs: ChatMessage[] | null = null
        for (let i = 0; i < (stopped ? 3 : 1); i++) {
          if (i > 0) await new Promise((r) => setTimeout(r, 350))
          const history = await fetchMessages(sessionId).catch(() => null)
          if (!history) break
          msgs = fromHistory(history)
          if (!stopped || match(msgs)) break
        }
        if (msgs && (!stopped || match(msgs))) {
          set({ messages: msgs })
        } else if (stopped) {
          // 重拉始终没看到原回答（极端时序/回滚未完成）：用快照还原，避免回答消失。
          set({ messages: get().messages.map((m) => (m.id === id ? original : m)) })
        }
      }
    } catch (err) {
      // 网络/鉴权等彻底失败：恢复原回答并提示。切走会话后不再打扰当前视图。
      if (get().activeId === sessionId) {
        const history = await fetchMessages(sessionId).catch(() => null)
        if (history) set({ messages: fromHistory(history) })
        alert(`重新生成失败：${(err as Error).message}`)
      }
    } finally {
      const busy = get().busySessions.filter((x) => x !== sessionId)
      set({
        regeneratingId: null,
        busySessions: busy,
        sending: busy.includes(get().activeId ?? ''),
        ...(get().aborter === aborter ? { aborter: null } : {}),
      })
      void get().loadSessions()
    }
  },

  /** 切换某轮活跃版本：切换后该轮显示指定版本，后续轮次按分支恢复。 */
  switchVersion: async (id, version) => {
    const sessionId = get().activeId
    const msg = get().messages.find((m) => m.id === id)
    if (!sessionId || !msg?.serverId || msg.version === version) return
    await apiSetActiveVersion(sessionId, msg.serverId, version)
    const history = await fetchMessages(sessionId)
    const msgs = fromHistory(history)
    set({ messages: msgs })
  },

  /** 基于该轮之前的上下文创建分支会话，并跳转到新分支。 */
  branchMessage: async (id) => {
    const sessionId = get().activeId
    const msg = get().messages.find((m) => m.id === id)
    if (!sessionId || !msg?.serverId) return
    const session = await apiCreateBranch(sessionId, msg.serverId)
    set((s) => ({
      sessions: [session, ...s.sessions],
      sessionsTotal: s.sessionsTotal + 1,
      activeId: session.id,
      messages: [],
      sidebarOpen: false,
    }))
    const history = await fetchMessages(session.id)
    const msgs = fromHistory(history)
    set({ messages: msgs })
    void get().loadSessions()
  },

  /** 重命名会话（更新侧栏标题）。 */
  renameSession: async (id, title) => {
    const trimmed = title.trim()
    if (!trimmed) return
    const updated = await apiRenameSession(id, trimmed)
    set((s) => ({ sessions: s.sessions.map((x) => (x.id === id ? updated : x)) }))
  },

  /** 更新会话配置（工具权限 / 思考模式；成功后刷新会话列表）。 */
  updateConfig: async (id, config) => {
    const updated = await apiUpdateSessionConfig(id, config)
    set((s) => ({ sessions: s.sessions.map((x) => (x.id === id ? updated : x)) }))
  },

  uploadDocument: async (file) => {
    let sessionId = get().activeId
    if (!sessionId) {
      sessionId = await get().createSession()
    }
    const result = await apiUploadChatDocument(sessionId, file)
    // 上传成功：重拉历史，让后端注入的 [文档] 消息（含工作区路径溯源）出现。
    const history = await fetchMessages(sessionId)
    set({ messages: fromHistory(history) })
    void get().loadSessions()
    return result
  },

  sendMessage: async (content) => {
    // 当前活动会话在生成（普通流或重新生成）中禁止再次发送（后端同会话串行）。
    // 其它会话的后台流不阻塞本会话发送。
    if (get().busySessions.includes(get().activeId ?? '') || get().regeneratingId) return
    // 允许空 content：纯文件上传场景（用户只传文件不输入文字）也触发一轮回复
    // （需求 6）。后端会把空消息规范化为占位提示，基于注入的 [文档]/[图片]
    // 内容回复。userMsg 气泡里空文本不渲染（附件卡片已展示文件）。
    const text = content.trim()

    // 确保存在活动会话
    let sessionId = get().activeId
    if (!sessionId) {
      sessionId = await get().createSession()
    }

    const userMsg: ChatMessage = { id: uid(), role: 'user', content: text, status: 'done' }
    const botMsg: ChatMessage = {
      id: uid(),
      role: 'assistant',
      content: '',
      status: 'streaming',
      // 思考过程分段：流式中保持空数组，reasoning/tool 事件逐步填充
      thinking: [],
      // 编排子任务进度：仅编排模式预置空数组（否则渲染思考过程折叠块）
      ...(get().sessions.find((s) => s.id === sessionId)?.config?.mode === 'orchestrate' ? { tasks: [] } : {}),
    }
    set({
      messages: [...get().messages, userMsg, botMsg],
      sending: sessionId === get().activeId,
      busySessions: [...get().busySessions, sessionId],
    })

    const aborter = new AbortController()
    set({ aborter })

    const patchBot = (patch: Partial<ChatMessage>) => {
      set({ messages: get().messages.map((m) => (m.id === botMsg.id ? { ...m, ...patch } : m)) })
    }

    // 思考增量：追加到当前 text 分段（或新建分段），与"想→做→想"顺序一致
    const appendReasoning = (delta: string) => {
      set({
        messages: get().messages.map((m) => {
          if (m.id !== botMsg.id) return m
          const t = m.thinking ?? []
          const last = t[t.length - 1]
          if (last?.kind === 'text') {
            return { ...m, thinking: [...t.slice(0, -1), { kind: 'text', content: last.content + delta }] }
          }
          return { ...m, thinking: [...t, { kind: 'text', content: delta }] }
        }),
      })
    }
    const appendThinking = (seg: ThinkingSegment) => {
      set({
        messages: get().messages.map((m) =>
          m.id === botMsg.id ? { ...m, thinking: [...(m.thinking ?? []), seg] } : m,
        ),
      })
    }

    let completed = false // 收到后端 done 事件才算真正结束（后端已落库）
    let errored = false // 流中错误（后端发 error 事件前已把部分内容落库）
    try {
      await streamChat(
        sessionId,
        text,
        {
          onDelta: (delta) => patchBot({ content: (get().messages.find((m) => m.id === botMsg.id)?.content ?? '') + delta }),
          onReasoning: (delta) => appendReasoning(delta),
          onToolCall: (toolCallId, name, args) => {
            appendThinking({ kind: 'tool-call', name, arguments: args })
            // 本地工具（External=true）：进入确认/降级流程并回填结果
            if (LOCAL_TOOL_NAMES.has(name)) {
              void handleLocalToolCall(sessionId, toolCallId, name, args)
            }
          },
          onToolResult: (_toolCallId, name, content, error) =>
            appendThinking({ kind: 'tool-result', name, content, error: Boolean(error) }),
          onTaskStatus: (ev) => {
            // run_completed/run_failed 无子任务 ID，终态由 done 事件处理。
            if (!ev.taskId) return
            set({
              messages: get().messages.map((m) =>
                m.id !== botMsg.id ? m : { ...m, tasks: mergeTaskStatus(m.tasks, ev) },
              ),
            })
          },
          onDone: () => {
            completed = true
            patchBot({ status: 'done' })
          },
          // 流中错误：已收到部分正文则保留并标记完成（不覆盖已显示内容）；
          // 若正文全空（模型空回复/工具轮无总结等），用后端给的上下文化提示
          // 作占位文案，避免渲染"(空)"空气泡。本轮未落库，占位仅本地显示。
          onError: (message) => {
            errored = true
            const cur = get().messages.find((m) => m.id === botMsg.id)
            patchBot({
              status: 'done',
              content: cur?.content || message || '模型未生成内容（空回复），本轮未保存，请重试。',
            })
          },
        },
        aborter.signal,
      )

      // 正常完成（后端已落库）→ 重拉历史回填 serverId/roundNo/version，
      // 让新对话气泡同样具备删除/重生成/分支能力，并同步自动命名标题。
      // 生成期间若已切走：后台完成只刷新列表，不覆盖当前视图（切回时
      // selectSession 拉到的历史即完整回答）。
      if (completed) {
        if (get().activeId === sessionId) {
          const history = await fetchMessages(sessionId)
          set({ messages: fromHistory(history) })
        } else {
          void get().loadSessions()
        }
      } else {
        // 用户主动停止 / 流中错误：后端已把部分内容落库（persistPartialOnError
        // 用 context.WithoutCancel 脱离取消信号写库），重拉历史回填 serverId，
        // 让被打断/出错的气泡同样具备删除/重生成/分支能力（不再只有复制按钮）。
        // 停止场景的落库发生在"服务端检测到断连之后"，存在异步窗口；且断连瞬间
        // 最后一两段增量可能丢失，落库内容 ≥ 本地内容。因此按"历史最后一条助手
        // 回答以本地内容开头"判定本轮已入库，并做有限重试避免拉到中间态。
        const localContent = get().messages.find((m) => m.id === botMsg.id)?.content ?? ''
        const match = (list: ChatMessage[]) => {
          const last = list[list.length - 1]
          return localContent !== '' && last?.role === 'assistant' && last.content.startsWith(localContent)
        }
        let msgs: ChatMessage[] | null = null
        for (let i = 0; i < (errored ? 1 : 3); i++) {
          if (i > 0) await new Promise((r) => setTimeout(r, 350))
          const history = await fetchMessages(sessionId).catch(() => null)
          if (!history) break
          msgs = fromHistory(history)
          if (match(msgs)) break
        }
        // 停顿/重拉期间可能已发起新一轮对话或切换了会话（同会话仍有流在跑、
        // 本地气泡已被替换，或已切走该会话）：不再重拉，避免覆盖新视图。
        if (
          get().activeId === sessionId &&
          msgs &&
          !get().busySessions.includes(sessionId) &&
          get().messages.some((m) => m.id === botMsg.id) &&
          match(msgs)
        ) {
          set({ messages: msgs })
        }
      }
    } catch (err) {
      // 发送彻底失败（网络/未登录等）：移除 assistant 占位气泡，
      // 错误只通过 alert 提示，不进入对话内容。已切走时静默——该会话历史
      // 未落库，切回时从后端拉取自然不含占位气泡。
      if (get().activeId === sessionId) {
        set({ messages: get().messages.filter((m) => m.id !== botMsg.id) })
        alert(`发送失败：${(err as Error).message}`)
      }
    } finally {
      // 移除本会话忙标记；sending 重算为"当前活动会话是否仍在跑"。
      // aborter/pendingLocalCall 是全局单值，仅当自己仍是最新流时才重置，
      // 避免后台旧流收尾时误清新会话流的停止句柄/确认弹窗。
      const busy = get().busySessions.filter((x) => x !== sessionId)
      set({
        busySessions: busy,
        sending: busy.includes(get().activeId ?? ''),
        ...(get().aborter === aborter ? { aborter: null } : {}),
        ...(get().aborter === aborter ? { pendingLocalCall: null } : {}),
      })
      // 会话 updated_at 变化，静默刷新列表
      void get().loadSessions()
    }
  },

  stopStreaming: () => {
    const aborter = get().aborter
    if (!aborter) return
    aborter.abort()
    // 重新生成中：不在这里标记消息——regenerateMessage 收尾会重拉历史
    // 恢复原回答（后端已回滚截断），此刻标记 done 反而在恢复前闪烁空内容。
    const regenerating = get().regeneratingId !== null
    // 停止的是当前活动会话的流：同步移出忙列表，使停止后立即可再发
    // （同会话"停止后立即重发"语义与改造前一致）。
    const busySessions = get().busySessions.filter((id) => id !== get().activeId)
    set({
      sending: false,
      aborter: null,
      busySessions,
      ...(regenerating
        ? {}
        : {
            // 停止普通对话流：把未完成的消息标记为 done（保留已收到的内容）
            messages: get().messages.map((m) =>
              m.status === 'streaming' ? { ...m, status: 'done' } : m,
            ),
          }),
    })
  },

  setSidebarOpen: (open) => set({ sidebarOpen: open }),

  /** 用户对本地工具调用做出决定：
   *   允许 → Tauri 端本地执行 → 回填执行结果；
   *   拒绝 → 回填"用户拒绝"的失败结果，让 agent 调整策略。
   *   回填失败（会话已超时/已结束）静默吞掉，避免弹窗残留错误提示。 */
  resolveLocalCall: async (allow) => {
    const pending = get().pendingLocalCall
    if (!pending) return
    set({ pendingLocalCall: null })
    const { sessionId, toolCallId, command, cwd } = pending
    try {
      let content: string
      let isError: boolean
      if (!allow) {
        content = '用户拒绝在本地执行该命令'
        isError = true
      } else {
        const result = await runLocalShell(command, cwd)
        content = result.content
        isError = result.isError
      }
      await submitToolResult(sessionId, toolCallId, content, isError)
    } catch (err) {
      await submitToolResult(
        sessionId,
        toolCallId,
        `本地工具执行失败：${(err as Error).message}`,
        true,
      ).catch(() => {})
    }
  },
}))
