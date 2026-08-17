package llmsvc

import (
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// newRegistryHandler 构造带注册表的 handler（注册表条目上游指向 mockUpstream）。
func newRegistryHandler(t *testing.T, up *mockUpstream, usage *fakeUsageStore, specs []ModelSpec) *httptest.Server {
	t.Helper()
	reg := NewRegistry()
	reg.Reload(specs, zap.NewNop())
	srv := newTestHandler(t, up, usage, HandlerConfig{
		Registry:             reg,
		PromptPricePer1M:     0.27,
		CompletionPricePer1M: 1.10,
	})
	return srv
}

func TestNoThinking_StripParams(t *testing.T) {
	up := &mockUpstream{}
	up.set(0, `{"id":"cmpl-1","object":"chat.completion","model":"glm-5.2",`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, "application/json")
	specs := []ModelSpec{
		{Name: "glm-5.2", BaseURL: up.serverURL(t), APIKey: "k", NoThinking: true, Enabled: true, IsDefault: true},
	}
	srv := newRegistryHandler(t, up, &fakeUsageStore{}, specs)

	// 会话配置开启了思考模式：请求携带 thinking + reasoning_effort。
	body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],` +
		`"thinking":{"type":"enabled"},"reasoning_effort":"medium","stream":false}`
	resp := doChat(t, srv.URL, body, "42")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	got := string(up.latestBody())
	if strings.Contains(got, "thinking") {
		t.Errorf("no_thinking 模型上游请求仍含 thinking 字段: %s", got)
	}
	if strings.Contains(got, "reasoning_effort") {
		t.Errorf("no_thinking 模型上游请求仍含 reasoning_effort 字段: %s", got)
	}
}

func TestNoThinking_KeepParams(t *testing.T) {
	up := &mockUpstream{}
	up.set(0, `{"id":"cmpl-1","object":"chat.completion","model":"deepseek-v4-flash",`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, "application/json")
	specs := []ModelSpec{
		{Name: "deepseek-v4-flash", BaseURL: up.serverURL(t), APIKey: "k", NoThinking: false, Enabled: true, IsDefault: true},
	}
	srv := newRegistryHandler(t, up, &fakeUsageStore{}, specs)

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],` +
		`"thinking":{"type":"enabled"},"reasoning_effort":"medium","stream":false}`
	resp := doChat(t, srv.URL, body, "42")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	got := string(up.latestBody())
	if !strings.Contains(got, "thinking") || !strings.Contains(got, "reasoning_effort") {
		t.Errorf("普通模型应透传 thinking/reasoning_effort，实际: %s", got)
	}
}
