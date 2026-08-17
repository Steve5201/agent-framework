import axios, { AxiosError, type InternalAxiosRequestConfig } from 'axios'
import { getAccessToken, getRefreshToken, setTokens, clearTokens } from './storage'
import { getServerUrl } from './settings'
import { getGuestId } from './guest'
import { genUuid } from './uuid'
import { DEFAULT_AGENT_ID } from './roles'
import type {
  AdminModule,
  AdminUser,
  Agent,
  AgentDefaults,
  AgentUsage,
  ApiErrorBody,
  ChatResult,
  ChatDocUploadResult,
  CreateAgentRequest,
  CreateUserRequest,
  DataOverview,
  DiskQuota,
  HistoryMessage,
  KbDocument,
  KbLite,
  KbSearchHit,
  KnowledgeBase,
  ListAgentsResponse,
  ListKnowledgeBasesResponse,
  ListKbsResponse,
  ListLogsResponse,
  ListSessionsResponse,
  ListUsersResponse,
  LoginResponse,
  McpServer,
  McpServerListResponse,
  Model,
  ModelInput,
  ModelListResponse,
  RefreshResponse,
  RegenerateResult,
  RegisterResponse,
  ResourceInfo,
  Session,
  SessionConfig,
  Skill,
  ToolInfo,
  UpdateAgentRequest,
  User,
  UserQuota,
  UserTag,
} from '@/types/api'

// ---------------------------------------------------------------------------
// 统一的业务错误（含后端错误码与 request_id，供 UI 精确提示）
// ---------------------------------------------------------------------------
export class ApiError extends Error {
  code: string
  requestId: string

  constructor(message: string, code = 'unknown', requestId = '') {
    super(message)
    this.name = 'ApiError'
    this.code = code
    this.requestId = requestId
  }
}

// ---------------------------------------------------------------------------
// axios 实例
//  服务器地址运行时动态读取（默认 localhost:8080，可在登录页修改；
//  部署时用 VITE_API_BASE_URL 覆盖默认值）。baseURL 在请求拦截器中逐请求设置。
// ---------------------------------------------------------------------------

/** 当前生效的服务器地址（每次请求动态读取，修改设置立即生效）。 */
export function getApiBase(): string {
  return getServerUrl()
}

export const api = axios.create({
  timeout: 60_000,
  headers: { 'Content-Type': 'application/json' },
})

// ---- 请求拦截：注入 baseURL + access token + 全链路 request_id ---------------
// 存储层为双后端（localStorage / Tauri store），读取是异步的。
api.interceptors.request.use(async (config) => {
  config.baseURL = getServerUrl()
  const token = await getAccessToken()
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  } else {
    // 游客（未登录）：注入本地游客 ID，服务端据此派生出稳定的负整数 user_id
    const guestId = getGuestId()
    if (guestId) config.headers['X-Guest-ID'] = guestId
  }
  config.headers['X-Request-Id'] = genUuid()
  return config
})

// ---- 401 单飞行刷新重试 ------------------------------------------------------
// 多个并发请求同时 401 时只发一次 refresh，其余等待同一 Promise。
let refreshPromise: Promise<string | null> | null = null

async function tryRefresh(): Promise<string | null> {
  const refresh = await getRefreshToken()
  if (!refresh) return null
  try {
    const resp = await axios.post<RefreshResponse>(`${getServerUrl()}/v1/auth/refresh`, {
      refresh_token: refresh,
    })
    await setTokens(resp.data.access_token, resp.data.refresh_token)
    return resp.data.access_token
  } catch {
    await clearTokens().catch(() => {})
    return null
  }
}

/**
 * 刷新 access token（供非 axios 通道复用，如 sse.ts 的 fetch 流式请求）。
 * 成功返回新 token；刷新/重放失败返回 null（调用方自行兜底）。
 */
export async function refreshAccessToken(): Promise<string | null> {
  return tryRefresh()
}

/** 当刷新彻底失败（refresh 也过期/吊销）时触发：UI 监听此事件跳登录页。 */
export const AUTH_EXPIRED_EVENT = 'agent:auth-expired'

type RetriableConfig = InternalAxiosRequestConfig & { _retried?: boolean }

api.interceptors.response.use(
  (resp) => resp,
  async (error: AxiosError<ApiErrorBody>) => {
    const original = error.config as RetriableConfig | undefined
    const status = error.response?.status
    const url = original?.url ?? ''
    const isAuthRoute = url.includes('/auth/login') || url.includes('/auth/refresh')

    // 401 且非登录/刷新自身 → 尝试刷新并重放一次
    if (status === 401 && original && !original._retried && !isAuthRoute) {
      original._retried = true
      refreshPromise ??= tryRefresh().finally(() => {
        refreshPromise = null
      })
      const newToken = await refreshPromise
      if (newToken) {
        original.headers.Authorization = `Bearer ${newToken}`
        return api(original)
      }
      // 刷新失败：令牌彻底失效，通知全局跳登录
      window.dispatchEvent(new CustomEvent(AUTH_EXPIRED_EVENT))
    }

    throw toApiError(error)
  },
)

