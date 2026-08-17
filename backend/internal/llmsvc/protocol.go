package llmsvc

import (
	"encoding/json"
	"time"
)

// 本文件定义 llm-gateway 作为"服务端"收发的 OpenAI 兼容协议结构。
//
// 说明：framework/llm 包内也有同名结构（openAIMessage 等），但那是
// "客户端"视角、且未导出。llm-gateway 作为独立服务端点，必须自持一份
// 入站/出站协议类型（职责不同：一端解析入站请求，一端构造出站响应）。

// chatCompletionRequest 入站请求体（OpenAI 兼容 /v1/chat/completions）。
type chatCompletionRequest struct {
	Model           string          `json:"model"`
	Messages        []openAIMessage `json:"messages"`
	Tools           []openAITool    `json:"tools,omitempty"`
	Stream          bool            `json:"stream"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	MaxTokens       *int            `json:"max_tokens,omitempty"`
	Thinking        *thinkingBody   `json:"thinking,omitempty"`         // DeepSeek V4 思考开关
	ReasoningEffort string          `json:"reasoning_effort,omitempty"` // 推理强度 low|high|max
}

// thinkingBody 思考模式开关体（DeepSeek V4：type = enabled | disabled）。
type thinkingBody struct {
	Type string `json:"type"`
}

// openAIMessage 协议消息。文本场景 content 为字符串；
// 多模态（图片等 content 为数组）暂不支持，转换层会拒绝。
// ReasoningContent 为思考模型返回/回传的思考内容（DeepSeek
// reasoning_content）：入站请求里 agent 会把历史工具轮的思考内容
// 原样回传（官方要求），本字段负责接住并透传给上游厂商。
type openAIMessage struct {
	Role             string           `json:"role"`
	Content          string           `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
}

// openAIToolCall 工具调用指令。
type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// openAITool 工具声明。
type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ---------------------------------------------------------------------------
// 出站响应（非流式）
// ---------------------------------------------------------------------------

// chatCompletionResponse 非流式响应体（OpenAI 兼容）。
type chatCompletionResponse struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Created int64       `json:"created"`
	Model   string      `json:"model"`
	Choices []outChoice `json:"choices"`
	Usage   outUsage    `json:"usage"`
}

type outChoice struct {
	Index        int        `json:"index"`
	Message      outMessage `json:"message"`
	FinishReason string     `json:"finish_reason"`
}

type outMessage struct {
	Role             string        `json:"role"`
	Content          string        `json:"content"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
	ToolCalls        []outToolCall `json:"tool_calls,omitempty"`
}

type outToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type outUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ---------------------------------------------------------------------------
// 出站响应（流式 SSE chunk）
// ---------------------------------------------------------------------------

// streamChunk 流式响应中的单个 data 块（OpenAI 兼容 chat.completion.chunk）。
type streamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	// Usage 仅出现在带 stream_options.include_usage 的流末尾块（choices 为空）。
	Usage *outUsage `json:"usage,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Role             string            `json:"role,omitempty"`
	Content          string            `json:"content,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ToolCalls        []streamToolDelta `json:"tool_calls,omitempty"`
}

type streamToolDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

// newStreamChunk 构造一个流式块（ID/创建时间/模型固定，choices 由调用方填充）。
func newStreamChunk(id, model string, created time.Time) streamChunk {
	return streamChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created.Unix(),
		Model:   model,
	}
}
