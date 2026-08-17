// ---------------------------------------------------------------------------
// 与 gateway 契约一一对应的类型（见 docs/api/backend.md 与 openapi.yaml）
// ---------------------------------------------------------------------------

/** 统一错误体 {code, message, request_id} */
export interface ApiErrorBody {
  code: string
  message: string
  request_id: string
}

/** 前端携带的请求 ID（贯穿后端全链路） */
export type RequestId = string

/** 用户标签（key-value，后端 users.tags JSONB；用于配置可见性/分组权限等） */
export interface UserTag {
  key: string
  value: string
}

/** 用户 */
export interface User {
  id: string
  username: string
  role?: string
  /** 用户标签（来源：注册/登录时的 agent_id、管理员建用户等） */
  tags?: UserTag[]
}

/** 注册响应 */
export interface RegisterResponse {
  user_id: string
  username: string
}

/** 登录响应 */
export interface LoginResponse {
  access_token: string
  refresh_token: string
  expires_in: number
  user: User
}

/** 刷新响应 */
export interface RefreshResponse {
  access_token: string
  refresh_token: string
  expires_in: number
}

/** 思考模式配置（DeepSeek V4：enabled + reasoning_effort） */
export interface ThinkingConfig {
  /** 思考开关（false = 直接回答，不产生思考过程） */
  enabled: boolean
  /** 推理强度：low | high | max（缺省 = 厂商默认 high） */
  reasoning_effort?: string
}

/** 会话级配置（工具权限 + 思考模式；缺省 = 全部工具启用 + 思考按厂商默认） */
export interface SessionConfig {
  /** 允许使用的工具名白名单；空/缺省 = 全部工具启用（旧字段，兼容旧会话） */
  enabled_tools?: string[]
  /** 用户级资源标识（能力 id 或技能名）；空/缺省 = 全部启用。
   *  阶段1 起优先于 enabled_tools，服务端翻译为工具白名单。 */
  enabled_resources?: string[]
  /** enabled_resources 是否被显式设置过（含清空，presence 标记，与 kb_ids_set 同款）：
   *  true = 会话锁定资源选择——空数组即"不启用任何能力/技能"（只保留基础对话），
   *  不再跟随默认配置；false/缺省 = 未设置（空/缺省仍 = 全部启用）。 */
  enabled_resources_set?: boolean
  thinking?: ThinkingConfig
  /** 会话限定的知识库 ID 列表；空 = 本会话不使用知识库检索（kb_search 不装配），
   *  非空 = kb_search 限定在所选知识库内检索（模型显式传 kb_ids 时优先）。 */
  kb_ids?: string[]
  /** kb_ids 是否被显式设置过（含清空）：true = 会话锁定知识库选择，
   *  空数组即"不使用知识库"，不再跟随管理端默认配置；false = 跟随默认。 */
  kb_ids_set?: boolean
  /** 会话限定的 MCP server 启用列表（管理员会话级配置）；空 = 管理端已启用的
   *  全部 MCP server 生效（普通用户默认行为）。 */
  mcp_servers?: string[]
  /** mcp_servers 是否被显式设置过（含清空，presence 标记，与 enabled_resources_set 同款）：
   *  true = 会话锁定 MCP 选择——空数组即"本会话不装配任何 MCP 工具"（全不选）；
   *  false/缺省 = 未设置（空/缺省 = 全部已启用 server 生效）。 */
  mcp_servers_set?: boolean
  /** 管理员级配置（快照固化，随会话创建写入；普通用户配置区不可见、不可改）：
   *  单次对话最大推理轮数；0 = 未设置（装配时回退服务实例默认）。 */
  max_rounds?: number
  /** 短期记忆窗口保留的最大消息数；0 = 未设置（装配时回退服务实例默认）。 */
  max_messages?: number
  /** 思考（工具调用）轮次上限；0 = 未设置（不单独限制，仅受 max_rounds 保护）。 */
  max_thinking_rounds?: number
  /** 会话选定的大模型名（llm-gateway 模型注册表内名称；空 = 未设置，
   *  装配时回退服务实例默认模型）。普通可配字段：配置区选择，创建会话时
   *  从智能体默认配置继承；llm-gateway 按此名路由到具体供应商。 */
  model?: string
  /** 会话运行模式：single（默认）| orchestrate（多智能体编排）。
   *  orchestrate 模式下用户消息作为编排目标，由服务端内置角色池拆解协作。 */
  mode?: 'single' | 'orchestrate'
  /** 编排方案（仅 mode=orchestrate 生效）：
   *  fixed（默认）| dynamic（LLM 动态分解子任务 DAG）。 */
  orchestrate_plan?: 'fixed' | 'dynamic'
  /** 能力类别 presence 标记（能力/技能作为独立配置类别，各自支持"全不选"）：
   * true = 能力白名单 = enabled_resources 中的能力项（空能力项 = 默认不启用
   * 任何能力）；false/缺省 = 能力未设置（跟随实例全量）。 */
  enabled_capabilities_set?: boolean
  /** 技能类别 presence 标记（语义同 enabled_capabilities_set）：
   * true = 技能白名单 = enabled_resources 中的技能项（空技能项 = 默认不启用
   * 任何技能）；false/缺省 = 技能未设置（跟随实例全量）。 */
  enabled_skills_set?: boolean
}