/** 从 axios 错误中提取统一错误体；无响应体时给出兜底文案。 */
function toApiError(error: AxiosError<ApiErrorBody>): ApiError {
  const body = error.response?.data
  if (body?.message) {
    // 后端内部兜底错误（50001 "internal error"，真实原因在服务端日志）
    // 对用户没有意义，统一转成可操作的提示。
    if (Number(body.code) === 50001 || body.message === 'internal error') {
      // 保留后端具体 message（如"清理旧 MCP 代码目录失败"），便于管理员定位根因；
      // 仅当后端未给出具体信息时才退回通用提示。
      const detail = body.message && body.message !== 'internal error' ? `：${body.message}` : ''
      return new ApiError(`后端服务异常${detail}，请确认后端已启动且各服务健康`, 'internal', body.request_id)
    }
    return new ApiError(body.message, bizCodeToErrorCode(body.code), body.request_id)
  }
  if (error.code === 'ECONNABORTED') {
    return new ApiError('请求超时，请稍后重试', 'timeout', '')
  }
  if (!error.response) {
    return new ApiError(
      `无法连接服务器（${getServerUrl()}），请检查后端是否已启动`,
      'network',
      '',
    )
  }
  return new ApiError(`请求失败（HTTP ${error.response.status}）`, 'http_error', '')
}

/**
 * 整型业务码 → 字符串错误码。后端 HTTP 错误体 code 字段是整型（如 40901），
 * 但业务判断需要语义字符串（如 ALREADY_EXISTS）；此处统一转换，保证
 * UI 里 `e.code === 'XXX'` 的判断可靠。
 */
const BIZ_CODE_TO_CODE: Record<number, string> = {
  40001: 'INVALID_ARGUMENT',
  40002: 'FAILED_PRECONDITION',
  40101: 'UNAUTHENTICATED',
  40301: 'PERMISSION_DENIED',
  40401: 'NOT_FOUND',
  40901: 'ALREADY_EXISTS',
  40902: 'VERSION_CONFLICT',
  42901: 'RESOURCE_EXHAUSTED',
  49901: 'CANCELLED',
  50001: 'INTERNAL',
  50301: 'UNAVAILABLE',
  50401: 'DEADLINE_EXCEEDED',
}

function bizCodeToErrorCode(code: unknown): string {
  if (typeof code === 'string') return code
  if (typeof code === 'number') return BIZ_CODE_TO_CODE[code] ?? `BIZ_${code}`
  return 'unknown'
}

// ---------------------------------------------------------------------------
// 认证接口
// ---------------------------------------------------------------------------
/**
 * 注册（仅分智能体入口）：/v1/auth/register/{agent_id}。
 * 裸 /v1/auth/register 已下线——管理员只能被管理员创建，不能自助注册。
 */
export async function register(username: string, password: string, agentId?: string): Promise<RegisterResponse> {
  const url = agentId ? `/v1/auth/register/${encodeURIComponent(agentId)}` : '/v1/auth/register'
  const resp = await api.post<RegisterResponse>(url, { username, password })
  return resp.data
}

/**
 * 登录：
 *  - agentId 非空 → /v1/auth/login/{agent_id}（智能体门户，首次登录绑定 agent 标签）；
 *  - agentId 为空 → /v1/auth/login（管理员入口）。
 */
export async function login(username: string, password: string, agentId?: string): Promise<LoginResponse> {
  const url = agentId ? `/v1/auth/login/${encodeURIComponent(agentId)}` : '/v1/auth/login'
  const resp = await api.post<LoginResponse>(url, { username, password })
  return resp.data
}

export async function logout(refreshToken: string): Promise<void> {
  await api.post('/v1/auth/logout', { refresh_token: refreshToken })
}

export async function fetchMe(): Promise<User> {
  const resp = await api.get<{ id: string; username: string; role?: string; tags?: UserTag[] }>('/v1/auth/me')
  return resp.data
}

/** 用户自助修改密码（登录态）。旧密码错误/新密码不合规由后端返回明确错误。 */
export async function changePassword(oldPassword: string, newPassword: string): Promise<void> {
  await api.put('/v1/auth/password', { old_password: oldPassword, new_password: newPassword })
}

