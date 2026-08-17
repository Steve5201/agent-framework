package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Steve5201/agent-framework/schema"
)

// OpenAICompatible 实现 OpenAI 兼容协议（POST /chat/completions）的客户端。
//
// 之所以一份实现能接多家厂商，是因为 OpenAI 生态普及后，几乎所有
// 主流模型厂商（DeepSeek/OpenAI/Kimi/智谱/通义...）都提供了
// OpenAI 兼容的 HTTP 端点——协议完全一致，只是 BaseURL 和模型名不同。
type OpenAICompatible struct {
	name        string        // 供应商名（日志/路由）
	baseURL     string        // 协议端点，如 https://api.deepseek.com
	apiKey      string        // 模型接入密钥（由调用方传入）
	model       string        // 默认模型名
	client      *http.Client  // HTTP 客户端（不设全局超时，由调用方 ctx 控制）
	timeout     time.Duration // 非流式请求的整体超时
	maxRetries  int           // 最大重试次数
	temperature *float64      // 默认采样温度（nil 表示不覆盖）
	topP        *float64      // 默认核采样（nil 表示不覆盖）
	maxTokens   *int          // 默认 token 上限（nil 表示不覆盖）
}

// Config OpenAI 兼容客户端的构造配置。
type Config struct {
	Name       string        // 供应商名（日志/路由）
	BaseURL    string        // 协议端点
	APIKey     string        // 模型接入密钥
	Model      string        // 默认模型名（请求未指定时使用）
	Timeout    time.Duration // 非流式请求超时（默认 60s）
	MaxRetries int           // 可重试错误的最大重试次数；0=不重试，负值=使用默认 3

	// 默认采样参数（nil 表示不覆盖服务端默认；请求级 Request 可覆盖）。
	Temperature *float64 // 采样温度 0~2
	TopP        *float64 // 核采样 0~1
	MaxTokens   *int     // 生成 token 数上限
}

// NewOpenAICompatible 构造客户端并校验关键配置。
// 安全约束：APIKey 为空直接报错，杜绝"忘记注入密钥"的情况。
func NewOpenAICompatible(cfg Config) (*OpenAICompatible, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("llm: BaseURL 不能为空")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("llm: APIKey 不能为空")
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-3.5-turbo" // 兜底默认值
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	// 重试默认关闭（0=不重试），调用方按业务显式开启。
	// 说明：默认关闭更安全——重试会放大整体超时与上游压力，
	// llm-gateway 等对上游错误做状态映射的场景不应隐式重试。
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 3
	}

	transport := &retryableTransport{
		base:       http.DefaultTransport,
		maxRetries: cfg.MaxRetries,
	}

	return &OpenAICompatible{
		name:        cfg.Name,
		baseURL:     cfg.BaseURL,
		apiKey:      cfg.APIKey,
		model:       cfg.Model,
		timeout:     cfg.Timeout,
		maxRetries:  cfg.MaxRetries,
		temperature: cfg.Temperature,
		topP:        cfg.TopP,
		maxTokens:   cfg.MaxTokens,
		client:      &http.Client{Transport: transport},
	}, nil
}

// Name 返回供应商名称。
func (c *OpenAICompatible) Name() string {
	if c.name == "" {
		return "openai-compatible"
	}
	return c.name
}

// ---- 协议映射（schema ↔ OpenAI 协议）----

