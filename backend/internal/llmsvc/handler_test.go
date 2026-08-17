package llmsvc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Steve5201/agent-backend/internal/auth"
	"github.com/Steve5201/agent-framework/llm"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// fakeUsageStore 内存用量存储：可断言写入记录、预设本月累计。
type fakeUsageStore struct {
	mu         sync.Mutex
	logs       []*UsageLog
	monthTotal int64
	monthErr   error
	// Overview 测试注入
	overview     *UsageOverview
	overviewDays int // 记录最近一次 Overview 请求的窗口（断言透传）
}

// Overview 返回注入的总览（UsageAdmin 测试用），并记录请求窗口。
func (f *fakeUsageStore) Overview(_ context.Context, days int) (*UsageOverview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overviewDays = days
	if f.overview != nil {
		cp := *f.overview
		return &cp, nil
	}
	return &UsageOverview{}, nil
}

func (f *fakeUsageStore) LogUsage(_ context.Context, l *UsageLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, l)
	return nil
}

func (f *fakeUsageStore) MonthTotalTokens(_ context.Context, _ int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.monthTotal, f.monthErr
}

// AgentTotals 内存聚合：按 agent_id 汇总 fake 中已记录的成功调用。
func (f *fakeUsageStore) AgentTotals(_ context.Context, agentID string, _ int) (*AgentUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := &AgentUsage{AgentID: agentID}
	for _, l := range f.logs {
		if l.AgentID != agentID || !l.Success {
			continue
		}
		u.Calls++
		u.PromptTokens += int64(l.PromptTokens)
		u.CompletionTokens += int64(l.CompletionTokens)
		u.TotalTokens += int64(l.TotalTokens)
		u.CostUSD += l.CostUSD
	}
	return u, nil
}

func (f *fakeUsageStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.logs)
}

// last 返回最后一条用量记录。
func (f *fakeUsageStore) last() *UsageLog {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.logs) == 0 {
		return nil
	}
	return f.logs[len(f.logs)-1]
}

// mockUpstream 模拟上游大模型厂商（记录请求、可配置返回/延迟）。
type mockUpstream struct {
	mu          sync.Mutex
	status      int
	body        string
	contentType string
	sleep       time.Duration
	srv         *httptest.Server

	lastBody   []byte
	reqCount   int
	authHeader string
}

func (m *mockUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.lastBody = body
	m.reqCount++
	m.authHeader = r.Header.Get("Authorization")
	st, respBody, ct, sl := m.status, m.body, m.contentType, m.sleep
	m.mu.Unlock()

	if sl > 0 {
		time.Sleep(sl)
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	if st != 0 {
		w.WriteHeader(st)
		_, _ = w.Write([]byte(`{"error":{"message":"mock error","code":"err","type":"mock"}}`))
		return
	}
	_, _ = w.Write([]byte(respBody))
}

// set 并发安全地改写 mock 返回。
func (m *mockUpstream) set(status int, body, contentType string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status, m.body, m.contentType = status, body, contentType
}

// newTestHandler 组装：httptest mock 上游 + OpenAICompatible 客户端 + handler。
func newTestHandler(t *testing.T, upstream *mockUpstream, usage *fakeUsageStore, cfg HandlerConfig) *httptest.Server {
	t.Helper()
	if cfg.Provider == nil {
		provider, err := llm.NewOpenAICompatible(llm.Config{
			BaseURL:    upstream.serverURL(t),
			APIKey:     "test-key",
			Model:      "deepseek-v4-flash",
			Timeout:    2 * time.Second,
			MaxRetries: 0,
		})
		if err != nil {
			t.Fatalf("llm.NewOpenAICompatible error = %v", err)
		}
		cfg.Provider = provider
	}
	cfg.Usage = usage
	cfg.Log = zap.NewNop()
	h := NewHandler(cfg)
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// doChat 发送一次 /v1/chat/completions 请求。
func doChat(t *testing.T, url, body, userID string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req.Header.Set(headerUserID, userID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do error = %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// readBody 读取响应体并转为字符串。
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body error = %v", err)
	}
	return string(b)
}

// decodeErrorBody 解析统一错误体 {code, message, request_id}。
func decodeErrorBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("错误体不是 JSON: %q", body)
	}
	return m
}