// ---------------------------------------------------------------------------
// 会话接口
// ---------------------------------------------------------------------------
/**
 * 创建会话。agentId 为会话所属智能体域：
 *  - ''（默认）→ 管理端域；
 *  - '<id>'    → 对应智能体域（/agent/<id> 页面下创建）。
 */
export async function createSession(title?: string, agentId = ''): Promise<Session> {
  const resp = await api.post<{ session: Session }>('/v1/agent/sessions', {
    ...(title ? { title } : {}),
    agent_id: agentId,
  })
  return resp.data.session
}

/** 分页列出会话。agentId：'' = 管理端域；'*' = 全部域；其它 = 精确匹配智能体域。 */
export async function listSessions(page = 1, pageSize = 50, agentId = ''): Promise<ListSessionsResponse> {
  const resp = await api.get<ListSessionsResponse>('/v1/agent/sessions', {
    params: { page, page_size: pageSize, agent_id: agentId },
  })
  return resp.data
}

/** 登录后合并游客会话到当前账号（POST /v1/agent/sessions/merge-guest）。 */
export async function mergeGuestSessions(guestId: string): Promise<number> {
  const resp = await api.post<{ migrated: number }>('/v1/agent/sessions/merge-guest', { guest_id: guestId })
  return resp.data.migrated
}

export async function getSession(id: string): Promise<Session> {
  const resp = await api.get<{ session: Session }>(`/v1/agent/sessions/${id}`)
  return resp.data.session
}

export async function deleteSession(id: string): Promise<void> {
  await api.delete(`/v1/agent/sessions/${id}`)
}

/** 重命名会话（标题 1~100 字符）。 */
export async function renameSession(id: string, title: string): Promise<Session> {
  const resp = await api.patch<{ session: Session }>(`/v1/agent/sessions/${id}`, { title })
  return resp.data.session
}

/** 更新会话配置（工具权限 / 思考模式；PATCH /v1/agent/sessions/{id}）。 */
export async function updateSessionConfig(id: string, config: SessionConfig): Promise<Session> {
  const resp = await api.patch<{ session: Session }>(`/v1/agent/sessions/${id}`, { config })
  return resp.data.session
}

/** 列出工具集（名称 + 描述，管理/调试用）。
 *  agentId 目标智能体域；缺省 = 后端当前实例域（向后兼容）。 */
export async function listTools(agentId?: string): Promise<ToolInfo[]> {
  const url = agentId ? withQuery('/v1/agent/tools', { agent_id: agentId }) : '/v1/agent/tools'
  const resp = await api.get<{ tools: ToolInfo[] }>(url)
  return resp.data.tools ?? []
}

/** 列出普通用户可见的资源清单（能力 + 技能，阶段1·权限分层）。
 *  只含 id/名称/说明，不含工具名与技能代码，供会话配置弹窗勾选用。
 *  agentId 目标智能体域；缺省 = 后端当前实例域（向后兼容）。 */
export async function listResources(agentId?: string): Promise<ResourceInfo[]> {
  const url = agentId ? withQuery('/v1/agent/resources', { agent_id: agentId }) : '/v1/agent/resources'
  const resp = await api.get<{ resources: ResourceInfo[] }>(url)
  return resp.data.resources ?? []
}

/** 列出当前资源域的知识库（普通用户可访问，对话配置区"知识库"弹窗勾选用；
 *  域由后端按用户身份锁定，agentId 仅对最高超管生效——可跟随切换智能体）。 */
export async function listKbs(agentId = DEFAULT_AGENT_ID): Promise<KbLite[]> {
  const resp = await api.get<ListKbsResponse>(withQuery('/v1/agent/kbs', { agent_id: agentId }))
  return resp.data.bases ?? []
}

/** 读取当前资源域的智能体默认会话配置（普通用户接口，P3 反馈）。
 *  对话配置区"大模型"弹窗的回退链用：会话绑定模型失效时，取智能体默认配置
 *  的 model 字段，再找不到才回退系统默认。域由后端按用户身份锁定
 *  （agentId 仅对最高超管生效）。无默认配置 → 空对象。 */
export async function getAgentDefaults(agentId = DEFAULT_AGENT_ID): Promise<AgentDefaults> {
  const resp = await api.get<{ defaults: AgentDefaults }>(withQuery('/v1/agent/defaults', { agent_id: agentId }))
  return resp.data.defaults ?? {}
}

export async function fetchMessages(id: string): Promise<HistoryMessage[]> {
  const resp = await api.get<{ messages: HistoryMessage[] }>(`/v1/agent/sessions/${id}/messages`)
  return resp.data.messages
}