/** 工具信息（GET /v1/agent/tools，管理/调试用） */
export interface ToolInfo {
  name: string
  description: string
  /** 是否由外部（桌面客户端）代理执行——本地工具（阶段3） */
  external?: boolean
}

/** 普通用户可见的资源项（GET /v1/agent/resources，阶段1·权限分层）。
 *  只含 id/名称/说明，不含任何工具名与技能代码。 */
export interface ResourceInfo {
  /** 资源标识：能力 id（如 search）或技能名（如 emoji-helper） */
  id: string
  /** 展示名 */
  name: string
  /** 一句话说明 */
  description: string
  /** 来源：capability（内置能力）| skill（管理端上传技能） */
  type: 'capability' | 'skill'
}

/** 资源清单响应 */
export interface ListResourcesResponse {
  resources: ResourceInfo[]
}

// ---------------------------------------------------------------------------
// 管理端（admin panel，/v1/admin/*）契约（见 docs/api/admin.md）
// ---------------------------------------------------------------------------

/** 管理端模块元信息（GET /v1/admin/modules） */
export interface AdminModule {
  key: string
  name: string
  description: string
  /** false = 占位模块（前端渲染"规划中"） */
  implemented: boolean
}

/** 技能（管理端视图，Anthropic Agent Skills：目录 + SKILL.md） */
export interface Skill {
  name: string
  description: string
  license?: string
  /** frontmatter 语义版本号（metadata.version/version，可选） */
  semver?: string
  /** 注册的工具名（skill_<净化名>） */
  tool_name: string
  /** SKILL.md 完整内容（frontmatter + 正文） */
  content: string
  /** 目录内其它文件数（不含 SKILL.md） */
  file_count: number
  /** 目录内其它文件的相对路径（含子目录，用于验证 zip 结构是否保留） */
  files?: string[]
  updated_at: string
  /** 解析是否通过（无效技能在列表中可见，供修复） */
  valid: boolean
  error?: string
  /** 是否启用（禁用 = agent 不注册其工具） */
  enabled: boolean
  /** 当前生效版本号（从 1 起） */
  version: number
  /** 历史版本（不含当前，按版本号倒序） */
  versions?: SkillVersion[]
}

/** 技能历史版本元信息（版本身份 = 语义版本号，同一版本号只能有一份） */
export interface SkillVersion {
  semver: string
  updated_at: string
  size: number
}

/** MCP server 配置（与后端 mcp.ServerConfig 对应，POST/PUT 提交体） */
export interface McpServer {
  name: string
  /** 是否启用（缺省 = 启用；false = agent 不注册其工具） */
  enabled?: boolean
  /** stdio=本地命令 / http=远程 endpoint */
  transport?: 'stdio' | 'http'
  /** stdio：启动命令（如 npx） */
  command?: string
  /** stdio：命令参数 */
  args?: string[]
  /** stdio：子进程工作目录（可选；Claude/trae/workbuddy 标准字段） */
  cwd?: string
  /** stdio：子进程环境变量 */
  env?: Record<string, string>
  /** http：远程 endpoint 地址 */
  url?: string
  /** http：请求头 */
  headers?: Record<string, string>
  /** 连接/请求超时秒数 */
  timeout_seconds?: number
  /** 工具确认级别：L0 | L1 | L2 | L3 */
  default_permission?: string
  /** 最近一次"测试连接/启用"成功发现的工具名（管理端展示用） */
  discovered_tools?: { name: string; description?: string }[]
  /** 最近一次"测试连接/启用"失败原因（管理端展示用） */
  discovery_error?: string
}

