package schema

import "encoding/json"

// PermissionLevel 工具权限分级。
//
// 背景：Agent 可以调用工具，但工具的能力越强，被滥用/误用的风险越大。
// 例如"计算 1+1"无害，而"执行任意脚本"可能删库。因此每个工具声明一个
// 权限级别，宿主环境根据级别决定"直接执行 / 弹窗确认 / 放入沙盒"。
//
// P1 阶段只实现 L0/L1 工具；L2/L3 的强制确认钩子在 B3 提供，
// 真正的沙盒执行器在 P4 由宿主层（agent-service / desktop）实现。
type PermissionLevel int

const (
	// PermissionL0Pure 纯计算：无副作用（如 calculator），直接执行。
	PermissionL0Pure PermissionLevel = iota

	// PermissionL1Read 只读访问：读取文件/查询数据，不修改任何状态。
	PermissionL1Read

	// PermissionL2Write 写操作：修改状态/写文件，需要用户确认。
	PermissionL2Write

	// PermissionL3Dangerous 危险操作：执行脚本/删除/联网，需要
	// 用户确认 + 沙盒隔离 + 全量审计日志。
	PermissionL3Dangerous
)

// String 返回权限级别的可读名称，便于日志与用户提示。
func (p PermissionLevel) String() string {
	switch p {
	case PermissionL0Pure:
		return "L0_pure"
	case PermissionL1Read:
		return "L1_read"
	case PermissionL2Write:
		return "L2_write"
	case PermissionL3Dangerous:
		return "L3_dangerous"
	default:
		return "unknown"
	}
}

// RequiresApproval 判断该级别是否必须经过用户确认才能执行。
// L0/L1 直接执行，L2/L3 必须确认。
func (p PermissionLevel) RequiresApproval() bool {
	return p >= PermissionL2Write
}

// ToolSchema 工具的描述信息，用于告诉 LLM"有哪些工具、长什么样"。
//
// 重要背景：LLM 本身不能直接执行代码，它只能"看到"这份 JSON 描述，
// 然后在需要时发起调用请求（ToolCall）。因此工具描述的准确性直接
// 决定调用成功率——描述写得越清楚，LLM 越不会乱调。
type ToolSchema struct {
	// Name 工具唯一名称，通常用 snake_case（如 get_weather）。
	// LLM 通过它发起调用，必须在注册表中唯一。
	Name string `json:"name"`

	// Description 功能说明，写给 LLM "看"的。
	// 要写清楚：做什么、何时用、注意事项。这是 LLM 决定是否调用
	// 该工具的唯一依据。
	Description string `json:"description"`

	// Parameters 参数定义，格式为 JSON Schema（对象）。
	// 例如 {"type":"object","properties":{"a":{"type":"number"}}}。
	// 使用 json.RawMessage 以便原样透传给 LLM，不做结构假设。
	Parameters json.RawMessage `json:"parameters"`

	// Required 必填参数名列表（与 Parameters 中声明的属性对应）。
	Required []string `json:"required,omitempty"`

	// Permission 该工具的权限级别，默认零值 L0（纯计算）。
	Permission PermissionLevel `json:"permission,omitempty"`

	// External 标记该工具由宿主外部执行（如桌面客户端本地工具、
	// 浏览器确认类工具）。
	//
	// 为 true 时，agent 循环不会直接调用 Execute，而是通过
	// AsyncRunner.Dispatch 派发给外部执行环境，挂起等待宿主调用
	// Session.SubmitToolResult 回填结果后继续循环。
	//
	// 典型用途：需要触达用户本机能力（shell/git/文件对话框）的工具——
	// 框架进程无法访问用户桌面，必须由宿主（如 Tauri 桌面端）代理执行。
	// 该字段仅框架内部使用，序列化发给 LLM 时会被 openai.go 过滤掉。
	External bool `json:"external,omitempty"`
}

// RequiresApproval 该工具调用前是否需用户确认（由权限级别决定）。
func (t ToolSchema) RequiresApproval() bool {
	return t.Permission.RequiresApproval()
}

// ToolCall 一次工具调用指令：由 LLM 在回复中携带，表示
// "请帮我调用名为 Name 的工具，参数是 Arguments"。
//
// 注意：这不是执行结果，而是"请求"。Agent 循环检测到它之后，
// 去注册表找到真实工具执行，再把结果包装成 ToolResult 回填给 LLM。
type ToolCall struct {
	// ID 本次调用的唯一标识（由 LLM 生成）。
	// 后续结果回填时靠它关联，因此必须原样保存。
	ID string `json:"id"`

	// Name 要调用的工具名，必须能在注册表中找到。
	Name string `json:"name"`

	// Arguments 调用参数，JSON 编码（由 LLM 生成）。
	// 执行前需要解析为具体类型。
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult 一次工具调用的执行结果，将作为 role=tool 的消息
// 回填给 LLM，让模型看到工具返回了什么并继续推理。
type ToolResult struct {
	// ToolCallID 对应 ToolCall.ID，保证结果与调用配对。
	ToolCallID string `json:"tool_call_id"`

	// Name 工具名，便于日志追踪。
	Name string `json:"name"`

	// Content 执行结果的文本表示。
	Content string `json:"content"`

	// IsError 标记执行是否失败。失败的结果同样回填给 LLM，
	// 让它知道"工具没跑通"，从而调整策略（例如换参数重试）。
	IsError bool `json:"is_error,omitempty"`
}
