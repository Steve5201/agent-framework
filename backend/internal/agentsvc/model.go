// Package agentsvc 实现 agent-service 业务域：会话管理、智能体对话、
// 历史持久化与恢复。
//
// 调用链（P2 架构决策）：
//
//	gateway ──gRPC AgentService──▶ agent-service ──HTTP(OpenAI)──▶ llm-gateway ──▶ 厂商
//	                        │
//	                        ├─ sessions/messages 落库（PostgreSQL，P2-41/P2-44）
//	                        ├─ 内嵌 framework agent 引擎（含通用工具集）
//	                        └─ user_id 经 gRPC metadata（x-user-id）注入（P2-46）
//
// 设计要点：
//   - agent-service 不直连厂商，统一走 llm-gateway（密钥收敛在网关一侧）；
//   - 每次对话把 DB 历史预加载进 framework 记忆（WithInitialHistory），
//     保证模型在完整上下文上继续（P2-44）；
//   - 同会话并发锁 + request_id 幂等，防止历史交错与重复执行（P2-47）。
package agentsvc

import (
	"encoding/json"
	"time"

	"github.com/Steve5201/agent-framework/schema"
)

// 会话状态。
const (
	// SessionStatusActive 正常会话。
	SessionStatusActive = 1
	// SessionStatusDeleted 已删除（软删，保留数据可审计）。
	SessionStatusDeleted = 0
)

// Session 会话领域模型（sessions 表）。
type Session struct {
	ID        int64
	UserID    int64
	AgentID   string // 会话所属智能体域（'' = 管理端域；'<id>' = 对应智能体域）
	Title     string
	Status    int
	CreatedAt time.Time
	UpdatedAt time.Time
	Config    SessionConfig // 会话配置（工具权限/思考模式，JSONB 列）
}

// SessionStats 管理端会话统计聚合结果（数据管理模块）。
type SessionStats struct {
	Daily   []SessionDayStat   // 近 days 天完整日序列（含 0 值，DB 时区）
	ByAgent []SessionAgentStat // 近 days 天按智能体域分布（会话数倒序）
	Total   int64              // 全量累计有效会话数（status=1）
}

// SessionDayStat 单日新建会话统计。
type SessionDayStat struct {
	Date     string // YYYY-MM-DD
	Sessions int64
}

// SessionAgentStat 单智能体域会话统计。
type SessionAgentStat struct {
	AgentID  string // '' = 管理端域
	Sessions int64
}

// maxSystemPromptRunes 按智能体基础提示词长度上限（rune 计数，防超长文本落库）。
const maxSystemPromptRunes = 4096

