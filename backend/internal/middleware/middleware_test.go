package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Steve5201/agent-backend/internal/errors"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// mockHandler 返回固定状态码与响应体的处理器。
func mockHandler(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// do 对 handler 发起一次 GET 请求并返回记录器。
func do(handler http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// requestIDFromCtx 从请求 context 读取 request_id（测试辅助）。
func requestIDFromCtx(r *http.Request) string {
	return errors.RequestIDFromContext(r.Context())
}

// nopLogger 返回静默 logger（仅用于占位组合测试）。
func nopLogger() *zap.Logger { return zap.NewNop() }

// newAppErr 构造一个带语义的 *errors.Error（测试辅助）。
func newAppErr() *errors.Error {
	return errors.New(errors.CodeResourceExhausted, "quota exceeded").WithRequestID("req-panic")
}

// ---------------------------------------------------------------------------
// RequestID
// ---------------------------------------------------------------------------

// TestRequestID_Generates 验证无请求头时生成新 ID 并写入 context 与响应头。
func TestRequestID_Generates(t *testing.T) {
	var gotID string
	handler := RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = requestIDFromCtx(r)
		io.WriteString(w, "ok")
	}))

	rec := do(handler, "/ping")
	if gotID == "" {
		t.Fatal("context 应携带 request_id")
	}
	if rec.Header().Get(HeaderRequestID) != gotID {
		t.Errorf("响应头 X-Request-Id = %q, 与 context (%q) 不一致", rec.Header().Get(HeaderRequestID), gotID)
	}
}

// TestRequestID_Propagates 验证已存在的请求头被原样透传。
func TestRequestID_Propagates(t *testing.T) {
	const want = "req-from-gateway"
	handler := RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("echo", requestIDFromCtx(r))
		io.WriteString(w, "ok")
	}))

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set(HeaderRequestID, want)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("echo") != want {
		t.Errorf("context request_id = %q, want %q", rec.Header().Get("echo"), want)
	}
	if rec.Header().Get(HeaderRequestID) != want {
		t.Errorf("响应头 = %q, want %q", rec.Header().Get(HeaderRequestID), want)
	}
}

// ---------------------------------------------------------------------------
// Logger
// ---------------------------------------------------------------------------

// TestLogger_Fields 验证访问日志包含关键字段（method/path/status/request_id 等）。
func TestLogger_Fields(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	log := zap.New(core)

	handler := Chain(
		mockHandler(http.StatusCreated, "hello"),
		RequestID(),
		Logger(log),
	)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions?page=1", nil)
	req.Header.Set(HeaderRequestID, "req-log-1")
	req.RemoteAddr = "10.0.0.5:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if logs.Len() != 1 {
		t.Fatalf("应有 1 条访问日志，实际 %d", logs.Len())
	}
	fields := logs.All()[0].ContextMap()

	want := map[string]any{
		"method":     "POST",
		"path":       "/api/sessions",
		"query":      "page=1",
		"status":     int64(http.StatusCreated),
		"bytes":      int64(5),
		"request_id": "req-log-1",
		"client_ip":  "10.0.0.5",
	}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("日志字段 %s = %v, want %v", k, fields[k], v)
		}
	}
}

// ---------------------------------------------------------------------------
// Recovery
// ---------------------------------------------------------------------------

// TestRecovery_PanicString 验证普通 panic 返回 500 + 统一错误体，不泄露细节。
func TestRecovery_PanicString(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	log := zap.New(core)

	handler := Chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("数据库连接炸了") // 内部细节，不应泄露到响应体
		}),
		RequestID(),
		Recovery(log),
	)

	rec := do(handler, "/boom")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v", err)
	}
	if body["code"] != float64(50001) {
		t.Errorf("body.code = %v, want 50001", body["code"])
	}
	if body["message"] != "internal error" {
		t.Errorf("body.message = %v, want 'internal error'", body["message"])
	}
	if rec.Header().Get(HeaderRequestID) == "" {
		t.Error("恢复响应应携带 X-Request-Id")
	}
	if body["request_id"] != rec.Header().Get(HeaderRequestID) {
		t.Error("响应体 request_id 应与响应头一致")
	}

	// 内部细节只进 error 级日志
	hasErrorLog := false
	for _, e := range logs.All() {
		if e.Level == zap.ErrorLevel {
			hasErrorLog = true
		}
	}
	if !hasErrorLog {
		t.Error("panic 应记录 error 级日志")
	}
}

// TestRecovery_PanicAppError 验证 panic 值本身是应用错误时保留其语义。
func TestRecovery_PanicAppError(t *testing.T) {
	handler := Chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(newAppErr())
		}),
		RequestID(),
		Recovery(nopLogger()),
	)

	rec := do(handler, "/boom")
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != float64(42901) {
		t.Errorf("body.code = %v, want 42901", body["code"])
	}
}

// ---------------------------------------------------------------------------
// Chain / 组合
// ---------------------------------------------------------------------------

// TestChain_Order 验证 Chain 组合顺序：内层处理器能从 context 读取到
// 外层 RequestID 写入的 request_id。
func TestChain_Order(t *testing.T) {
	handler := Chain(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if requestIDFromCtx(r) == "" {
				t.Error("内层处理器应能从 context 读取 request_id")
			}
			io.WriteString(w, "ok")
		}),
		RequestID(),
		Recovery(nopLogger()),
		Logger(nopLogger()),
	)
	rec := do(handler, "/x")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