// ---------------------------------------------------------------------------
// mockUpstream 辅助：httptest.Server 包装
// ---------------------------------------------------------------------------

func (m *mockUpstream) serverURL(t *testing.T) string {
	t.Helper()
	if m.srv == nil {
		m.srv = httptest.NewServer(m)
		t.Cleanup(m.srv.Close)
	}
	return m.srv.URL
}

func (m *mockUpstream) requests() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reqCount
}

func (m *mockUpstream) latestBody() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.lastBody...)
}

func (m *mockUpstream) latestAuth() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.authHeader
}

// ---------------------------------------------------------------------------
// 非流式（P2-31）
// ---------------------------------------------------------------------------

func TestCompletions_NonStream_Success(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	srv := newTestHandler(t, up, usage, HandlerConfig{
		Model:            "deepseek-v4-flash",
		PromptPricePer1M: 0.27, CompletionPricePer1M: 1.10,
	})

	// mock 上游：标准 OpenAI 非流式响应
	up.set(0, `{"id":"cmpl-1","object":"chat.completion","model":"deepseek-v4-flash",`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":"你好，我是助手"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":15,"completion_tokens":8,"total_tokens":23}}`, "application/json")

	resp := doChat(t, srv.URL, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"你好"}],"stream":false}`, "42")
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var out chatCompletionResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("响应不是合法 chatCompletionResponse: %v", err)
	}
	if out.Object != "chat.completion" || len(out.Choices) != 1 {
		t.Errorf("响应结构异常: %+v", out)
	}
	if got := out.Choices[0].Message.Content; got != "你好，我是助手" {
		t.Errorf("content = %q", got)
	}
	if out.Usage.TotalTokens != 23 {
		t.Errorf("usage = %+v", out.Usage)
	}

	// 上游收到的请求应含 model/messages 与鉴权头
	reqBody := string(up.latestBody())
	if !strings.Contains(reqBody, `"deepseek-v4-flash"`) || !strings.Contains(reqBody, `"你好"`) {
		t.Errorf("上游收到的请求体异常: %s", reqBody)
	}
	if up.latestAuth() != "Bearer test-key" {
		t.Errorf("上游鉴权头异常: %q", up.latestAuth())
	}

	// 用量落库（成功一条）
	if usage.count() != 1 {
		t.Fatalf("应写入 1 条用量，got %d", usage.count())
	}
	last := usage.last()
	if last.UserID != 42 || last.TotalTokens != 23 || !last.Success || last.Stream {
		t.Errorf("用量记录异常: %+v", last)
	}
	if last.CostUSD != CostUSD(15, 8, 0.27, 1.10) {
		t.Errorf("成本计算异常: %f", last.CostUSD)
	}
}

// ---------------------------------------------------------------------------
// 流式 SSE（P2-32）
// ---------------------------------------------------------------------------

func TestCompletions_Stream_Success(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	srv := newTestHandler(t, up, usage, HandlerConfig{Model: "deepseek-v4-flash"})

	sseBody := "data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hel\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	up.set(0, sseBody, "text/event-stream")

	resp := doChat(t, srv.URL, `{"messages":[{"role":"user","content":"hi"}],"stream":true}`, "7")
	body := readBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	// 应包含 role 首块、增量、收尾与 [DONE]，且顺序正确
	for _, want := range []string{`"role":"assistant"`, `"content":"Hel"`, `"content":"lo"`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Errorf("SSE 输出缺少 %q，实际输出: %s", want, body)
		}
	}
	if strings.Index(body, "Hel") > strings.Index(body, "lo") {
		t.Error("增量块顺序异常：Hel 应在 lo 之前")
	}

	// 成功流式用量一条
	if usage.count() != 1 || !usage.last().Success || !usage.last().Stream {
		t.Errorf("用量记录异常: %+v", usage.last())
	}
}

