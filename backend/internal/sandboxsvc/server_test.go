// server_test.go —— sandbox-service HTTP 层单测（httptest，无需真实环境）。
package sandboxsvc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	exec := NewExecutor(Config{
		WorkRoot:   t.TempDir(),
		MaxTimeout: 5 * time.Second,
		AgentUID:   1000,
		UIDBase:    2000,
		Log:        zap.NewNop(),
	})
	return NewServer(ServerConfig{
		MaxWorkers: 2,
		Log:        zap.NewNop(),
		Executor:   exec,
	})
}

func doExec(t *testing.T, s *Server, body []byte) (*httptest.ResponseRecorder, map[string]json.RawMessage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/exec", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	var parsed map[string]json.RawMessage
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("响应不是合法 JSON: %v", err)
		}
	}
	return rec, parsed
}

func TestHealthz(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz 状态码 = %d", rec.Code)
	}
}

func TestExec_BadJSON(t *testing.T) {
	s := newTestServer(t)
	rec, _ := doExec(t, s, []byte(`{invalid`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应返回 400，实际 %d", rec.Code)
	}
}

func TestExec_ValidationErrorReturnedAsResult(t *testing.T) {
	s := newTestServer(t)
	// user_id 缺失 → 沙盒拒绝（HTTP 200 + result.Error 原样返回）
	rec, parsed := doExec(t, s, []byte(`{"language":"shell","code":"echo hi"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("校验失败应返回 200 + Error，实际 %d", rec.Code)
	}
	if len(parsed["error"]) == 0 || !bytes.Contains(parsed["error"], []byte("user_id")) {
		t.Fatalf("响应缺少 user_id 错误说明: %s", rec.Body.String())
	}
}

func TestExec_BlacklistReturnedAsResult(t *testing.T) {
	s := newTestServer(t)
	rec, parsed := doExec(t, s, []byte(`{"user_id":1,"language":"shell","code":"sudo whoami"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("黑名单拒绝应返回 200 + Error，实际 %d", rec.Code)
	}
	if !bytes.Contains(parsed["error"], []byte("黑名单")) {
		t.Fatalf("响应缺少黑名单说明: %s", rec.Body.String())
	}
}

// TestExec_Semaphore 并发限流：占满信号量后，新请求应立即 503。
func TestExec_Semaphore(t *testing.T) {
	s := newTestServer(t)
	// 占满 MaxWorkers=2 的槽位
	s.sem <- struct{}{}
	s.sem <- struct{}{}
	defer func() {
		<-s.sem
		<-s.sem
	}()

	rec, _ := doExec(t, s, []byte(`{"user_id":1,"language":"shell","code":"echo hi"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("并发占满应返回 503，实际 %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("并发")) {
		t.Fatalf("503 响应应说明并发超限: %s", rec.Body.String())
	}
}