/**
 * 上传聊天文档（模块二）：multipart 字段 file。
 * 后端复用 rag ingest 解析管线（md/txt/html/xlsx 原生；pdf/docx/pptx 委托沙盒），
 * 全文落盘用户工作区 users/<uid>/chat-files/<sid>/ 并注入一条限长 user 消息，
 * 后续轮次可持续追问。超时放宽：沙盒解析 pdf/docx 可能较慢。
 */
export async function uploadChatDocument(sessionId: string, file: File): Promise<ChatDocUploadResult> {
  const form = new FormData()
  form.append('file', file)
  const resp = await api.post<ChatDocUploadResult>(
    `/v1/agent/sessions/${sessionId}/documents`,
    form,
    {
      headers: { 'Content-Type': 'multipart/form-data' },
      timeout: 120_000,
    },
  )
  return resp.data
}

/** 删除一轮完整对话（该轮 user + assistant + 工具对全删；删空后会话自动删除）。 */
export async function deleteMessage(sessionId: string, messageId: string): Promise<void> {
  await api.delete(`/v1/agent/sessions/${sessionId}/messages/${messageId}`)
}

/** 重新生成某轮回答（旧版本保留，可切换；返回新生成的版本号）。 */
export async function regenerate(sessionId: string, messageId: string): Promise<RegenerateResult> {
  const resp = await api.post<RegenerateResult>(
    `/v1/agent/sessions/${sessionId}/messages/${messageId}/regenerate`,
  )
  return resp.data
}

/** 切换某轮活跃版本。 */
export async function setActiveVersion(sessionId: string, messageId: string, version: number): Promise<void> {
  await api.post(`/v1/agent/sessions/${sessionId}/messages/${messageId}/version`, { version })
}

/** 基于某轮之前的上下文创建分支会话（返回新会话）。 */
export async function createBranch(sessionId: string, messageId: string): Promise<Session> {
  const resp = await api.post<{ session: Session }>(
    `/v1/agent/sessions/${sessionId}/messages/${messageId}/branch`,
  )
  return resp.data.session
}

export async function chat(sessionId: string, content: string): Promise<ChatResult> {
  const resp = await api.post<ChatResult>(`/v1/agent/sessions/${sessionId}/chat`, { content })
  return resp.data
}

/** 回填外部工具执行结果（阶段3·本地工具代理）。
 *  POST /v1/agent/sessions/{id}/tool-results——桌面客户端完成本地工具
 *  执行后调用，唤醒 agent-service 中挂起等待的会话继续推理。 */
export async function submitToolResult(
  sessionId: string,
  toolCallId: string,
  content: string,
  isError = false,
): Promise<void> {
  await api.post(`/v1/agent/sessions/${sessionId}/tool-results`, {
    tool_call_id: toolCallId,
    content,
    is_error: isError,
  })
}

// ---------------------------------------------------------------------------
// 管理端接口（admin panel，/v1/admin/*，需 admin 角色）
// ---------------------------------------------------------------------------
export async function adminListModules(): Promise<AdminModule[]> {
  const resp = await api.get<{ modules: AdminModule[] }>('/v1/admin/modules')
  return resp.data.modules ?? []
}

// ---- 技能管理（多租户：agentId = 资源域，缺省 = 默认域 tutor） ----------------
export async function adminListSkills(agentId = DEFAULT_AGENT_ID): Promise<Skill[]> {
  const resp = await api.get<{ skills: Skill[] }>(withQuery('/v1/admin/skills', { agent_id: agentId }))
  return resp.data.skills ?? []
}

export async function adminCreateSkill(name: string, content: string, agentId = DEFAULT_AGENT_ID): Promise<Skill> {
  const resp = await api.post<{ skill: Skill }>(withQuery('/v1/admin/skills', { agent_id: agentId }), { name, content })
  return resp.data.skill
}

export async function adminUpdateSkill(
  name: string,
  content: string,
  overwrite = false,
  agentId = DEFAULT_AGENT_ID,
): Promise<Skill> {
  const resp = await api.put<{ skill: Skill }>(
    withQuery(`/v1/admin/skills/${encodeURIComponent(name)}`, {
      agent_id: agentId,
      ...(overwrite ? { overwrite: 'true' } : {}),
    }),
    { content },
  )
  return resp.data.skill
}

export async function adminDeleteSkill(name: string, agentId = DEFAULT_AGENT_ID): Promise<void> {
  await api.delete(withQuery(`/v1/admin/skills/${encodeURIComponent(name)}`, { agent_id: agentId }))
}

/**
 * 上传 zip 技能包。技能名与版本号由后端从包内 SKILL.md 自动提取，
 * 前端只传文件即可。overwrite=true 覆盖同版本号（默认同版本不同内容会 409）。
 */