// openAIMessage OpenAI 协议中的单条消息。
type openAIMessage struct {
	Role             string           `json:"role"`
	Content          string           `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
}

// openAIToolCall OpenAI 协议中的工具调用指令。
type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// toOpenAIMessages 把 schema.Message 序列转为 OpenAI 协议消息。
// 字段一一对应（schema 设计时就按协议对齐），无需转换逻辑。
func toOpenAIMessages(msgs []schema.Message) []openAIMessage {
	out := make([]openAIMessage, 0, len(msgs))
	for _, m := range msgs {
		msg := openAIMessage{
			Role:             string(m.Role),
			Content:          m.Content,
			ReasoningContent: m.Reasoning,
			ToolCallID:       m.ToolCallID,
		}
		// assistant 消息可能携带工具调用指令，需一并转换
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, openAIToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: tc.Name, Arguments: string(tc.Arguments)},
			})
		}
		out = append(out, msg)
	}
	return out
}

// openAITool OpenAI 协议的 tools 元素：
//
//	{"type":"function","function":{"name":"...","description":"...","parameters":{...}}}
type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// toOpenAITools 把 schema.ToolSchema 转成 OpenAI 协议 tools 数组。
// schema 的设计刻意与协议对齐，转换只做一次包装。
func toOpenAITools(tools []schema.ToolSchema) []openAITool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAITool, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if len(params) == 0 {
			// 无参数声明的工具，给一个空对象占位，避免协议校验失败
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return out
}

// ---- 请求构造 ----

// chatCompletionRequest OpenAI 协议请求体。
type chatCompletionRequest struct {
	Model           string          `json:"model"`
	Messages        []openAIMessage `json:"messages"`
	Tools           []openAITool    `json:"tools,omitempty"`
	Stream          bool            `json:"stream"`
	StreamOptions   *streamOptions  `json:"stream_options,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	MaxTokens       *int            `json:"max_tokens,omitempty"`
	Thinking        *thinkingBody   `json:"thinking,omitempty"`         // DeepSeek V4 思考开关
	ReasoningEffort string          `json:"reasoning_effort,omitempty"` // 推理强度 low|high|max
}

// thinkingBody DeepSeek V4 思考模式开关体。
type thinkingBody struct {
	Type string `json:"type"` // enabled | disabled
}

// streamOptions OpenAI 协议 stream_options（流式附加选项）。
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// buildBody 构造协议请求体 JSON。
func (c *OpenAICompatible) buildBody(req *Request, stream bool) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = c.model
	}
	body := chatCompletionRequest{
		Model:    model,
		Messages: toOpenAIMessages(req.Messages),
		Tools:    toOpenAITools(req.Tools),
		Stream:   stream,
	}
	// 流式请求默认开启 include_usage：让上游在流末尾附带 usage 统计块，
	// 否则 DeepSeek 等厂商流式响应不回传 token 用量（非流式无此问题）。
	if stream {
		if req.StreamOptions != nil {
			body.StreamOptions = &streamOptions{IncludeUsage: req.StreamOptions.IncludeUsage}
		} else {
			body.StreamOptions = &streamOptions{IncludeUsage: true}
		}
	}
	// 采样参数优先级：请求级 > 客户端默认 > 不发（服务端默认）
	body.Temperature = firstOr(req.Temperature, c.temperature)
	body.TopP = firstOr(req.TopP, c.topP)
	body.MaxTokens = firstOr(req.MaxTokens, c.maxTokens)
	// 思考模式（DeepSeek V4）：显式配置时才下发，未配置沿用厂商默认（思考开启）。
	if req.Thinking != nil {
		t := "enabled"
		if !req.Thinking.Enabled {
			t = "disabled"
		}
		body.Thinking = &thinkingBody{Type: t}
		body.ReasoningEffort = req.Thinking.ReasoningEffort
	}
	return json.Marshal(body)
}

// firstOr 返回第一个非 nil 指针；都为空则返回 nil。
// 用于"请求参数优先、客户端默认兜底"的取值逻辑。
func firstOr[T any](req, cfg *T) *T {
	if req != nil {
		return req
	}
	return cfg
}

// ---- 非流式调用 ----

// chatCompletionResponse OpenAI 协议非流式响应体。
type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// Chat 非流式对话：发送请求、解析完整响应。
// 超时由 c.timeout 控制（通过 context.WithTimeout 包裹）。
func (c *OpenAICompatible) Chat(ctx context.Context, req *Request) (*Response, error) {
	body, err := c.buildBody(req, false)
	if err != nil {
		return nil, fmt.Errorf("llm: 构造请求体失败: %w", err)
	}

	// 非流式请求才设置整体超时；流式请求由调用方 ctx 控制。
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	data, status, err := c.do(ctx, body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, c.decodeError(status, data)
	}

	var out chatCompletionResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("llm: 解析响应失败: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("llm: 厂商返回错误 code=%s type=%s: %s", out.Error.Code, out.Error.Type, out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("llm: 响应中无 choices（模型返回空结果）")
	}

	choice := out.Choices[0]
	resp := &Response{
		Content:      choice.Message.Content,
		Reasoning:    choice.Message.ReasoningContent,
		FinishReason: choice.FinishReason,
		Usage: Usage{
			PromptTokens:     out.Usage.PromptTokens,
			CompletionTokens: out.Usage.CompletionTokens,
			TotalTokens:      out.Usage.TotalTokens,
		},
	}
	for _, tc := range choice.Message.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, schema.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: json.RawMessage(tc.Function.Arguments),
		})
	}
	return resp, nil
}

