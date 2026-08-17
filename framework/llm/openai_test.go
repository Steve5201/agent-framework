package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/schema"
)

// newTestClient 构造指向 httptest 服务器的客户端，便于验证协议交互。
func newTestClient(t *testing.T, h http.HandlerFunc) *OpenAICompatible {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	c, err := NewOpenAICompatible(Config{
		Name: "test", BaseURL: ts.URL, APIKey: "test-key", Model: "test-model",
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatible error = %v", err)
	}
	return c
}

// TestChat_Reasoning 验证思考内容往返：
// 请求侧 assistant 消息的 Reasoning 序列化为 reasoning_content 发送；
// 响应侧 reasoning_content 解析回 Reasoning。
func TestChat_Reasoning(t *testing.T) {
	var gotBody map[string]any

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"choices":[{
				"message":{"role":"assistant","content":"最终回答","reasoning_content":"先想一步"},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`))
	})

	resp, err := c.Chat(context.Background(), &Request{
		Model: "test-model",
		Messages: []schema.Message{
			{Role: schema.RoleAssistant, Content: "", Reasoning: "历史思考",
				ToolCalls: []schema.ToolCall{{ID: "call_1", Name: "t", Arguments: json.RawMessage(`{}`)}}},
			{Role: schema.RoleTool, ToolCallID: "call_1", Content: "ok"},
		},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	// 请求体：assistant 消息带 reasoning_content（工具轮回传必需）
	msgs := gotBody["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["reasoning_content"] != "历史思考" {
		t.Errorf("请求应带 reasoning_content, got %v", first)
	}

	// 响应解析：思考内容与回答分开返回
	if resp.Reasoning != "先想一步" {
		t.Errorf("Reasoning = %q, want 先想一步", resp.Reasoning)
	}
	if resp.Content != "最终回答" {
		t.Errorf("Content = %q", resp.Content)
	}
}

// TestChat_NonStream 验证非流式调用的完整协议交互。
func TestChat_NonStream(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"choices":[{
				"message":{"role":"assistant","content":"你好，我是AI"},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
		}`))
	})

	resp, err := c.Chat(context.Background(), &Request{
		Model:    "test-model",
		Messages: []schema.Message{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	// 路径、鉴权头正确
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("auth = %q, want Bearer test-key", gotAuth)
	}

	// 请求体：model / stream=false / 消息角色正确
	if gotBody["model"] != "test-model" {
		t.Errorf("body.model = %v", gotBody["model"])
	}
	if gotBody["stream"] != false {
		t.Errorf("body.stream = %v, want false", gotBody["stream"])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("body.messages = %v", gotBody["messages"])
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "hi" {
		t.Errorf("message = %v", first)
	}

	// 响应解析正确
	if resp.Content != "你好，我是AI" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("Usage = %+v", resp.Usage)
	}
}

// TestBuildBody_StreamOptions 验证流式请求默认开启 include_usage：
// DeepSeek 等厂商流式默认不回传 usage，必须在请求体声明
// stream_options.include_usage 才能拿到流末 usage 统计块。
func TestBuildBody_StreamOptions(t *testing.T) {
	c := &OpenAICompatible{}

	req := &Request{Model: "m", Messages: []schema.Message{{Role: schema.RoleUser, Content: "hi"}}}

	// 1) 非流式：不携带 stream_options
	b, err := c.buildBody(req, false)
	if err != nil {
		t.Fatalf("buildBody error = %v", err)
	}
	if strings.Contains(string(b), "stream_options") {
		t.Errorf("非流式请求不应带 stream_options: %s", b)
	}

	// 2) 流式 + 未显式设置：默认 include_usage=true
	b, err = c.buildBody(req, true)
	if err != nil {
		t.Fatalf("buildBody error = %v", err)
	}
	var body struct {
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("unmarshal error = %v, body = %s", err, b)
	}
	if !body.StreamOptions.IncludeUsage {
		t.Errorf("流式请求默认应 include_usage=true: %s", b)
	}

	// 3) 流式 + 显式关闭：尊重调用方设置
	req.StreamOptions = &StreamOptions{IncludeUsage: false}
	b, err = c.buildBody(req, true)
	if err != nil {
		t.Fatalf("buildBody error = %v", err)
	}
	var body2 struct {
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(b, &body2); err != nil {
		t.Fatalf("unmarshal error = %v, body = %s", err, b)
	}
	if body2.StreamOptions.IncludeUsage {
		t.Errorf("显式关闭后仍 include_usage=true: %s", b)
	}
}

// TestBuildBody_Thinking 思考模式透传：thinking.type + reasoning_effort。
func TestBuildBody_Thinking(t *testing.T) {
	c := &OpenAICompatible{}

	// 1) 未配置：不发 thinking / reasoning_effort（沿用厂商默认）
	req := &Request{Model: "m", Messages: []schema.Message{{Role: schema.RoleUser, Content: "hi"}}}
	b, err := c.buildBody(req, false)
	if err != nil {
		t.Fatalf("buildBody error = %v", err)
	}
	if strings.Contains(string(b), "thinking") || strings.Contains(string(b), "reasoning_effort") {
		t.Errorf("未配置思考模式不应发 thinking/reasoning_effort: %s", b)
	}

	// 2) 关闭思考 + effort=high
	req.Thinking = &ThinkingConfig{Enabled: false, ReasoningEffort: "high"}
	b, err = c.buildBody(req, false)
	if err != nil {
		t.Fatalf("buildBody error = %v", err)
	}
	var body struct {
		Thinking        *struct{ Type string } `json:"thinking"`
		ReasoningEffort string                 `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("unmarshal error = %v, body = %s", err, b)
	}
	if body.Thinking == nil || body.Thinking.Type != "disabled" {
		t.Errorf("关闭思考应 thinking.type=disabled: %s", b)
	}
	if body.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q, want high: %s", body.ReasoningEffort, b)
	}

	// 3) 开启思考 + effort 为空：只发 type=enabled，不发 effort
	req.Thinking = &ThinkingConfig{Enabled: true}
	b, err = c.buildBody(req, false)
	if err != nil {
		t.Fatalf("buildBody error = %v", err)
	}
	var body3 struct {
		Thinking        *struct{ Type string } `json:"thinking"`
		ReasoningEffort string                 `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(b, &body3); err != nil {
		t.Fatalf("unmarshal error = %v, body = %s", err, b)
	}
	if body3.Thinking == nil || body3.Thinking.Type != "enabled" {
		t.Errorf("开启思考应 thinking.type=enabled: %s", b)
	}
	if body3.ReasoningEffort != "" {
		t.Errorf("effort 为空不应下发: %s", b)
	}
}

func TestChat_Tools(t *testing.T) {
	var gotBody map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	})

	_, err := c.Chat(context.Background(), &Request{
		Messages: []schema.Message{{Role: schema.RoleUser, Content: "1+1?"}},
		Tools: []schema.ToolSchema{{
			Name:        "calculator",
			Description: "加法计算器",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"}}}`),
		}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("body.tools = %v", gotBody["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Errorf("tool.type = %v", tool["type"])
	}
	fn, ok := tool["function"].(map[string]any)
	if !ok || fn["name"] != "calculator" {
		t.Errorf("tool.function = %v", tool["function"])
	}
}

// TestChat_EmptyChoices 验证无 choices 时返回明确错误。
func TestChat_EmptyChoices(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[]}`))
	})
	if _, err := c.Chat(context.Background(), &Request{}); err == nil {
		t.Error("空 choices 应返回错误")
	} else if !strings.Contains(err.Error(), "无 choices") {
		t.Errorf("错误信息不明确: %v", err)
	}
}

// TestChat_HTTPError 验证厂商错误被解码为可读错误。
func TestChat_HTTPError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error","code":"400"}}`))
	})
	if _, err := c.Chat(context.Background(), &Request{}); err == nil {
		t.Fatal("400 应返回错误")
	} else if !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("错误信息不含状态码: %v", err)
	}
}

// TestDecodeError_MessageExtraction 验证错误体提取优先级：
// OpenAI error 格式 > 网关 {message} 包装 > 原始文本 > 空体兜底。
func TestDecodeError_MessageExtraction(t *testing.T) {
	c := &OpenAICompatible{}

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "空响应体给出明确兜底提示（不再'未知错误'）",
			body: "",
			want: "上游返回空响应体（无错误详情）",
		},
		{
			name: "空白响应体同样兜底",
			body: "   \n",
			want: "上游返回空响应体（无错误详情）",
		},
		{
			name: "OpenAI 标准 error 格式",
			body: `{"error":{"message":"Model Not Exist","type":"invalid_request_error","code":"400"}}`,
			want: "Model Not Exist",
		},
		{
			name: "网关 {message} 包装（llm-gateway 错误体）",
			body: `{"code":40001,"message":"上游模型服务返回错误: x","request_id":"r1"}`,
			want: "上游模型服务返回错误: x",
		},
		{
			name: "非 JSON 原始文本回退",
			body: `Bad Request`,
			want: "Bad Request",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.decodeError(http.StatusBadRequest, []byte(tc.body))
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误消息不含 %q: %v", tc.want, err)
			}
		})
	}
}
