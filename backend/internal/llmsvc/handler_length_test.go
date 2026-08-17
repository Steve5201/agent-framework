package llmsvc

// llm-gateway 链路长度/透传回归测试。
//
// 排查背景：render_html 生成文档时，deepseek-v4 的完成参数总是"被截断"。
// 排查结论（2026-08）：截断不在本服务——全链路（llm-gateway 转发 / framework
// SSE 解析 / agent 拼接）均无截断逻辑；本测试固化两个回归点：
//  1. max_tokens 请求参数完整透传上游（若被吞，模型将受上游默认输出上限约束）；
//  2. 上游返回超长 tool_calls.arguments（数百 KB）时，llm-gateway 响应完整透传，
//     不截断（证明"参数被截断"不是本服务导致）。

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHandler_MaxTokensPassthrough 验证入站 max_tokens 原样转发上游。
func TestHandler_MaxTokensPassthrough(t *testing.T) {
	up := &mockUpstream{}
	up.set(0, `{"id":"cmpl-1","object":"chat.completion","model":"deepseek-v4-flash",`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "application/json")
	specs := []ModelSpec{
		{Name: "deepseek-v4-flash", BaseURL: up.serverURL(t), APIKey: "k", Enabled: true, IsDefault: true},
	}
	srv := newRegistryHandler(t, up, &fakeUsageStore{}, specs)

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],` +
		`"max_tokens":16384,"stream":false}`
	resp := doChat(t, srv.URL, body, "42")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	got := string(up.latestBody())
	if !strings.Contains(got, `"max_tokens":16384`) {
		t.Errorf("上游请求未透传 max_tokens: %s", got)
	}
}

// TestHandler_LongToolCallResponse_NoTruncation 验证上游返回超长工具参数时，
// llm-gateway 非流式响应完整透传（300KB+ arguments 不截断）。
func TestHandler_LongToolCallResponse_NoTruncation(t *testing.T) {
	// 构造 300KB 的 render_html arguments（html 为长文档）。
	html := "<html><body>" + strings.Repeat("<p>段落内容。</p>", 15000) + "</body></html>"
	args := `{"format":"html","title":"测试文档","html":"` + html + `"}`
	if len(args) < 300*1024 {
		t.Fatalf("测试数据过短: %d 字节", len(args))
	}

	upResp := map[string]any{
		"id": "cmpl-1", "object": "chat.completion", "model": "deepseek-v4-flash",
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []any{map[string]any{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": "render_html", "arguments": args},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
	}
	upBody, err := json.Marshal(upResp)
	if err != nil {
		t.Fatalf("构造上游响应失败: %v", err)
	}

	up := &mockUpstream{}
	up.set(0, string(upBody), "application/json")
	specs := []ModelSpec{
		{Name: "deepseek-v4-flash", BaseURL: up.serverURL(t), APIKey: "k", Enabled: true, IsDefault: true},
	}
	srv := newRegistryHandler(t, up, &fakeUsageStore{}, specs)

	req := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp := doChat(t, srv.URL, req, "42")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}

	var out struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(readBody(t, resp)), &out); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(out.Choices) != 1 || len(out.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("响应缺少 tool_calls: %+v", out.Choices)
	}
	got := out.Choices[0].Message.ToolCalls[0].Function.Arguments
	if got != args {
		t.Fatalf("工具参数透传不完整: 期望 %d 字节，实际 %d 字节", len(args), len(got))
	}
}

// TestHandler_RegistryMaxTokens 验证注册表配置 max_tokens 后，即使入站未显式
// 传 max_tokens，转发上游的请求体也会带上注册表默认值（修复 deepseek-v4
// 未设置 max_tokens 时被服务端默认 8192 截断的关键一环）。
func TestHandler_RegistryMaxTokens(t *testing.T) {
	up := &mockUpstream{}
	up.set(0, `{"id":"cmpl-1","object":"chat.completion","model":"deepseek-v4-flash",`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "application/json")
	specs := []ModelSpec{
		{Name: "deepseek-v4-flash", BaseURL: up.serverURL(t), APIKey: "k",
			Enabled: true, IsDefault: true, MaxTokens: 16384},
	}
	srv := newRegistryHandler(t, up, &fakeUsageStore{}, specs)

	// 入站请求故意不带 max_tokens，验证注册表默认值被注入。
	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp := doChat(t, srv.URL, body, "42")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	got := string(up.latestBody())
	if !strings.Contains(got, `"max_tokens":16384`) {
		t.Errorf("注册表 max_tokens 未注入上游请求: %s", got)
	}
	if strings.Contains(got, `"max_tokens":0`) {
		t.Errorf("注册表 max_tokens=0 被错误注入: %s", got)
	}
}