export async function adminUploadSkill(file: File, overwrite = false, agentId = DEFAULT_AGENT_ID): Promise<Skill> {
  const form = new FormData()
  form.append('file', file)
  const resp = await api.post<{ skill: Skill }>(
    withQuery('/v1/admin/skills/upload', {
      agent_id: agentId,
      ...(overwrite ? { overwrite: 'true' } : {}),
    }),
    form,
    { headers: { 'Content-Type': 'multipart/form-data' } },
  )
  return resp.data.skill
}

/** 启用/禁用技能（PATCH /v1/admin/skills/{name}/enabled）。 */
export async function adminSetSkillEnabled(name: string, enabled: boolean, agentId = DEFAULT_AGENT_ID): Promise<Skill> {
  const resp = await api.patch<{ skill: Skill }>(
    withQuery(`/v1/admin/skills/${encodeURIComponent(name)}/enabled`, { agent_id: agentId }),
    { enabled },
  )
  return resp.data.skill
}

/** 回滚技能到历史版本（POST /v1/admin/skills/{name}/versions/{semver}/restore）。 */
export async function adminRestoreSkillVersion(name: string, semver: string, agentId = DEFAULT_AGENT_ID): Promise<Skill> {
  const resp = await api.post<{ skill: Skill }>(
    withQuery(
      `/v1/admin/skills/${encodeURIComponent(name)}/versions/${encodeURIComponent(semver)}/restore`,
      { agent_id: agentId },
    ),
  )
  return resp.data.skill
}

// withQuery 拼接查询参数：url + {key=value}；url 已有 ? 则用 &。空值忽略。
function withQuery(url: string, kv: Record<string, string>): string {
  const parts = Object.entries(kv).filter(([, v]) => v !== '' && v !== undefined)
  if (parts.length === 0) return url
  const sep = url.includes('?') ? '&' : '?'
  return `${url}${sep}${parts.map(([k, v]) => `${k}=${encodeURIComponent(v)}`).join('&')}`
}

// ---- MCP server 管理（多租户：agentId = 资源域，缺省 = 默认域 tutor） -----------
export async function adminListMcpServers(agentId = DEFAULT_AGENT_ID): Promise<McpServer[]> {
  const resp = await api.get<McpServerListResponse>(withQuery('/v1/admin/mcp-servers', { agent_id: agentId }))
  return resp.data.servers ?? []
}

export async function adminCreateMcpServer(cfg: McpServer, agentId = DEFAULT_AGENT_ID): Promise<McpServer> {
  const resp = await api.post<{ server: McpServer }>(withQuery('/v1/admin/mcp-servers', { agent_id: agentId }), cfg)
  return resp.data.server
}

export async function adminUpdateMcpServer(name: string, cfg: McpServer, agentId = DEFAULT_AGENT_ID): Promise<McpServer> {
  const resp = await api.put<{ server: McpServer }>(
    withQuery(`/v1/admin/mcp-servers/${encodeURIComponent(name)}`, { agent_id: agentId }),
    cfg,
  )
  return resp.data.server
}

export async function adminDeleteMcpServer(name: string, agentId = DEFAULT_AGENT_ID): Promise<void> {
  await api.delete(withQuery(`/v1/admin/mcp-servers/${encodeURIComponent(name)}`, { agent_id: agentId }))
}

/** 启用/禁用 MCP server（PATCH /v1/admin/mcp-servers/{name}/enabled）。
 *  启用是真实动作：后端会实际连接并发现工具，连不上则返回错误（不会启用）。 */
export async function adminSetMcpEnabled(name: string, enabled: boolean, agentId = DEFAULT_AGENT_ID): Promise<McpServer> {
  const resp = await api.patch<{ server: McpServer }>(
    withQuery(`/v1/admin/mcp-servers/${encodeURIComponent(name)}/enabled`, { agent_id: agentId }),
    { enabled },
  )
  return resp.data.server
}

/** 测试连接已保存的 MCP server（POST .../{name}/test），返回 {server, tools, error}。 */
export async function adminTestMcpServer(
  name: string,
  agentId = DEFAULT_AGENT_ID,
): Promise<{ server: McpServer; tools: McpToolInfo[]; error: string }> {
  const resp = await api.post<{ server: McpServer; tools: McpToolInfo[]; error: string }>(
    withQuery(`/v1/admin/mcp-servers/${encodeURIComponent(name)}/test`, { agent_id: agentId }),
  )
  return resp.data
}

/** 测试一段尚未保存的 MCP 配置（POST /v1/admin/mcp-servers/test），返回 {tools, error}。 */
export interface McpToolInfo {
  name: string
  description?: string
}