/** MCP server 列表响应 */
export interface McpServerListResponse {
  servers: McpServer[]
}

// ---- 知识库管理（P3-A /v1/admin/kb，经 gateway→rag-service） ----------------

/** 知识库文档视图（摄取状态对前端可见） */
export interface KbDocument {
  doc_id: string
  kb_id: string
  file_name: string
  /** queued | processing | succeeded | failed */
  status: string
  chunk_count: number
  error?: string
  created_at: string
  updated_at: string
}

/** 知识库视图 */
export interface KnowledgeBase {
  id: string
  name: string
  description: string
  /** 所属智能体域（多租户隔离，'' 或缺省 = 默认域 tutor） */
  agent_id?: string
  /** 启用状态（false = 停用，资源启停体系；普通用户/会话检索不可见） */
  enabled: boolean
  doc_count: number
  created_at: string
  updated_at: string
  /** 详情接口附带（分页） */
  documents?: KbDocument[]
  total?: number
}

/** 检索预览命中片段 */
export interface KbSearchHit {
  chunk_id: string
  doc_id: string
  content: string
  source: string
  score: number
}

/** 知识库列表响应 */
export interface ListKnowledgeBasesResponse {
  bases: KnowledgeBase[]
}

/** 知识库轻量视图（GET /v1/agent/kbs 普通用户接口，供对话配置区勾选）。
 *  只含会话配置所需的轻量字段；域由后端按用户身份锁定。 */
export interface KbLite {
  id: string
  name: string
  description: string
  doc_count: number
}

/** 普通用户知识库列表响应（附带实际生效的资源域） */
export interface ListKbsResponse {
  bases: KbLite[]
  agent_id: string
}

// ---- 智能体管理（阶段3·多租户 /v1/admin/agents，仅最高超管） ----------------

/** 智能体（agents 注册表，含每个智能体的超管归属） */
export interface Agent {
  id: string
  name: string
  /** 描述（空 = 无） */
  description?: string
  /** 默认模型（创建时可指定；空 = 用实例默认） */
  model?: string
  /** 所属智能体超管 user_id（'' = 尚未绑定） */
  owner_user_id: string
  /** 状态：1=启用 0=停用 */
  status: 0 | 1
  /** 形象（emoji，空 = 用首字兜底） */
  avatar?: string
  /** 欢迎语（新会话首屏展示；空 = 用实例默认） */
  welcome?: string
  /** 按智能体系统提示词（空 = 用实例全局 prompt） */
  system_prompt?: string
  /** 默认推理强度 low/high/max（空 = 用实例默认） */
  reasoning_effort?: string
  created_at?: string
  updated_at?: string
}

/** 智能体列表响应 */
export interface ListAgentsResponse {
  agents: Agent[]
}

/** 创建智能体请求（仅最高超管；owner_user_id 将被授予 agent_admin 并绑定该智能体） */
export interface CreateAgentRequest {
  id: string
  name: string
  description?: string
  model?: string
  owner_user_id?: string
  avatar?: string
  welcome?: string
  system_prompt?: string
  reasoning_effort?: string
}

/** 更新智能体请求（PATCH；空串字段 = 清空，name 必填非空；reasoning_effort 仅 low/high/max） */
export interface UpdateAgentRequest {
  name: string
  description?: string
  model?: string
  avatar?: string
  welcome?: string
  system_prompt?: string
  reasoning_effort?: string
}

