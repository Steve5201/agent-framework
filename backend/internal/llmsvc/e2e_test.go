package llmsvc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
)

// ---------------------------------------------------------------------------
// 全流程链路（E2E）测试
//
// 链路：真实 framework 客户端 → llm-gateway HTTP 端点 → 真实 OpenAICompatible
// 上游客户端 → mock 厂商 → 反向 SSE / JSON → 客户端解析。
//
// 与各环节单测的区别：E2E 同时覆盖两处协议转换（入站归一化 toLLMRequest +
// 出站构造 buildChatCompletionResponse / streamChunk）与 SSE 双端解析
// （framework sseStream + gateway stream）的整条闭环，用于排查
// "单测都过、串起来就挂"的协议断点。
// ---------------------------------------------------------------------------

// newE2EClient 构造指向 llm-gateway 的 framework 客户端（与 agent 端用法一致）。
// 注意 BaseURL 需带 /v1 前缀：framework 客户端在 BaseURL 后追加 /chat/completions
// 拼接协议端点（同 DeepSeek 的 https://api.deepseek.com/v1），而 llm-gateway
// 的端点挂在 /v1/chat/completions。
func newE2EClient(t *testing.T, gatewayURL string) *llm.OpenAICompatible {
	t.Helper()
	c, err := llm.NewOpenAICompatible(llm.Config{
		BaseURL:    gatewayURL + "/v1",
		APIKey:     "e2e-client-key",
		Model:      "deepseek-v4-flash",
		MaxRetries: 0,
	})
	if err != nil {
		t.Fatalf("newE2EClient error = %v", err)
	}
	return c
}

// e2eCtx 注入 X-User-Id（gateway 入站鉴权要求）的上下文。
func e2eCtx(t *testing.T, userID string) context.Context {
	t.Helper()
	return llm.WithHeader(context.Background(), headerUserID, userID)
}

// collectStream 消费完整条流，返回拼接结果与累计 usage。
func collectStream(t *testing.T, st llm.Stream) (content, reasoning string, toolCalls []llm.ToolCallDelta, usage *llm.Usage) {
	t.Helper()
	defer st.Close()
	for {
		ev, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		content += ev.Content
		reasoning += ev.Reasoning
		toolCalls = append(toolCalls, ev.ToolCalls...)
		if ev.Usage != nil {
			usage = ev.Usage
		}
	}
	return
}

// sseData 把任意结构序列化为一条 SSE data 事件（构造上游 mock 响应用）。
func sseData(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return "data: " + string(b) + "\n\n"
}

// sseChunk 构造一个 OpenAI 流式块（choices 单元素，可选 finish_reason）。
func sseChunk(delta map[string]any, finish string) string {
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	return sseData(map[string]any{"choices": []any{choice}})
}

// ---------------------------------------------------------------------------
// 场景 1：流式全链路（思考 + 文本 + usage）
// ---------------------------------------------------------------------------

