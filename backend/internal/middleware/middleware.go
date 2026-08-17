// Package middleware 提供 HTTP 服务统一中间件（P2-11）：
//   - RequestID：request_id 生成/透传（无则生成，有则透传），写入 context 与响应头；
//   - Logger：zap 结构化访问日志（method/path/status/耗时/request_id/客户端 IP）；
//   - Recovery：panic 恢复，返回统一错误响应体 {code,message,request_id}，不泄露堆栈。
//
// 中间件统一使用 Chain 组合，顺序即执行顺序（列表第一项为最外层）。
// 推荐组合：Chain(handler, RequestID(), Logger(log), Recovery(log))
// 这样 RequestID 最外层先生成 request_id，Logger 记录全链路，Recovery 兜底 panic。
package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-backend/internal/reqid"
	"go.uber.org/zap"
)

// HeaderRequestID 请求/响应头中携带 request_id 的字段名。
const HeaderRequestID = "X-Request-Id"

// Chain 按顺序组合多个中间件。mws 中的第一项是执行时的最外层。
func Chain(handler http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		handler = mws[i](handler)
	}
	return handler
}

// newRequestID 生成 32 位十六进制随机 ID（复用 reqid 包，与 gRPC 侧一致）。
func newRequestID() string {
	return reqid.Generate()
}

// RequestID 生成/透传 request_id：
//   - 请求头 X-Request-Id 已存在（如网关转发）→ 原样透传；
//   - 不存在 → 生成新 ID；
//   - 无论哪种情况，都写入 context（供日志/错误携带）与响应头。
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimSpace(r.Header.Get(HeaderRequestID))
			if id == "" {
				id = newRequestID()
			}
			ctx := errors.NewContextWithRequestID(r.Context(), id)
			w.Header().Set(HeaderRequestID, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// responseWriter 包装 http.ResponseWriter，捕获状态码与写入字节数供访问日志使用。
// 同时透传 http.Flusher（SSE 流式响应需要 Flush），避免破坏流式能力。
type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

// WriteHeader 记录首次写入的状态码（后续重复调用仍记录第一次）。
func (w *responseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write 记录累计写入字节数。
func (w *responseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush 透传 Flush 能力（SSE / 流式响应必需）。
func (w *responseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// clientIP 提取客户端真实 IP。优先取 X-Forwarded-For 首项（经网关转发时），
// 否则取 RemoteAddr 的 host 部分。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Logger 输出 zap 结构化访问日志。
// 每个请求记录：method/path/status/耗时/request_id/客户端 IP/User-Agent/字节数。
func Logger(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w}

			next.ServeHTTP(rw, r)

			if rw.status == 0 {
				rw.status = http.StatusOK // 处理器未显式写状态码，视为 200
			}
			log.Info("http request",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.String("query", r.URL.RawQuery),
				zap.Int("status", rw.status),
				zap.Int64("duration_ms", time.Since(start).Milliseconds()),
				zap.Int("bytes", rw.bytes),
				zap.String("request_id", errors.RequestIDFromContext(r.Context())),
				zap.String("client_ip", clientIP(r)),
				zap.String("user_agent", r.UserAgent()),
			)
		})
	}
}

// Recovery 捕获 handler 抛出的 panic：
//   - 记录完整堆栈（内部排查用）；
//   - 返回 500 + 统一错误响应体 {code,message,request_id}，堆栈不泄露给调用方；
//   - 响应头携带 request_id，便于调用方对齐日志。
func Recovery(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					reqID := errors.RequestIDFromContext(r.Context())
					log.Error("panic recovered",
						zap.Any("panic", rec),
						zap.String("method", r.Method),
						zap.String("path", r.URL.Path),
						zap.String("request_id", reqID),
						zap.Stack("stack"),
					)

					appErr := toPanicError(rec).WithRequestID(reqID)
					status, body := errors.HTTPBody(appErr)
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set(HeaderRequestID, reqID)
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(body)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// toPanicError 将 panic 值规范化为 *errors.Error。
// 已是应用错误则原样保留；其余统一归为 INTERNAL（内部细节只进日志，不进响应体）。
func toPanicError(v any) *errors.Error {
	switch e := v.(type) {
	case *errors.Error:
		return e
	case error:
		return errors.Wrap(errors.CodeInternal, "internal error", e)
	default:
		return errors.New(errors.CodeInternal, "internal error")
	}
}