export async function adminTestMcpConfig(cfg: McpServer): Promise<{ tools: McpToolInfo[]; error: string }> {
  const resp = await api.post<{ tools: McpToolInfo[]; error: string }>('/v1/admin/mcp-servers/test', cfg)
  return resp.data
}

/** 上传"本地 MCP 代码"zip 包并注册为 stdio server（POST .../upload）。
 *  同名 server 默认返回 409（ALREADY_EXISTS），需用户确认后带 overwrite=true 重试覆盖。 */
export async function adminUploadMcpServer(
  file: File,
  name = '',
  entry = '',
  overwrite = false,
  agentId = DEFAULT_AGENT_ID,
): Promise<McpServer> {
  const form = new FormData()
  form.append('file', file)
  if (name) form.append('name', name)
  if (entry) form.append('entry', entry)
  const resp = await api.post<{ server: McpServer }>(
    withQuery('/v1/admin/mcp-servers/upload', {
      agent_id: agentId,
      ...(overwrite ? { overwrite: 'true' } : {}),
    }),
    form,
    { headers: { 'Content-Type': 'multipart/form-data' } },
  )
  return resp.data.server
}

// ---- 知识库管理（P3-A，多租户：agentId = 资源域） -------------------------------
export async function adminListKbs(agentId = DEFAULT_AGENT_ID): Promise<KnowledgeBase[]> {
  const resp = await api.get<ListKnowledgeBasesResponse>(withQuery('/v1/admin/kb', { agent_id: agentId }))
  return resp.data.bases ?? []
}

export async function adminCreateKb(name: string, description: string, agentId = DEFAULT_AGENT_ID): Promise<KnowledgeBase> {
  const resp = await api.post<{ kb: KnowledgeBase }>(withQuery('/v1/admin/kb', { agent_id: agentId }), {
    name,
    description,
  })
  return resp.data.kb
}

/** 知识库详情 + 文档分页 */
export async function adminGetKb(id: string, page = 1, pageSize = 20, agentId = DEFAULT_AGENT_ID): Promise<KnowledgeBase> {
  const resp = await api.get<{ kb: KnowledgeBase }>(
    withQuery(`/v1/admin/kb/${encodeURIComponent(id)}`, {
      agent_id: agentId,
      page: String(page),
      page_size: String(pageSize),
    }),
  )
  return resp.data.kb
}

export async function adminDeleteKb(id: string, agentId = DEFAULT_AGENT_ID): Promise<void> {
  await api.delete(withQuery(`/v1/admin/kb/${encodeURIComponent(id)}`, { agent_id: agentId }))
}

/** 上传文档到知识库（multipart，字段名 file） */
export async function adminUploadKbDoc(id: string, file: File, agentId = DEFAULT_AGENT_ID): Promise<KbDocument> {
  const form = new FormData()
  form.append('file', file)
  const resp = await api.post<{ doc: KbDocument }>(
    withQuery(`/v1/admin/kb/${encodeURIComponent(id)}/documents`, { agent_id: agentId }),
    form,
    { headers: { 'Content-Type': 'multipart/form-data' } },
  )
  return resp.data.doc
}

export async function adminDeleteKbDoc(kbId: string, docId: string, agentId = DEFAULT_AGENT_ID): Promise<void> {
  await api.delete(
    withQuery(`/v1/admin/kb/${encodeURIComponent(kbId)}/documents/${encodeURIComponent(docId)}`, { agent_id: agentId }),
  )
}

/** 更新知识库名称/描述/启用状态（enabled 缺省 = 不改，仅改名/描述） */
export async function adminUpdateKb(
  id: string,
  name: string,
  description: string,
  agentId = DEFAULT_AGENT_ID,
  enabled?: boolean,
): Promise<KnowledgeBase> {
  const resp = await api.put<{ kb: KnowledgeBase }>(withQuery(`/v1/admin/kb/${encodeURIComponent(id)}`, { agent_id: agentId }), {
    name,
    description,
    ...(enabled !== undefined ? { enabled } : {}),
  })
  return resp.data.kb
}

/** 手动重试摄取失败文档 */
export async function adminRetryKbDoc(kbId: string, docId: string, agentId = DEFAULT_AGENT_ID): Promise<KbDocument> {
  const resp = await api.post<{ doc: KbDocument }>(
    withQuery(`/v1/admin/kb/${encodeURIComponent(kbId)}/documents/${encodeURIComponent(docId)}/retry`, {
      agent_id: agentId,
    }),
  )
  return resp.data.doc
}