func TestE2E_Stream_TextReasoningUsage(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	srv := newTestHandler(t, up, usage, HandlerConfig{
		Model:            "deepseek-v4-flash",
		PromptPricePer1M: 0.27, CompletionPricePer1M: 1.10,
	})

	// 上游按 OpenAI SSE 协议返回：role → 思考增量 → 文本增量 → finish → usage → [DONE]。
	body := sseChunk(map[string]any{"role": "assistant"}, "") +
		sseChunk(map[string]any{"reasoning_content": "先想一步"}, "") +
		sseChunk(map[string]any{"content": "你"}, "") +
		sseChunk(map[string]any{"content": "好"}, "") +
		sseChunk(map[string]any{}, "stop") +
		sseData(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}) +
		"data: [DONE]\n\n"
	up.set(0, body, "text/event-stream")

	client := newE2EClient(t, srv.URL)
	st, err := client.ChatStream(e2eCtx(t, "42"), &llm.Request{
		Model:    "deepseek-v4-flash",
		Messages: []schema.Message{{Role: schema.Role("user"), Content: "你好"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error = %v", err)
	}
	content, reasoning, _, gotUsage := collectStream(t, st)

	if content != "你好" {
		t.Errorf("内容拼接 = %q, want 你好", content)
	}
	if reasoning != "先想一步" {
		t.Errorf("思考拼接 = %q, want 先想一步", reasoning)
	}
	if gotUsage == nil || gotUsage.TotalTokens != 15 {
		t.Errorf("usage = %+v, want total=15", gotUsage)
	}

	// gateway 侧：流式成功落一条带 token 的用量记录
	if usage.count() != 1 {
		t.Fatalf("应写入 1 条用量, got %d", usage.count())
	}
	last := usage.last()
	if !last.Success || !last.Stream || last.TotalTokens != 15 || last.UserID != 42 {
		t.Errorf("用量记录异常: %+v", last)
	}
	if last.CostUSD != CostUSD(10, 5, 0.27, 1.10) {
		t.Errorf("成本异常: %v", last.CostUSD)
	}

	// gateway 转发的上游请求应开启 include_usage（流式默认），否则上游不回 usage
	if reqBody := string(up.latestBody()); !strings.Contains(reqBody, "include_usage") {
		t.Errorf("上游请求缺少 stream_options.include_usage: %s", reqBody)
	}
}

// ---------------------------------------------------------------------------
// 场景 2：流式工具调用全链路（分片参数按 index 拼接）
// ---------------------------------------------------------------------------

func TestE2E_Stream_ToolCalls(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	srv := newTestHandler(t, up, usage, HandlerConfig{Model: "deepseek-v4-flash"})

	// 上游：role → 工具名/id 首片 → 参数两片 → finish → [DONE]。
	body := sseChunk(map[string]any{"role": "assistant"}, "") +
		sseChunk(map[string]any{"tool_calls": []any{
			map[string]any{"index": 0, "id": "call_0", "function": map[string]any{"name": "calculator", "arguments": ""}},
		}}, "") +
		sseChunk(map[string]any{"tool_calls": []any{
			map[string]any{"index": 0, "function": map[string]any{"arguments": `{"a":1,`}},
		}}, "") +
		sseChunk(map[string]any{"tool_calls": []any{
			map[string]any{"index": 0, "function": map[string]any{"arguments": `"b":2}`}},
		}}, "") +
		sseChunk(map[string]any{}, "tool_calls") +
		"data: [DONE]\n\n"
	up.set(0, body, "text/event-stream")

	client := newE2EClient(t, srv.URL)
	st, err := client.ChatStream(e2eCtx(t, "9"), &llm.Request{
		Model:    "deepseek-v4-flash",
		Messages: []schema.Message{{Role: schema.Role("user"), Content: "计算 1+2"}},
		Tools: []schema.ToolSchema{{
			Name:        "calculator",
			Description: "四则运算",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}}}`),
		}},
	})
	if err != nil {
		t.Fatalf("ChatStream error = %v", err)
	}
	_, _, deltas, _ := collectStream(t, st)

	if len(deltas) == 0 {
		t.Fatal("未收到任何工具调用增量")
	}
	var name, id, args string
	for _, d := range deltas {
		if d.Name != "" {
			name = d.Name
		}
		if d.ID != "" {
			id = d.ID
		}
		args += d.Arguments
	}
	if name != "calculator" || id != "call_0" {
		t.Errorf("工具元信息异常: name=%q id=%q", name, id)
	}
	if args != `{"a":1,"b":2}` {
		t.Errorf("参数拼接 = %q, want %q", args, `{"a":1,"b":2}`)
	}

	// gateway 转发的上游请求应携带 tools 声明（协议转换往返无损）
	if reqBody := string(up.latestBody()); !strings.Contains(reqBody, "calculator") {
		t.Errorf("上游请求缺少 tools: %s", reqBody)
	}
}

// ---------------------------------------------------------------------------
// 场景 3：非流式全链路（文本 + 思考 + 工具调用 + usage）
// ---------------------------------------------------------------------------

func TestE2E_NonStream_ToolCallAndText(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	srv := newTestHandler(t, up, usage, HandlerConfig{
		Model: "deepseek-v4-flash", PromptPricePer1M: 0.27, CompletionPricePer1M: 1.10,
	})
	up.set(0, `{"id":"cmpl-e2e","object":"chat.completion","model":"deepseek-v4-flash",`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":"结果 3","reasoning_content":"先算再答",`+
		`"tool_calls":[{"id":"call_0","type":"function","function":{"name":"calculator","arguments":"{\"a\":1,\"b\":2}"}}]},`+
		`"finish_reason":"tool_calls"}],`+
		`"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}}`, "application/json")

	client := newE2EClient(t, srv.URL)
	resp, err := client.Chat(e2eCtx(t, "7"), &llm.Request{
		Model:    "deepseek-v4-flash",
		Messages: []schema.Message{{Role: schema.Role("user"), Content: "计算 1+2"}},
		Tools: []schema.ToolSchema{{
			Name: "calculator", Description: "四则运算",
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		}},
	})
	if err != nil {
		t.Fatalf("Chat error = %v", err)
	}
	if resp.Content != "结果 3" || resp.Reasoning != "先算再答" {
		t.Errorf("内容异常: content=%q reasoning=%q", resp.Content, resp.Reasoning)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", resp.FinishReason)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "calculator" ||
		string(resp.ToolCalls[0].Arguments) != `{"a":1,"b":2}` {
		t.Errorf("tool_calls 异常: %+v", resp.ToolCalls)
	}
	if resp.Usage.TotalTokens != 12 {
		t.Errorf("usage = %+v", resp.Usage)
	}

	last := usage.last()
	if last == nil || !last.Success || last.TotalTokens != 12 || last.CostUSD != CostUSD(8, 4, 0.27, 1.10) {
		t.Errorf("用量记录异常: %+v", last)
	}
}

// TestE2E_Stream_PreservesFinishReason 验证 gateway 流式收尾时透传上游真实的
// finish_reason（如 tool_calls）而非硬编码 stop。OpenAI 兼容客户端依赖
// finish_reason=tool_calls 决定是否执行工具——若被改写为 stop，工具链路断。
func TestE2E_Stream_PreservesFinishReason(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	srv := newTestHandler(t, up, usage, HandlerConfig{Model: "deepseek-v4-flash"})

	// 上游：工具调用增量后以 tool_calls 收尾。
	body := sseChunk(map[string]any{"role": "assistant"}, "") +
		sseChunk(map[string]any{"tool_calls": []any{
			map[string]any{"index": 0, "id": "call_0", "function": map[string]any{"name": "calculator", "arguments": `{"a":1,"b":2}`}},
		}}, "") +
		sseChunk(map[string]any{}, "tool_calls") +
		"data: [DONE]\n\n"
	up.set(0, body, "text/event-stream")

	resp := doChat(t, srv.URL, `{"messages":[{"role":"user","content":"计算 1+2"}],"stream":true}`, "1")
	out := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, out)
	}
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Errorf("gateway 未透传上游 finish_reason=tool_calls，实际输出: %s", out)
	}
}

// ---------------------------------------------------------------------------
// 场景 4：上游错误跨链映射（429 流式 / 非流式）
// ---------------------------------------------------------------------------

func TestE2E_Stream_Upstream429(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	srv := newTestHandler(t, up, usage, HandlerConfig{Model: "deepseek-v4-flash"})
	up.set(http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`, "application/json")

	client := newE2EClient(t, srv.URL)
	_, err := client.ChatStream(e2eCtx(t, "1"), &llm.Request{
		Model:    "deepseek-v4-flash",
		Messages: []schema.Message{{Role: schema.Role("user"), Content: "hi"}},
	})
	if err == nil {
		t.Fatal("上游 429 应返回错误")
	}
	var he *llm.HTTPStatusError
	if !errors.As(err, &he) {
		t.Fatalf("错误应类型化为 HTTPStatusError, got %T: %v", err, err)
	}
	if he.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", he.Status)
	}
	// gateway 映射后的可读文案应透传到客户端
	if !strings.Contains(err.Error(), "上游模型服务限流") {
		t.Errorf("错误文案缺少网关映射信息: %v", err)
	}
	// 失败也要落一条用量（token 未知记 0）
	if usage.count() != 1 || usage.last().Success {
		t.Errorf("用量记录异常: %+v", usage.last())
	}
}

func TestE2E_NonStream_Upstream429(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	srv := newTestHandler(t, up, usage, HandlerConfig{Model: "deepseek-v4-flash"})
	up.set(http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`, "application/json")

	client := newE2EClient(t, srv.URL)
	_, err := client.Chat(e2eCtx(t, "1"), &llm.Request{
		Model:    "deepseek-v4-flash",
		Messages: []schema.Message{{Role: schema.Role("user"), Content: "hi"}},
	})
	if err == nil {
		t.Fatal("上游 429 应返回错误")
	}
	var he *llm.HTTPStatusError
	if !errors.As(err, &he) {
		t.Fatalf("错误应类型化为 HTTPStatusError, got %T: %v", err, err)
	}
	if he.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", he.Status)
	}
	if usage.count() != 1 || usage.last().Success {
		t.Errorf("用量记录异常: %+v", usage.last())
	}
}