/** 智能体默认会话配置（agent_defaults.json；字段缺省 = 无该项默认） */
export interface AgentDefaults {
  /** 默认启用的工具名白名单 */
  enabled_tools?: string[]
  /** 默认启用的资源（能力 id / 技能名） */
  enabled_resources?: string[]
  /** 默认资源是否显式设置过（presence 标记）：true = 空数组即"默认不启用任何
   *  能力/技能"；false/缺省 = 无该项默认（跟随实例全局行为）。 */
  enabled_resources_set?: boolean
  /** 默认思考模式 */
  thinking?: ThinkingConfig
  /** 默认知识库 ID 列表（空数组 = 默认不使用知识库） */
  kb_ids?: string[]
  /** 默认知识库选择是否显式设置过（presence 标记）：true = 空数组即"新会话默认
   *  不使用知识库检索"；false/缺省 = 无该项默认。 */
  kb_ids_set?: boolean
  /** 默认 MCP server 启用列表（空数组 = 全部生效） */
  mcp_servers?: string[]
  /** 默认 MCP 选择是否显式设置过（presence 标记）：true = 空数组即"新会话默认
   *  不装配任何 MCP 工具"（全不选）；false/缺省 = 无该项默认（跟随实例全部生效）。 */
  mcp_servers_set?: boolean
  /** 管理员级默认（仅管理端可设，随快照固化到新会话；0 = 不设置该项默认，
   *  装配时回退服务实例默认值）。普通用户配置区不展示、不可改。
   *  单次对话最大推理轮数（1..100）、短期记忆窗口最大消息数（>=2，0 = 不设置）、
   *  思考（工具调用）轮次上限（1..100）。 */
  max_rounds?: number
  max_messages?: number
  max_thinking_rounds?: number
  /** 智能体域默认模型名（llm-gateway 模型注册表内名称；空 = 不设置该项默认，
   *  新会话回退服务实例默认模型）。普通用户可在会话配置区改选。 */
  model?: string
  /** 智能体域默认运行模式（single | orchestrate；空 = single）。 */
  mode?: 'single' | 'orchestrate'
  /** 智能体域默认编排方案（fixed | dynamic；空 = fixed）。
   *  仅 mode=orchestrate 生效；新会话创建时随快照固化，普通用户可在配置区改选。 */
  orchestrate_plan?: 'fixed' | 'dynamic'
  /** 默认能力全不选标记（presence 语义同 SessionConfig）：
   * true = 默认能力白名单 = enabled_resources 中的能力项（空 = 新会话默认
   * 不启用任何能力）；false/缺省 = 能力项无默认（跟随实例全量）。 */
  enabled_capabilities_set?: boolean
  /** 默认技能全不选标记（语义同 enabled_capabilities_set）。 */
  enabled_skills_set?: boolean
}

/** 智能体用量聚合（llm-gateway /v1/usage/agents/{id}；最近 N 天成功调用） */
export interface AgentUsage {
  agent_id: string
  /** 成功调用次数 */
  calls: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  /** 估算成本（美元） */
  cost_usd: number
  /** 最近一次成功调用时间（RFC3339；无调用则缺省） */
  last_used_at?: string
}

// ---- 数据管理（运营分析台 /v1/admin/data/overview，仅最高超管） -------------

/** 数据管理总览：会话统计 + 用量总览 + Top 用户用户名回填（三端聚合） */
export interface DataOverview {
  sessions: SessionStatsView
  usage: UsageOverview
  /** Top 用户 user_id → username 回填 */
  user_names: Record<string, string>
}

/** 会话统计（agent-service AdminSessionStats 扁平视图） */
export interface SessionStatsView {
  /** 按日新建会话数（完整日序列，含 0 值，升序） */
  days: SessionDayStat[]
  /** 按智能体域会话分布（会话数倒序） */
  agents: SessionAgentStat[]
  /** 全量累计有效会话数 */
  total_sessions: number
}

export interface SessionDayStat {
  /** YYYY-MM-DD */
  date: string
  sessions: number
}

export interface SessionAgentStat {
  /** '' = 管理端域 */
  agent_id: string
  sessions: number
}

/** 用量总览（llm-gateway /v1/usage/overview） */
export interface UsageOverview {
  summary: UsageSummary
  /** 完整日序列（含 0 值，升序） */
  daily: UsageDay[]
  by_model: UsageGroup[]
  by_agent: UsageGroup[]
  by_user: UsageUser[]
}

export interface UsageSummary {
  calls: number
  success: number
  failed: number
  /** 去重活跃用户数 */
  dau: number
  total_tokens: number
  cost_usd: number
}

export interface UsageDay {
  date: string
  calls: number
  success: number
  failed: number
  dau: number
  total_tokens: number
  cost_usd: number
}

export interface UsageGroup {
  key: string
  calls: number
  total_tokens: number
  cost_usd: number
}

export interface UsageUser {
  user_id: number
  calls: number
  total_tokens: number
  cost_usd: number
}

// ---- 用户管理（阶段3·多租户 /v1/admin/users，超管类角色） -------------------

