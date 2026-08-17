package schema

// Role 表示对话消息的发送方角色。
// 角色是 LLM 理解对话上下文的关键标记，OpenAI/DeepSeek 等主流
// 兼容协议均使用这些字符串值，因此定义必须与协议保持一致。
type Role string

const (
	// RoleSystem 系统指令：设定 Agent 身份与行为边界。
	// 通常位于对话最开头，优先级最高，可覆盖用户消息的约束。
	RoleSystem Role = "system"

	// RoleUser 用户输入。
	RoleUser Role = "user"

	// RoleAssistant 模型回答。
	RoleAssistant Role = "assistant"

	// RoleTool 工具执行结果的回填消息。
	// LLM 发起工具调用后，我们把执行结果以 role=tool 的消息发回，
	// 模型才能"看到"工具返回了什么并继续推理。这是 Function Calling
	// 闭环中必不可少的一环。
	RoleTool Role = "tool"
)

// Message 一条对话消息，是整个 Agent 系统中传递的最小数据单元。
//
// 它与 OpenAI 兼容协议的 chat message 一一对应，可直接序列化为
// JSON 发送给 DeepSeek 等模型。
//
// 字段说明：
//   - Reasoning 仅当 Role==RoleAssistant 时需要：模型的思考内容
//     （DeepSeek 返回的 reasoning_content，与 content 同级）。
//     思考内容有两个用途：① 前端渲染"思考过程"气泡；② 作为上下文
//     回传给模型——官方规则：无工具调用的轮次回传会被忽略，但一旦
//     本轮发生过工具调用，后续请求必须带上 reasoning_content 否则
//     400。因此框架统一保存并在请求中携带，天然满足工具轮要求。
//   - ToolCallID 仅当 Role==RoleTool 时需要：标记这条结果属于哪一次
//     工具调用（对应 ToolCall.ID），模型据此把结果和调用配对。
//   - ToolCalls 仅当 Role==RoleAssistant 时需要：模型在回复中附带
//     的工具调用指令列表。协议要求它必须和紧随其后的 role=tool
//     结果消息成对出现，否则模型会拒绝继续推理。
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	Reasoning  string     `json:"reasoning,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// IsToolResult 判断该消息是否为工具执行结果回填。
func (m Message) IsToolResult() bool {
	return m.Role == RoleTool
}