/** 检索预览：在指定知识库内检索，返回命中片段 */
export async function adminSearchKb(
  kbId: string,
  query: string,
  topK = 5,
  agentId = DEFAULT_AGENT_ID,
): Promise<KbSearchHit[]> {
  const resp = await api.post<{ hits: KbSearchHit[] }>(
    withQuery(`/v1/admin/kb/${encodeURIComponent(kbId)}/search`, { agent_id: agentId }),
    { query, top_k: topK },
  )
  return resp.data.hits ?? []
}

// ---- 智能体管理（阶段3·多租户，仅最高超管可访问） -------------------------------
export async function adminListAgents(): Promise<Agent[]> {
  const resp = await api.get<ListAgentsResponse>('/v1/admin/agents')
  return resp.data.agents ?? []
}

export async function adminCreateAgent(req: CreateAgentRequest): Promise<Agent> {
  const resp = await api.post<{ agent: Agent }>('/v1/admin/agents', req)
  return resp.data.agent
}

/** 智能体详情（super_admin 任意；agent_admin 仅自身归属域） */
export async function adminGetAgent(id: string): Promise<Agent> {
  const resp = await api.get<{ agent: Agent }>(`/v1/admin/agents/${encodeURIComponent(id)}`)
  return resp.data.agent
}

/** 更新智能体元数据（空串字段 = 清空；name 必填非空） */
export async function adminUpdateAgent(id: string, req: UpdateAgentRequest): Promise<Agent> {
  const resp = await api.patch<{ agent: Agent }>(`/v1/admin/agents/${encodeURIComponent(id)}`, req)
  return resp.data.agent
}

/** 软删除智能体（仅最高超管；tutor 不可删） */
export async function adminDeleteAgent(id: string): Promise<void> {
  await api.delete(`/v1/admin/agents/${encodeURIComponent(id)}`)
}

/** 启停智能体（仅最高超管；status=0 后该域禁止创建会话） */
export async function adminSetAgentStatus(id: string, status: 0 | 1): Promise<Agent> {
  const resp = await api.post<{ agent: Agent }>(`/v1/admin/agents/${encodeURIComponent(id)}/status`, { status })
  return resp.data.agent
}

/** 绑定/更换/解绑智能体超管（仅最高超管；owner_user_id 空串 = 解绑） */
export async function adminBindAgentOwner(id: string, ownerUserId: string): Promise<Agent> {
  const resp = await api.post<{ agent: Agent }>(`/v1/admin/agents/${encodeURIComponent(id)}/owner`, { owner_user_id: ownerUserId })
  return resp.data.agent
}

/** 智能体用量聚合（最近 N 天成功调用；days 1..90，缺省 7） */
export async function adminGetAgentUsage(id: string, days = 7): Promise<AgentUsage> {
  const resp = await api.get<AgentUsage>(`/v1/admin/agents/${encodeURIComponent(id)}/usage`, { params: { days } })
  return resp.data
}

// ---- 数据管理（运营分析台 /v1/admin/data/overview，仅最高超管） -------------

/** 数据管理总览（会话活跃度 + 用量/成本 + Top 用户用户名回填；days 1..90 缺省 30） */
export async function adminDataOverview(days = 30): Promise<DataOverview> {
  const resp = await api.get<DataOverview>('/v1/admin/data/overview', { params: { days } })
  return resp.data
}

/** 读取智能体默认会话配置（无配置 = 空对象） */
export async function adminGetAgentDefaults(id: string): Promise<AgentDefaults> {
  const resp = await api.get<{ defaults: AgentDefaults }>(`/v1/admin/agents/${encodeURIComponent(id)}/defaults`)
  return resp.data.defaults
}

/** 写入/清空智能体默认会话配置（空对象 = 清空） */
export async function adminPutAgentDefaults(id: string, def: AgentDefaults): Promise<AgentDefaults> {
  const resp = await api.put<{ defaults: AgentDefaults }>(`/v1/admin/agents/${encodeURIComponent(id)}/defaults`, def)
  return resp.data.defaults
}

// ---- 用户管理（阶段3·多租户，超管类角色可访问；管辖范围由后端按身份分层） --------
export async function adminListUsers(
  options: { page?: number; page_size?: number; keyword?: string } = {},
): Promise<ListUsersResponse> {
  const resp = await api.get<ListUsersResponse>('/v1/admin/users', { params: options })
  return resp.data
}

export async function adminCreateUser(req: CreateUserRequest): Promise<AdminUser> {
  const resp = await api.post<{ user: AdminUser }>('/v1/admin/users', req)
  return resp.data.user
}

/** 重置用户密码（PUT /v1/admin/users/{id}；调用者角色须高于被重置账号，平级拒绝） */
export async function adminResetPassword(userId: string, password: string): Promise<AdminUser> {
  const resp = await api.put<{ user: AdminUser }>(`/v1/admin/users/${encodeURIComponent(userId)}`, { password })
  return resp.data.user
}