// TestCompletions_Stream_UsageChunk 验证上游附带 usage 时，llm-gateway
// 在 [DONE] 前转发 usage 块（choices 为空）——agent 侧据此统计流式 token。
func TestCompletions_Stream_UsageChunk(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	srv := newTestHandler(t, up, usage, HandlerConfig{Model: "deepseek-v4-flash"})

	sseBody := "data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n" +
		"data: [DONE]\n\n"
	up.set(0, sseBody, "text/event-stream")

	resp := doChat(t, srv.URL, `{"messages":[{"role":"user","content":"hi"}],"stream":true}`, "8")
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	// usage 块必须转发且在 [DONE] 前（客户端读到 [DONE] 即结束）
	want := `"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`
	if !strings.Contains(body, want) {
		t.Errorf("输出缺少 usage 块 %q，实际输出: %s", want, body)
	}
	iUsage := strings.Index(body, want)
	iDone := strings.Index(body, "data: [DONE]")
	if iUsage < 0 || iDone < 0 || iUsage > iDone {
		t.Errorf("usage 块应在 [DONE] 之前（usage=%d done=%d）", iUsage, iDone)
	}

	// 用量落库仍正确（含累计 token）
	if usage.count() != 1 || usage.last().TotalTokens != 15 {
		t.Errorf("用量记录异常: %+v", usage.last())
	}
}

// ---------------------------------------------------------------------------
// 限流与配额（P2-34）
// ---------------------------------------------------------------------------

func TestCompletions_RateLimited(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	// 速率 1/s、突发 1：第二请求必被限流
	srv := newTestHandler(t, up, usage, HandlerConfig{
		Model: "deepseek-v4-flash", RequestRate: 1, RequestBurst: 1,
	})
	up.set(0, `{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "application/json")

	body := `{"messages":[{"role":"user","content":"hi"}]}`
	if resp := doChat(t, srv.URL, body, "1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("首请求应成功, status = %d", resp.StatusCode)
	}
	resp := doChat(t, srv.URL, body, "1")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("第二请求应 429, status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	errBody := decodeErrorBody(t, readBody(t, resp))
	if errBody["code"] != float64(42901) { // BizResourceExhausted
		t.Errorf("错误码异常: %v", errBody["code"])
	}
}

func TestCompletions_QuotaExceeded(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{monthTotal: 500}
	srv := newTestHandler(t, up, usage, HandlerConfig{
		Model: "deepseek-v4-flash", TokenQuotaMonth: 100,
	})

	resp := doChat(t, srv.URL, `{"messages":[{"role":"user","content":"hi"}]}`, "3")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("配额用尽应 429, status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	if up.requests() != 0 {
		t.Error("配额拒绝不应打到上游")
	}
}

func TestCompletions_UserRateIsolation(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	srv := newTestHandler(t, up, usage, HandlerConfig{
		Model: "deepseek-v4-flash", RequestRate: 1, RequestBurst: 1,
	})
	up.set(0, `{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "application/json")

	body := `{"messages":[{"role":"user","content":"hi"}]}`
	if resp := doChat(t, srv.URL, body, "1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("用户 1 首请求应成功")
	}
	// 用户 2 不受用户 1 限流影响
	if resp := doChat(t, srv.URL, body, "2"); resp.StatusCode != http.StatusOK {
		t.Fatalf("用户 2 请求应成功（限流桶按用户隔离）")
	}
	// 用户 1 再请求被限流
	if resp := doChat(t, srv.URL, body, "1"); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("用户 1 第二请求应 429")
	}
}

// ---------------------------------------------------------------------------
// 入参校验
// ---------------------------------------------------------------------------