/** 管理端用户视图（含角色与标签） */
export interface AdminUser {
  id: string
  username: string
  role?: string
  /** 用户标签（含 {key:'agent', value:<id>} 智能体归属） */
  tags?: UserTag[]
}

/** 用户列表响应 */
export interface ListUsersResponse {
  users: AdminUser[]
  total: number
}

// ---- 用户 token 配额（/v1/admin/quota，仅 super_admin） -------------

/** 单用户 token 配额视图（llm-gateway user_quota 表；0 = 不限） */
export interface UserQuota {
  user_id: number
  token_quota_month: number // 每月配额；0 = 不限
  updated_by: number
  updated_at: string
  used_this_month: number // 本月累计已用（实时聚合）
}

// ---- 工作区磁盘配额（/v1/admin/disk-quota，仅 super_admin） -------------

/** 单用户工作区保护区（protected/）磁盘配额视图；0 = 不限 */
export interface DiskQuota {
  user_id: number
  disk_quota_mb: number // 保护区配额上限（MB）；0 = 不限
  updated_by: number
  updated_at: string
}

// ---- 大模型管理（阶段3·P3 /v1/admin/models，super_admin + agent_admin） ----

/** 大模型接入配置（管理端视图；API Key 只存在于 llm-gateway，此处为打码值） */
export interface Model {
  /** 对外模型名（llm-gateway 注册表路由键；创建后不可修改） */
  name: string
  /** 供应商展示名（如 DeepSeek / Ollama / 本地） */
  provider_name?: string
  /** 上游 OpenAI 兼容端点 */
  base_url?: string
  /** 上游密钥打码值（****后 4 位）；空 = 本地模型或未配置 */
  api_key?: string
  /** 是否已配置有效密钥（本地模型为 false） */
  has_api_key?: boolean
  /** 实际发给上游的模型名；空 = 使用 name */
  upstream_model?: string
  /** 非流式超时（秒）；0 = 上游默认 60 */
  timeout_sec?: number
  /** 可重试错误最大重试次数；0 = 不重试 */
  max_retries?: number
  /** 输入单价（美元/百万 token） */
  prompt_price_per_1m?: number
  /** 输出单价（美元/百万 token） */
  completion_price_per_1m?: number
  /** 是否当前默认模型（唯一；不可删除/禁用，只能经设为默认操作转移） */
  is_default?: boolean
  /** 是否启用：false = 不参与路由、不出现在配置区下拉，但保留配置可再次启用 */
  enabled?: boolean
  /** 上游是否不支持 thinking/reasoning_effort（litellm custom_openai / Ollama 等）；true = 转发前剥离 */
  no_thinking?: boolean
  /** 请求级 max_tokens（completion 输出上限）；0 = 不设置（交上游默认） */
  max_tokens?: number
  created_at?: string
  updated_at?: string
}

/** 创建/更新模型请求体（更新时 api_key 留空 = 保留原密钥；is_default 只能经设为默认操作修改） */
export interface ModelInput {
  name: string
  provider_name?: string
  base_url?: string
  api_key?: string
  upstream_model?: string
  timeout_sec?: number
  max_retries?: number
  prompt_price_per_1m?: number
  completion_price_per_1m?: number
  is_default?: boolean
  /** 上游不支持 thinking/reasoning_effort 参数（litellm custom_openai / Ollama 等） */
  no_thinking?: boolean
  /** 请求级 max_tokens（completion 输出上限）；0 = 不设置（交上游默认）。大文档/长工具参数模型建议显式设置（如 16384），否则上游默认输出上限会截断生成 */
  max_tokens?: number
}

/** 模型列表响应（管理端点含密钥打码；公开 /v1/models 仅 name/provider_name/is_default） */
export interface ModelListResponse {
  models: Model[]
}

// ---------------------------------------------------------------------------
// 操作审计日志（阶段4·日志管理模块）
// ---------------------------------------------------------------------------

