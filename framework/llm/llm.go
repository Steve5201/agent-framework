// Package llm 提供大模型接入的统一抽象层。
//
// 为什么需要这个包？
// 项目要对接多家大模型（本地测试用 DeepSeek，生产还要支持
// OpenAI、Kimi、智谱等）。如果 agent 直接调某个厂商的 HTTP 接口，
// 换一家模型就要改一堆代码。本包把"模型调用"抽象成统一接口：
//   - Provider：任何厂商实现这个接口即可接入；
//   - OpenAICompatible：一份实现吃遍主流厂商（它们都提供
//     OpenAI 兼容协议端点）；
//   - DeepSeek：基于 OpenAICompatible 的工厂，仅换端点与默认模型。
//
// 设计原则：
//  1. 与厂商无关：Request/Response 是"通用格式"，不掺厂商字段；
//  2. 流式/非流式双支持：Chat（等完整响应）与 ChatStream（SSE 逐 token）；
//  3. token 用量统计：Usage 贯穿请求，供后续成本控制（llm-gateway）；
//  4. 工具调用预埋：Request.Tools / Response.ToolCalls / ToolCallDelta
//     已按 OpenAI 协议就位，B3 的 Function Calling 直接复用。
//
// 扩展点：如需接入协议完全不兼容的厂商（如 Anthropic），只需
// 新写一个实现 Provider 接口的类型，agent 层代码零改动。
package llm

import (
	"github.com/Steve5201/agent-framework/schema"
)

// Usage 一次请求的 token 消耗统计（各厂商协议均返回类似字段）。
// llm-gateway 后续用它做成本统计与配额控制。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamOptions OpenAI 兼容协议的流式附加选项（stream_options）。
// DeepSeek 等厂商流式响应默认不回传 usage，需显式开启 include_usage
// 后才会在流末尾附带 usage 统计块。
type StreamOptions struct {
	// IncludeUsage 是否在流末尾附带 usage 统计块。
	IncludeUsage bool
}

// Request 一次模型调用请求（通用格式，与具体厂商无关）。
type Request struct {
	// Model 模型名（如 deepseek-v4-flash / gpt-4o-mini）。空则使用客户端默认。
	Model string

	// Messages 对话消息序列。system 指令放在最前面。
	Messages []schema.Message

	// Tools 可选：Function Calling 工具集（B3 使用）。
	// 非空时随请求下发，LLM 才有机会发起工具调用。
	Tools []schema.ToolSchema

	// Stream 是否流式返回。true 时需使用 ChatStream 接口。
	Stream bool

	// StreamOptions 流式附加选项。nil 时流式请求默认开启
	// include_usage（流末尾附带 usage 统计块）；显式设置可覆盖。
	StreamOptions *StreamOptions

	// 采样参数（OpenAI 协议标准参数）：
	// 三者均为指针，nil 表示"不覆盖"——优先取本请求值，其次取
	// 客户端 Config 默认值，最后不发该字段（使用服务端默认）。
	Temperature *float64 // 采样温度 0~2：越高随机性越强，越低越确定
	TopP        *float64 // 核采样 0~1：只保留累计概率前 top-p 的候选
	MaxTokens   *int     // 本次生成的最大 token 数上限

	// Thinking 思考模式配置（DeepSeek V4 等思考模型）。
	// nil = 不干预，沿用厂商默认（思考开启）；非 nil 则透传
	// thinking.type（enabled/disabled）与 reasoning_effort。
	Thinking *ThinkingConfig
}

// ThinkingConfig 思考模式配置（透传厂商 thinking 参数）。
type ThinkingConfig struct {
	// Enabled 思考开关：true → thinking.type=enabled；false → disabled。
	Enabled bool
	// ReasoningEffort 推理强度：low | high | max。空 = 不发（厂商默认 high）。
	ReasoningEffort string
}

// Response 一次非流式调用的结果。
type Response struct {
	// Content 模型生成的文本回答。
	Content string

	// Reasoning 模型的思考内容（DeepSeek reasoning_content）。
	// 思考模型会先"想"再"答"；思考内容与回答分开返回。
	Reasoning string

	// ToolCalls 模型请求调用的工具列表（未使用工具时为空）。
	ToolCalls []schema.ToolCall

	// FinishReason 结束原因：stop（正常）| tool_calls | length | content_filter。
	FinishReason string

	// Usage token 用量统计。
	Usage Usage
}

// ToolCallDelta 流式返回中的工具调用增量片段。
// 说明：流式场景下，模型把工具参数切成多个分片逐段下发，
// 例如 arguments 先来 `{"a":1,` 再来 `"b":2}`。调用方必须按
// Index 分组、按顺序拼接完整后再解析（B5 agent 端负责拼装）。
type ToolCallDelta struct {
	Index     int    // 并行多工具调用时的序号
	ID        string // 调用 ID（通常首片携带）
	Name      string // 工具名（通常首片携带）
	Arguments string // 参数 JSON 的增量片段
}

// StreamEvent 流式返回中的一个事件（一次 delta）。
// 大多数事件只含 Content 文本增量；思考模型会先以 Reasoning
// 增量到达思考内容；工具调用事件只含 ToolCallDelta。
type StreamEvent struct {
	Content   string          // 本次文本增量（可能为空）
	Reasoning string          // 本次思考增量（可能为空；DeepSeek reasoning_content）
	ToolCalls []ToolCallDelta // 本次工具调用增量（可能为空）
	Usage     *Usage          // 流结束时模型可能附带用量统计
	Done      bool            // 流是否已结束（收到 [DONE]）
	// FinishReason 模型结束原因（stop | tool_calls | length | content_filter）。
	// 与 Done 不同：Done 表示流结束，FinishReason 是模型"为何停止"的业务语义。
	// 客户端/网关（llm-gateway 流式收尾块）据此透传真实结束原因，
	// 依赖 finish_reason=tool_calls 触发工具执行的链路不能只看到 stop。
	FinishReason string
}

// Stream 流式迭代器接口。
//
// 用法（Go 惯例：io.EOF 表示正常结束）：
//
//	st, err := provider.ChatStream(ctx, req)
//	if err != nil { ... }
//	defer st.Close()
//	for {
//	    ev, err := st.Next()
//	    if errors.Is(err, io.EOF) {
//	        break
//	    }
//	    if err != nil {
//	        // 流中错误
//	    }
//	    // 处理 ev
//	}
type Stream interface {
	Next() (StreamEvent, error)
	Close() error
}