/** 删除用户（DELETE /v1/admin/users/{id}；禁止删除自己与最后一名最高超管） */
export async function adminDeleteUser(userId: string): Promise<void> {
  await api.delete(`/v1/admin/users/${encodeURIComponent(userId)}`)
}

// ---- 用户 token 配额（/v1/admin/quota，仅 super_admin） -------------

/** 配额列表（含本月用量；llm-gateway 实时聚合） */
export async function adminListQuota(): Promise<UserQuota[]> {
  const resp = await api.get<{ quotas: UserQuota[] }>('/v1/admin/quota')
  return resp.data.quotas ?? []
}

/** 设置/覆盖用户配额；tokenQuotaMonth=0 表示不限 */
export async function adminSetUserQuota(userId: string, tokenQuotaMonth: number): Promise<void> {
  await api.put(`/v1/admin/quota/${encodeURIComponent(userId)}`, { token_quota_month: tokenQuotaMonth })
}

/** 删除用户配额覆盖（恢复角色默认：管理员不限 / 普通用户 1000 万） */
export async function adminClearUserQuota(userId: string): Promise<void> {
  await api.delete(`/v1/admin/quota/${encodeURIComponent(userId)}`)
}

// ---- 工作区磁盘配额（/v1/admin/disk-quota，仅 super_admin） -------------

/** 显式磁盘配额覆盖列表（无记录 = 该用户走角色默认） */
export async function adminListDiskQuota(): Promise<DiskQuota[]> {
  const resp = await api.get<{ quotas: DiskQuota[] }>('/v1/admin/disk-quota')
  return resp.data.quotas ?? []
}

/** 设置/覆盖用户保护区磁盘配额；diskQuotaMb=0 表示不限 */
export async function adminSetDiskQuota(userId: string, diskQuotaMb: number): Promise<void> {
  await api.put(`/v1/admin/disk-quota/${encodeURIComponent(userId)}`, { disk_quota_mb: diskQuotaMb })
}

/** 删除用户磁盘配额覆盖（恢复角色默认） */
export async function adminClearDiskQuota(userId: string): Promise<void> {
  await api.delete(`/v1/admin/disk-quota/${encodeURIComponent(userId)}`)
}

// ---- 大模型管理（P3 /v1/admin/models，super_admin + agent_admin） -------------

/** 公开模型列表（GET /v1/models，会话配置区模型下拉；仅名字/供应商/默认位） */
export async function listPublicModels(): Promise<Model[]> {
  const resp = await api.get<ModelListResponse>('/v1/models')
  return resp.data.models ?? []
}

/** 模型全量列表（管理端点，含密钥打码与接入参数） */
export async function adminListModels(): Promise<Model[]> {
  const resp = await api.get<ModelListResponse>('/v1/admin/models')
  return resp.data.models ?? []
}

export async function adminCreateModel(input: ModelInput): Promise<Model> {
  const resp = await api.post<{ model: Model }>('/v1/admin/models', input)
  return resp.data.model
}

/** 更新模型接入参数；api_key 留空 = 保留原密钥；默认位经 /default 单独修改 */
export async function adminUpdateModel(name: string, input: ModelInput): Promise<Model> {
  const resp = await api.put<{ model: Model }>(`/v1/admin/models/${encodeURIComponent(name)}`, input)
  return resp.data.model
}

/** 设为默认模型（POST /v1/admin/models/{name}/default；新默认强制启用，旧默认自动取消） */
export async function adminSetModelDefault(name: string): Promise<void> {
  await api.post(`/v1/admin/models/${encodeURIComponent(name)}/default`)
}

/** 启用/禁用模型（POST /v1/admin/models/{name}/status；默认模型不可禁用） */
export async function adminSetModelEnabled(name: string, enabled: boolean): Promise<void> {
  await api.post(`/v1/admin/models/${encodeURIComponent(name)}/status`, { enabled })
}

export async function adminDeleteModel(name: string): Promise<void> {
  await api.delete(`/v1/admin/models/${encodeURIComponent(name)}`)
}

/**
 * 操作审计日志查询：
 *  - agent_id：超管可选（空 = 全部域；agent_admin/admin 由后端强制锁定自身域）；
 *  - action：动作前缀过滤（如 "skills" 或 "skills.update"）；
 *  - user_id：按操作者过滤；
 *  - page / page_size：分页（后端上限 200）。
 */
export async function adminListLogs(options: {
  agent_id?: string
  action?: string
  user_id?: string
  page?: number
  page_size?: number
} = {}): Promise<ListLogsResponse> {
  const resp = await api.get<ListLogsResponse>('/v1/admin/logs', { params: options })
  return resp.data
}