// ---- 流式调用 ----

// ChatStream 流式对话：发起 SSE 请求并返回迭代器。
// 注意：不做整体超时（会截断流），由调用方通过 ctx 控制生命周期。
func (c *OpenAICompatible) ChatStream(ctx context.Context, req *Request) (Stream, error) {
	body, err := c.buildBody(req, true)
	if err != nil {
		return nil, fmt.Errorf("llm: 构造请求体失败: %w", err)
	}

	httpReq, err := c.newRequest(ctx, body)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm: 流式请求失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, c.decodeError(resp.StatusCode, data)
	}

	return newSSEStream(resp), nil
}

// ---- 通用 HTTP 工具 ----

// newRequest 构造带鉴权的 POST 请求（GetBody 支持重试时重建 body）。
func (c *OpenAICompatible) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("llm: 构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	// 注入调用方经 context 附加的自定义请求头（如 X-User-Id），
	// 供网关做租户隔离/配额统计；与 URL 拼接的 baseURL 相互独立。
	for k, v := range headersFromContext(ctx) {
		req.Header.Set(k, v)
	}
	return req, nil
}

// do 发送请求并读取完整响应体（重试由 retryableTransport 负责）。
func (c *OpenAICompatible) do(ctx context.Context, body []byte) ([]byte, int, error) {
	req, err := c.newRequest(ctx, body)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("llm: 请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("llm: 读取响应失败: %w", err)
	}
	return data, resp.StatusCode, nil
}

// decodeError 把非 200 状态码解析为类型化错误（HTTPStatusError）。
// 调用方可用 errors.As 提取 Status，映射到自己的错误体系
// （llm-gateway 据此返回合适的 HTTP 状态码，见 P2-35）。
//
// 错误可读性：优先解析 OpenAI 标准 error JSON 的 message；解析失败时回退
// 解析网关/厂商的 {message} 包装（llm-gateway 的错误体是 {code,message,
// request_id}，非 OpenAI 格式）；仍不行则直接使用原始响应体（截断 512B）。
// 响应体为空时明确提示"空响应体"——避免出现"未知错误"吞掉上游的真实拒绝原因。
func (c *OpenAICompatible) decodeError(status int, data []byte) error {
	msg := errorMessageFromBody(data)
	if msg == "" {
		msg = "上游返回空响应体（无错误详情）"
	}
	return &HTTPStatusError{Status: status, Body: data, msg: fmt.Sprintf("llm: HTTP %d, %s", status, msg)}
}

// errorMessageFromBody 从错误响应体提取可读信息。按优先级：
//
//  1. OpenAI 标准 {"error":{"message":...}} → "code=.. type=..: message"；
//  2. 网关/厂商包装 {"message":...}（llm-gateway 的 {code,message,request_id} 等）；
//  3. 其它原始文本（截断 512B）。
//
// 响应体为空/空白时返回空串（调用方给出"空响应体"兜底提示）。
func errorMessageFromBody(data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &e); err == nil {
		if e.Error.Message != "" {
			return fmt.Sprintf("code=%s type=%s: %s", e.Error.Code, e.Error.Type, e.Error.Message)
		}
		if e.Message != "" {
			return e.Message
		}
	}
	if raw := bytes.TrimSpace(data); len(raw) > 0 {
		s := string(raw)
		if len(s) > 512 {
			s = s[:512] + "…(截断)"
		}
		return s
	}
	return ""
}

// HTTPStatusError 上游返回非 2xx 时的类型化错误。
// 相比普通 error，它额外携带 Status，供上层（llm-gateway）做
// "上游状态 → 自身错误码/HTTP 状态"的映射，避免把 429 误判成 500。
// Body 保留上游原始响应体，供上层提取具体失败原因（如 DeepSeek 的
// {"error":{"message":…}}），避免把错误详情截断在转换层。
type HTTPStatusError struct {
	Status int
	Body   []byte
	msg    string
}

// Error 实现 error 接口。
func (e *HTTPStatusError) Error() string { return e.msg }