/** 单条操作审计记录（对应后端 adminsvc.AuditEntry） */
export interface AuditLogEntry {
  /** 操作时间（UTC RFC3339） */
  ts: string
  /** 操作者用户 ID */
  user_id: number
  /** 操作者角色（super_admin / agent_admin / admin） */
  role: string
  /** 操作者归属域（agent_admin/admin 非空；super_admin 无归属为空） */
  actor_agent?: string
  /** 操作目标域（日志文件所在域，查询过滤键） */
  target_agent: string
  /** 动作（模块.动词[.子路径]），如 skills.create / mcp.delete */
  action: string
  /** 原始请求方法 / 路径 */
  method: string
  path: string
  /** 响应状态码 */
  status: number
  /** 全链路请求 ID */
  request_id?: string
  /** handler 耗时（毫秒） */
  latency_ms?: number
}

/** 日志查询响应 */
export interface ListLogsResponse {
  logs: AuditLogEntry[]
  total: number
  page: number
  page_size: number
}

/** 创建用户请求（角色与标签权限由后端按调用者角色分层校验） */
export interface CreateUserRequest {
  username: string
  password: string
  /** user | agent_admin | super_admin（空 = user；agent_admin 会被强制绑定调用者智能体组） */
  role?: string
  /** 可选：追加智能体归属标签（仅超管可指定其它智能体） */
  agent_id?: string
  tags?: UserTag[]
}

/** 会话 */
export interface Session {
  id: string
  user_id: string
  title: string
  created_at: string
  updated_at: string
  /** 会话所属智能体域（'' = 管理端域；'<id>' = 对应智能体域） */
  agent_id?: string
  /** 会话配置（工具权限 / 思考模式） */
  config: SessionConfig
}

/** 会话列表响应 */
export interface ListSessionsResponse {
  sessions: Session[]
  total: number
}

/** 非流式对话响应 */
export interface ChatResult {
  content: string
  rounds: number
  tool_calls: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
}

/** 历史消息（GET /v1/agent/sessions/{id}/messages） */
export interface HistoryMessage {
  /** 数据库主键（BIGSERIAL 转字符串），删除/定位用 */
  id: string
  role: string
  content: string
  /** assistant 消息的思考内容（DeepSeek reasoning_content，工具轮回传必需） */
  reasoning: string
  tool_call_id: string
  tool_calls: string
  /** 轮次序号（每轮从 user 提问开始；重生成/分支/版本切换定位用） */
  round_no: number
  /** 该条回答的版本号（0=初始回答，重新生成递增） */
  version: number
  /** 该轮回答的版本总数（前端切换 UI 用） */
  total_versions: number
}

/** 重新生成响应（POST /v1/agent/sessions/{id}/messages/{mid}/regenerate） */
export interface RegenerateResult {
  content: string
  rounds: number
  tool_calls: number
  total_tokens: number
  /** 新生成的版本号 */
  version: number
}

/** SSE 事件（POST /v1/agent/sessions/{id}/chat/stream） */
export interface SSEEventBase {
  type: string
}
export interface SSEDeltaEvent extends SSEEventBase {
  type: 'delta'
  content: string
}
/** 思考内容增量（DeepSeek reasoning_content） */
export interface SSEReasoningEvent extends SSEEventBase {
  type: 'reasoning'
  content: string
}
/** 工具调用开始（参数已由后端拼装完整） */
export interface SSEToolCallEvent extends SSEEventBase {
  type: 'tool_call'
  name: string
  arguments: string
}
/** 工具执行返回（error 非空表示失败） */
export interface SSEToolResultEvent extends SSEEventBase {
  type: 'tool_result'
  name: string
  content: string
  error: string
}
/** 多智能体编排进度事件（mode=orchestrate）：子任务开始/结束、整体完成/失败 */
export interface SSETaskStatusEvent extends SSEEventBase {
  type: 'task_status'
  /** task_started | task_content | task_finished | run_completed | run_failed */
  task_type: string
  /** 子任务 ID（task_* 时非空） */
  task_id: string
  /** running | completed | failed | skipped（task_finished 时） */
  status: string
  /** 失败原因（run_failed / failed 时） */
  error: string
  /** 子任务输出增量（task_content 时，前端累积渲染打字机） */
  content?: string
  /** task_content 时区分增量内容：text | reasoning | tool_start | tool_end */
  kind?: string
  /** 该子任务累计 token（task_finished 时） */
  total_tokens: number
}
export interface SSEDoneEvent extends SSEEventBase {
  type: 'done'
  rounds: number
  tool_calls: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
}
export type SSEEvent =
  | SSEDeltaEvent
  | SSEReasoningEvent
  | SSEToolCallEvent
  | SSEToolResultEvent
  | SSETaskStatusEvent
  | SSEDoneEvent

