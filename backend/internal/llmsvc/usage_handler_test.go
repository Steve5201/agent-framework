package llmsvc

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// doChatAsAgent 以某智能体域身份调用 /v1/chat/completions（注入 X-Agent-Id）。
func doChatAsAgent(t *testing.T, url, body, userID, agentID string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req.Header.Set(headerUserID, userID)
	}
	if agentID != "" {
		req.Header.Set(headerAgentID, agentID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do error = %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestAgentUsage_DaysBoundary days 参数：缺省 7、范围 1..90。
func TestAgentUsage_DaysBoundary(t *testing.T) {
	up := &mockUpstream{}
	srv := newTestHandler(t, up, &fakeUsageStore{}, HandlerConfig{Model: "m"})
	defer srv.Close()

	// 非法 days 一律 400
	for _, q := range []string{"0", "91", "abc", "-1"} {
		resp, err := http.Get(srv.URL + "/v1/usage/agents/math?days=" + q)
		if err != nil {
			t.Fatalf("GET days=%s: %v", q, err)
		}
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("days=%q 应 400, got %d, body=%s", q, resp.StatusCode, body)
		}
	}

	// 缺省 days → 200（默认 7 天）
	resp, err := http.Get(srv.URL + "/v1/usage/agents/math")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("缺省 days 应 200, got %d, body=%s", resp.StatusCode, readBody(t, resp))
	}
}

// TestAgentUsage_AggregatesPerAgent 用量按智能体域聚合（X-Agent-Id → agent_id）。
func TestAgentUsage_AggregatesPerAgent(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	srv := newTestHandler(t, up, usage, HandlerConfig{
		Model: "deepseek-v4-flash", PromptPricePer1M: 1.0, CompletionPricePer1M: 2.0,
	})
	defer srv.Close()
	up.set(0, `{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, "application/json")

	// math 域成功调用 2 次
	for i := 0; i < 2; i++ {
		resp := doChatAsAgent(t, srv.URL, `{"messages":[{"role":"user","content":"hi"}]}`, "1", "math")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("completion #%d status = %d, body = %s", i, resp.StatusCode, readBody(t, resp))
		}
	}

	// math 聚合：calls=2、total=30（10+5 per call）；cost = 2×(10×1.0+5×2.0)/1e6 = 4e-5
	var math struct {
		AgentID     string  `json:"agent_id"`
		Calls       int     `json:"calls"`
		TotalTokens int64   `json:"total_tokens"`
		CostUSD     float64 `json:"cost_usd"`
	}
	body := readBody(t, mustGet(t, srv.URL+"/v1/usage/agents/math?days=7"))
	if err := json.Unmarshal([]byte(body), &math); err != nil {
		t.Fatalf("usage 响应非法 JSON: %s", body)
	}
	if math.AgentID != "math" || math.Calls != 2 || math.TotalTokens != 30 {
		t.Fatalf("math 聚合异常: %+v (body=%s)", math, body)
	}
	if math.CostUSD < 0.00003 || math.CostUSD > 0.00005 {
		t.Fatalf("cost 应按价格计算, got %v", math.CostUSD)
	}

	// 其它域（tutor）无调用 → 零值
	var tutor struct {
		Calls int `json:"calls"`
	}
	body = readBody(t, mustGet(t, srv.URL+"/v1/usage/agents/tutor?days=7"))
	if err := json.Unmarshal([]byte(body), &tutor); err != nil {
		t.Fatalf("tutor usage 响应非法 JSON: %s", body)
	}
	if tutor.Calls != 0 {
		t.Fatalf("tutor 应无调用, got %+v", tutor)
	}
}

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}