func TestCompletions_MissingUserID(t *testing.T) {
	up := &mockUpstream{}
	srv := newTestHandler(t, up, &fakeUsageStore{}, HandlerConfig{Model: "m"})

	resp := doChat(t, srv.URL, `{"messages":[{"role":"user","content":"hi"}]}`, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺少 X-User-Id 应 400, status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	errBody := decodeErrorBody(t, readBody(t, resp))
	if errBody["code"] != float64(40001) { // BizInvalidArgument
		t.Errorf("错误码异常: %v", errBody["code"])
	}
}

// TestCompletions_GuestUserID 游客身份（负整数 user_id，auth.GuestUserID 派生）
// 必须放行——否则游客模式的对话会在 llm-gateway 被 400 拒绝（阶段2·游客模式）。
func TestCompletions_GuestUserID(t *testing.T) {
	up := &mockUpstream{}
	usage := &fakeUsageStore{}
	srv := newTestHandler(t, up, usage, HandlerConfig{Model: "deepseek-v4-flash"})
	up.set(0, `{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, "application/json")

	// 用固定合法游客 ID 派生负 user_id（与网关 guest.go 同算法）。
	guestID := auth.GuestUserID("550e8400-e29b-41d4-a716-446655440000")
	if guestID >= 0 {
		t.Fatalf("游客 ID 应派生负 user_id, got %d", guestID)
	}
	body := `{"messages":[{"role":"user","content":"hi"}]}`
	resp := doChat(t, srv.URL, body, strconv.FormatInt(guestID, 10))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("游客身份应放行, status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	if up.requests() != 1 {
		t.Errorf("应透传 1 次上游请求, got %d", up.requests())
	}
}

func TestCompletions_InvalidJSON(t *testing.T) {
	up := &mockUpstream{}
	srv := newTestHandler(t, up, &fakeUsageStore{}, HandlerConfig{Model: "m"})

	resp := doChat(t, srv.URL, `{invalid`, "1")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400, status = %d", resp.StatusCode)
	}
}

func TestCompletions_EmptyMessages(t *testing.T) {
	up := &mockUpstream{}
	srv := newTestHandler(t, up, &fakeUsageStore{}, HandlerConfig{Model: "m"})

	resp := doChat(t, srv.URL, `{"messages":[]}`, "1")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空 messages 应 400, status = %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// 上游错误映射（P2-35）
// ---------------------------------------------------------------------------

func TestCompletions_Upstream429(t *testing.T) {
	up := &mockUpstream{}
	srv := newTestHandler(t, up, &fakeUsageStore{}, HandlerConfig{Model: "m"})
	up.set(http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`, "application/json")

	resp := doChat(t, srv.URL, `{"messages":[{"role":"user","content":"hi"}]}`, "1")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("上游 429 应映射为 429, status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
}

func TestCompletions_Upstream500(t *testing.T) {
	up := &mockUpstream{}
	srv := newTestHandler(t, up, &fakeUsageStore{}, HandlerConfig{Model: "m"})
	up.set(http.StatusInternalServerError, `{"error":{"message":"boom"}}`, "application/json")

	resp := doChat(t, srv.URL, `{"messages":[{"role":"user","content":"hi"}]}`, "1")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("上游 500 应映射为 503, status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	errBody := decodeErrorBody(t, readBody(t, resp))
	if errBody["code"] != float64(50301) { // BizUnavailable
		t.Errorf("错误码异常: %v", errBody["code"])
	}
}

func TestCompletions_UpstreamTimeout(t *testing.T) {
	up := &mockUpstream{}
	up.set(0, "", "application/json")
	up.mu.Lock()
	up.sleep = 500 * time.Millisecond
	up.mu.Unlock()

	// 用 200ms 超时的独立 provider
	provider, err := llm.NewOpenAICompatible(llm.Config{
		BaseURL: up.serverURL(t), APIKey: "k", Model: "m", Timeout: 200 * time.Millisecond, MaxRetries: 0,
	})
	if err != nil {
		t.Fatalf("provider error = %v", err)
	}
	handler := NewHandler(HandlerConfig{Log: zap.NewNop(), Provider: provider, Usage: &fakeUsageStore{}, Model: "m"})
	mux := http.NewServeMux()
	handler.Register(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	resp := doChat(t, hs.URL, `{"messages":[{"role":"user","content":"hi"}]}`, "1")
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("上游超时应映射为 504, status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
}
