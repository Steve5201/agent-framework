// respond.go —— HTTP 响应辅助（P2-53 统一错误体）。
//
// 所有错误统一走 apperr.HTTPBody：HTTP 状态码 + {code, message, request_id}，
// 前端/排障人员凭 request_id 在网关日志中串起整条链路。
package gatewaysvc

import (
	"encoding/json"
	"net/http"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// writeJSON 写出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// JSON 序列化失败（理论上不会）则静默丢弃，避免二次报错。
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 把统一错误写成 HTTP 错误响应。
//   - 下游 gRPC 返回的是 status 错误（非 *Error），先经 FromGRPCError 恢复
//     业务错误码（如 InvalidArgument → 40001），否则会被 HTTPBody 兜底成
//     50001 "internal error"，掩盖真实原因（如注册参数不合规）；
//   - request_id 缺失时用本请求 context 里的补齐（保证每条错误都有踪可查）。
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	err = apperr.FromGRPCError(err)
	status, body := apperr.HTTPBody(err)
	if body["request_id"] == "" {
		body["request_id"] = apperr.RequestIDFromContext(r.Context())
	}
	writeJSON(w, status, body)
}
