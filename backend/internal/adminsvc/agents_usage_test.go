package adminsvc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"go.uber.org/zap"
)

func TestAgentUsage_NotConfigured(t *testing.T) {
	s := &Service{llmURL: "", http: http.DefaultClient, log: zap.NewNop()}
	_, err := s.agentUsage("math", 7)
	if apperr.CodeOf(err) != apperr.CodeUnavailable {
		t.Fatalf("未配置 llmURL 应 Unavailable(503), got %v", err)
	}
}

func TestAgentUsage_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage/agents/math" {
			t.Errorf("意外路径 %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("days"); got != "7" {
			t.Errorf("days = %q, want 7", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agent_id":"math","calls":5,"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"cost_usd":0.0012,"last_used_at":"2026-08-01T00:00:00Z"}`))
	}))
	defer ts.Close()

	s := &Service{llmURL: ts.URL, http: ts.Client(), log: zap.NewNop()}
	out, err := s.agentUsage("math", 7)
	if err != nil {
		t.Fatalf("agentUsage: %v", err)
	}
	if out["calls"] != float64(5) {
		t.Fatalf("calls 透传异常: %+v", out)
	}
	if out["cost_usd"] != 0.0012 {
		t.Fatalf("cost 透传异常: %+v", out)
	}
	if out["total_tokens"] != float64(150) {
		t.Fatalf("total_tokens 透传异常: %+v", out)
	}
}

func TestAgentUsage_UpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer ts.Close()

	s := &Service{llmURL: ts.URL, http: ts.Client(), log: zap.NewNop()}
	if _, err := s.agentUsage("math", 7); apperr.CodeOf(err) != apperr.CodeUnavailable {
		t.Fatalf("上游 5xx 应 Unavailable, got %v", err)
	}
}

func TestAgentUsage_BadJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer ts.Close()

	s := &Service{llmURL: ts.URL, http: ts.Client(), log: zap.NewNop()}
	if _, err := s.agentUsage("math", 7); apperr.CodeOf(err) != apperr.CodeUnavailable {
		t.Fatalf("坏响应体应 Unavailable, got %v", err)
	}
}
