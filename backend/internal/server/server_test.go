package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegisterHealthz 验证 /healthz 返回 200 与 JSON 状态。
func TestRegisterHealthz(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHealthz(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"status":"ok"}` {
		t.Errorf("body = %q, want {\"status\":\"ok\"}", body)
	}
}