// SessionConfig 会话级配置（sessions.config JSONB 落库）。
// EnabledTools 为空 = 全部工具启用；Thinking 为空 = 思考按厂商默认（开启）。
type SessionConfig struct {
	EnabledTools []string `json:"enabled_tools,omitempty"`
	// EnabledResources 用户级资源标识（能力 id 或技能名）；空 = 全部启用。
	// 服务端把资源翻译成工具白名单后按此过滤；与 EnabledTools 并存时本字段优先。
	EnabledResources []string `json:"enabled_resources,omitempty"`
	// EnabledResourcesSet 是否显式设置过 EnabledResources（含清空，presence 标记，
	// 与 KBIDsSet 同款）：true = 会话锁定资源选择——空数组即"不启用任何能力/技能"
	// （只保留基础对话），不再跟随默认配置；false = 未设置（空/缺省 = 全部启用）。
	EnabledResourcesSet bool            `json:"enabled_resources_set,omitempty"`
	Thinking            *ThinkingConfig `json:"thinking,omitempty"`
	// KBIDs 会话限定的知识库 ID 列表；空 = 本会话不使用知识库检索（kb_search 不装配，
	// 模型不可调用）；非空 = kb_search 限定在所选知识库内检索（模型显式传 kb_ids 时优先）。
	KBIDs []string `json:"kb_ids,omitempty"`
	// KBIDsSet 是否显式设置过 kb_ids（含清空）：true = 本会话锁定 KBIDs 值
	// （nil/空 = 不使用知识库，不再跟随默认配置）；false = 未设置（跟随默认）。
	// 动态绑定下 repeated 空数组与未设置无法区分，必须靠此标记表达"显式清空"。
	KBIDsSet bool `json:"kb_ids_set,omitempty"`
	// MCPServers 会话限定的 MCP server 启用列表（管理员会话级配置）。
	// 空 = 管理端已启用的全部 MCP server 工具生效（普通用户默认行为）；
	// 非空 = 仅选中 server 的 mcp_<server>_ 工具装配（按 server 粒度过滤）。
	MCPServers []string `json:"mcp_servers,omitempty"`
	// MCPServersSet 是否显式设置过 mcp_servers（含清空，presence 标记，与
	// EnabledResourcesSet/KBIDsSet 同款）：true = 会话锁定 MCP 选择——空数组
	// 即"本会话不装配任何 MCP 工具"（全不选），不再回退"全部启用"；
	// false = 未设置（空/缺省 = 管理端全部已启用 server 生效）。
	MCPServersSet bool `json:"mcp_servers_set,omitempty"`

	// 管理员级配置（快照固化，只读于普通用户配置区；0 = 未设置，装配时
	// 回退服务实例默认值）。这些字段仅在会话创建时从管理端默认配置固化，
	// 普通用户更新配置时服务端保留快照原值，不允许用户改动。
	// MaxRounds 单次对话最大推理（LLM 调用）轮数，防止工具循环不收敛。
	MaxRounds int `json:"max_rounds,omitempty"`
	// MaxMessages 短期记忆窗口保留的最大消息数（历史恢复上限）。
	MaxMessages int `json:"max_messages,omitempty"`
	// MaxThinkingRounds 思考（工具调用）轮次上限；0 = 不单独限制。
	MaxThinkingRounds int `json:"max_thinking_rounds,omitempty"`
	// SystemPrompt 按智能体基础提示词（管理员级，只读于普通用户配置区）。
	// 来自 auth agents.system_prompt，经 CreateSessionRequest.system_prompt 固化进
	// 会话 config 快照（gateway 注入，SessionConfig 不暴露该字段，用户不可篡改）；
	// 非空时装配覆盖实例全局提示词（AGENT_SYSTEM_PROMPT）；空 = 用实例全局。
	SystemPrompt string `json:"system_prompt,omitempty"`

	// Model 会话选定的模型名（llm-gateway 模型注册表内名称；空 = 未设置，
	// 装配时回退服务实例默认模型）。普通可配字段：用户在配置区选择，
	// 创建会话时从 AgentDefaults 继承；llm-gateway 按此名路由到具体供应商。
	Model string `json:"model,omitempty"`

	// EnabledCapabilitiesSet 能力类别的 presence 标记（P3 反馈：能力与技能
	// 作为独立配置类别，各自支持"全不选"显式语义）。
	// true = 能力白名单 = EnabledResources 中的能力项（空能力项 = 默认不
	// 启用任何能力）；false = 能力未设置，跟随实例全量。
	EnabledCapabilitiesSet bool `json:"enabled_capabilities_set,omitempty"`
	// EnabledSkillsSet 技能类别的 presence 标记（语义同 EnabledCapabilitiesSet）：
	// true = 技能白名单 = EnabledResources 中的技能项（空技能项 = 默认不启用
	// 任何技能）；false = 技能未设置，跟随实例全量。
	// 两个标记都未设置 → 退化为旧的联合白名单语义（EnabledResourcesSet）。
	EnabledSkillsSet bool `json:"enabled_skills_set,omitempty"`

	// Mode 会话运行模式：single（默认，空/缺省）或 orchestrate（多智能体编排）。
	// orchestrate 模式下，用户消息作为编排目标，由服务端内置角色池拆解为
	// 子任务协作完成（见 orchestrate.go）。
	Mode string `json:"mode,omitempty"`

	// OrchestratePlan 编排方案（仅 mode=orchestrate 生效）：
	//   fixed   = 固定教研模板（默认，空/缺省）：研究→大纲→正文→审核；
	//   dynamic = LLM 动态分解：按用户目标实时拆解子任务 DAG（更灵活、多一次 LLM 调用）。
	// 空/缺省按 fixed 处理，向后兼容（旧配置 / 未设置即固定教研流水线）。
	OrchestratePlan string `json:"orchestrate_plan,omitempty"`
}