/** SSE 流中错误事件（event: error） */
export interface SSEErrorEvent {
  message: string
}

/** 思考过程分段：模型"想→做→想"循环中的每一步。
 *  文本=思考内容；tool-call=决定调用工具；tool-result=工具真实返回。 */
export type ThinkingSegment =
  | { kind: 'text'; content: string }
  | { kind: 'tool-call'; name: string; arguments: string }
  | { kind: 'tool-result'; name: string; content: string; error?: boolean }

/** 编排子任务节点状态（mode=orchestrate 流式过程，前端内存态渲染进度轨迹） */
export interface OrchestrationTask {
  /** 子任务 ID（如 research/outline/content/review） */
  id: string
  /** running | completed | failed | skipped */
  status: 'running' | 'completed' | 'failed' | 'skipped'
  /** 失败原因（failed 时） */
  error?: string
  /** 子任务正文输出（task_content kind=text 增量累积；完整版仍落库 orchestration_runs） */
  content?: string
  /** 子任务思考增量（task_content kind=reasoning 累积，渲染"思考中"状态） */
  reasoning?: string
  /** 当前正在调用的工具名（task_content kind=tool_start 时设置；tool_end 清空） */
  activeTool?: string
  /** 已完成的工具调用名列表（task_content kind=tool_end 时追加，渲染工具履历） */
  toolHistory?: string[]
  /** 该子任务累计 token（completed 时） */
  totalTokens?: number
}

/** 前端消息模型（内存态：含流式状态与展示统计） */
export interface ChatMessage {
  /** 前端唯一 ID（历史消息 = 后端数据库主键；流式新消息 = 前端随机 UUID） */
  id: string
  role: 'user' | 'assistant' | 'tool'
  content: string
  /** 后端消息 ID（仅历史消息有；有值时消息可被删除） */
  serverId?: string
  /** assistant 消息的流式/完成状态 */
  status?: 'streaming' | 'done' | 'error'
  /** assistant 消息的思考过程分段（思考气泡渲染；历史回放由后端 reasoning+tool 消息合并而来） */
  thinking?: ThinkingSegment[]
  /** assistant 消息的编排子任务进度轨迹（mode=orchestrate 流式时逐步填充） */
  tasks?: OrchestrationTask[]
  /** 上下文压缩记录（历史回看：该轮发生过上下文压缩，收纳 dropped 条早期消息）。
   *  仅历史消息从 __condense_v1__ system 记录解析而来，渲染提示条。 */
  condensed?: { dropped: number; count: number }
  /** 工具调用名（历史消息解析 tool_calls 得出，统计展示用） */
  toolNames?: string[]
  /** 关联工具调用 ID（role=tool 时） */
  toolCallId?: string
  /** 完成统计（token 用量，仅展示） */
  stats?: {
    rounds: number
    toolCalls: number
    totalTokens: number
  }
  /** 轮次序号（历史消息来自后端 round_no；新消息发送后落库刷新获得） */
  roundNo?: number
  /** 该条回答的版本号（0=初始回答） */
  version?: number
  /** 该轮回答的版本总数（>1 时展示版本切换） */
  totalVersions?: number
  /** 图片消息的视觉解析状态（多模态预留：unsupported=未启用 / described=已生成描述）。
   *  仅 [图片] 注入消息使用；接入视觉方案后后端会回填 described。 */
  vision?: 'unsupported' | 'described'
}

/** 聊天上传文档结果（模块二）：后端解析复用 rag ingest，全文落盘用户工作区。 */
export interface ChatDocUploadResult {
  file_name: string
  /** 上传类型：doc（文档，正文已解析注入）| image（图片，视觉解析预留） */
  kind: 'doc' | 'image'
  /** 图片渲染地址（相对服务器根，如 /files/users/...；仅 image 类型返回） */
  url?: string
  /** 相对工作区根路径（users/<uid>/chat-files/<sid>/<file>，前端溯源展示） */
  rel_path: string
  /** 解析出的分段数 */
  segments: number
  /** 注入会话历史的字符数（超长截断时含提示） */
  injected_len: number
  /** 文档内嵌媒体相对路径（图片等，可能为空） */
  media?: string[]
  /** 解析警告（如分段降级），可能为空 */
  warnings?: string[]
}
