package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// preflight 模拟浏览器预检请求（OPTIONS + Access-Control-Request-Method + Origin）。
func preflight(handler http.Handler, origin, reqHeaders string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodOptions, "/v1/auth/login/tutor", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	if reqHeaders != "" {
		req.Header.Set("Access-Control-Request-Headers", reqHeaders)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestCORS_GuestHeaderAllowed(t *testing.T) {
	h := CORS(CORSConfig{AllowedOrigins: []string{"http://localhost:3000"}})(mockHandler(http.StatusOK, "ok"))

	// 游客模式（未登录）请求会带 X-Guest-ID 头：预检必须放行该头，
	// 否则浏览器拦截 → 表现为"无法连接服务器"。回归：早期白名单缺此头导致
	// "Request header field x-guest-id is not allowed by Access-Control-Allow-Headers"。
	rec := preflight(h, "http://localhost:3000", "content-type, x-guest-id")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("预检状态码 = %d, want %d", rec.Code, http.StatusNoContent)
	}
	allowHeaders := rec.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(strings.ToLower(allowHeaders), "x-guest-id") {
		t.Fatalf("Access-Control-Allow-Headers = %q, 缺少 x-guest-id", allowHeaders)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want http://localhost:3000", got)
	}
}

func TestCORS_NotAllowedOriginRejected(t *testing.T) {
	h := CORS(CORSConfig{AllowedOrigins: []string{"http://localhost:3000"}})(mockHandler(http.StatusOK, "ok"))

	rec := preflight(h, "https://evil.example.com", "x-guest-id")
	// 不在白名单的来源：响应头不带 Access-Control-Allow-Origin（浏览器据此拦截）。
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("非白名单来源预检被放行: Access-Control-Allow-Origin = %q", got)
	}
}

func TestCORS_NoOriginIsSameOrigin(t *testing.T) {
	// 同源（无 Origin 头）请求不受 CORS 限制，正常通过。
	h := CORS(CORSConfig{AllowedOrigins: []string{"http://localhost:3000"}})(mockHandler(http.StatusOK, "ok"))
	rec := do(h, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("同源请求状态码 = %d, want %d", rec.Code, http.StatusOK)
	}
}