// ThinkingConfig 思考模式配置（DeepSeek V4）。
type ThinkingConfig struct {
	Enabled         bool   `json:"enabled"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// AgentDefaults 智能体域默认会话配置（文件态配置平面，见 defaults.go）。
//
// 落盘约定：<mcpDir>/<agent_id>/agent_defaults.json，与 skill/MCP 同构。
// 新会话未显式携带配置时继承该默认值；显式配置优先（ApplyDefaults 只填充
// 显式配置未设置的字段）。字段缺失 = 无该项默认；空数组 = 显式默认
// （如 kb_ids=[] = 默认不使用知识库检索），与 SessionConfig 语义一致。
type AgentDefaults struct {
	EnabledTools     []string `json:"enabled_tools,omitempty"`
	EnabledResources []string `json:"enabled_resources,omitempty"`
	// EnabledResourcesSet 是否显式设置过默认资源（presence 标记，与 SessionConfig 一致）：
	// true = 空/缺省数组即"默认不启用任何能力/技能"；false = 无该项默认（跟随实例全量）。
	EnabledResourcesSet bool            `json:"enabled_resources_set,omitempty"`
	Thinking            *ThinkingConfig `json:"thinking,omitempty"`
	KBIDs               []string        `json:"kb_ids,omitempty"`
	KBIDsSet            bool            `json:"kb_ids_set,omitempty"`
	MCPServers          []string        `json:"mcp_servers,omitempty"`
	// MCPServersSet 是否显式设置过默认 MCP server 列表（presence 标记，语义同
	// SessionConfig.MCPServersSet）：true = 空数组即"默认不装配任何 MCP 工具"。
	MCPServersSet bool `json:"mcp_servers_set,omitempty"`
	// 管理员级默认配置（仅管理端可设，随快照固化到新会话；0 = 不设置该项默认，
	// 装配时回退服务实例默认值）。普通用户配置区不展示、不可改。
	MaxRounds         int `json:"max_rounds,omitempty"`
	MaxMessages       int `json:"max_messages,omitempty"`
	MaxThinkingRounds int `json:"max_thinking_rounds,omitempty"`

	// Model 智能体域默认模型名（llm-gateway 模型注册表内名称；空 = 不设置
	// 该项默认，新会话回退服务实例默认模型）。普通用户可在配置区改选。
	Model string `json:"model,omitempty"`
	// EnabledCapabilitiesSet 默认能力全不选标记（presence 语义同 SessionConfig）：
	// true = 默认能力白名单 = EnabledResources 中的能力项（空 = 新会话默认
	// 不启用任何能力）；false = 能力项无默认（跟随实例全量）。
	EnabledCapabilitiesSet bool `json:"enabled_capabilities_set,omitempty"`
	// EnabledSkillsSet 默认技能全不选标记（语义同 EnabledCapabilitiesSet）。
	EnabledSkillsSet bool `json:"enabled_skills_set,omitempty"`
	// Mode 智能体域默认运行模式（single | orchestrate，空 = single）。
	Mode string `json:"mode,omitempty"`
	// OrchestratePlan 智能体域默认编排方案（fixed | dynamic，空 = fixed）。
	// 仅 mode=orchestrate 生效；新会话创建时随快照固化，普通用户可在配置区改选。
	OrchestratePlan string `json:"orchestrate_plan,omitempty"`
}

// ToolInfo 工具信息（ListTools 返回，供前端配置 UI 展示）。
type ToolInfo struct {
	Name        string
	Description string
	// External 是否由外部（桌面客户端）代理执行——本地工具。
	// 浏览器前端据此在本地工具调用时给出"需桌面客户端"的降级提示。
	External bool
}

// Active 会话是否处于正常状态。
func (s *Session) Active() bool { return s.Status == SessionStatusActive }

// Message 消息领域模型（messages 表，seq 由 repository 自动分配）。
type Message struct {
	ID            int64  // 数据库主键（BIGSERIAL，ListMessages 回读时填充，删除定位用）
	Role          string // user | assistant | tool | system
	Content       string
	Reasoning     string            // assistant 消息：思考内容（DeepSeek reasoning_content，工具轮回传必需）
	ToolCallID    string            // tool 消息：对应 assistant 的哪次工具调用
	ToolCalls     []schema.ToolCall // assistant 消息：工具调用指令
	RoundNo       int64             // 轮次序号（每个 user 消息开始新轮；删除/重生成/分支定位用）
	Version       int               // 重生成版本号（0=初始回答）
	TotalVersions int               // 该轮版本总数（ListMessages 统计返回，切换 UI 用）
	Hidden        bool              // 隐藏标记（repository 内部使用；DB 列 hidden）
}

// ToSchema 转 framework schema.Message（历史恢复、落库回读共用）。
func (m *Message) ToSchema() schema.Message {
	return schema.Message{
		Role:       schema.Role(m.Role),
		Content:    m.Content,
		Reasoning:  m.Reasoning,
		ToolCallID: m.ToolCallID,
		ToolCalls:  m.ToolCalls,
	}
}

// AuditToolCall 工具调用审计记录（audit_tool_calls 表，阶段1·审计）。
// 每次工具执行成功/失败各记一条，供管理端数据模块展示与安全审查。
type AuditToolCall struct {
	ID         int64           // 主键
	UserID     int64           // 操作者（auth.users.id，跨库无 FK）
	SessionID  int64           // 所在会话
	AgentName  string          // 智能体标识（当前统一 "default"，预留多智能体）
	Tool       string          // 工具名（如 file_ops、skill_emoji-helper）
	ToolCallID string          // 关联 assistant 消息中的工具调用 ID
	Arguments  json.RawMessage // 工具调用参数（原样 JSON，空对象时序列化为 {}）
	Result     string          // 工具返回文本（成功内容或错误描述）
	IsError    bool            // 是否执行失败
	DurationMs int64           // 单次执行耗时（毫秒）
	CreatedAt  time.Time       // 记录时间
}

// ResourceInfo 普通用户可见的资源项（ListResources 返回，阶段1·权限分层）。
// 只暴露 id/名称/说明，不含任何工具名与技能代码。
type ResourceInfo struct {
	ID          string // 资源标识：能力 id（如 search）或技能名（如 emoji-helper）
	Name        string // 展示名
	Description string // 一句话说明
	Type        string // capability | skill
}
