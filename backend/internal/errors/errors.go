// Package errors 提供统一的应用错误类型与错误码（P2-10）。
//
// 各微服务统一使用本包构造/传播业务错误，保证：
//   - 错误语义一致：字符串错误码（对齐 gRPC codes）↔ 整型业务码（HTTP 响应体）；
//   - 支持 errors.Is / errors.As 链式查找底层原因；
//   - 三种出口统一映射：HTTP（gateway）、gRPC（服务间）、上下文（request_id 透传）。
//
// 对外 HTTP 错误响应体固定为：
//
//	{
//	  "code":       40401,        // 整型业务码（错误码表见下）
//	  "message":    "会话不存在",
//	  "request_id": "a1b2c3..."   // 全链路追踪 ID
//	}
package errors

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// 错误码定义
// ---------------------------------------------------------------------------

// ErrorCode 业务错误码。字符串形式，语义对齐 gRPC codes，
// 便于服务间 gRPC 传递时不丢失语义。
type ErrorCode string

// 预定义错误码。HTTP 状态与整型业务码见 ErrorCode.HTTPStatus / ErrorCode.BizCode。
const (
	CodeOK                 ErrorCode = "OK"
	CodeInvalidArgument    ErrorCode = "INVALID_ARGUMENT"    // 参数非法
	CodeFailedPrecondition ErrorCode = "FAILED_PRECONDITION" // 前置条件不满足（如状态不允许操作）
	CodeUnauthenticated    ErrorCode = "UNAUTHENTICATED"     // 未认证 / token 无效
	CodePermissionDenied   ErrorCode = "PERMISSION_DENIED"   // 已认证但无权限（RBAC）
	CodeNotFound           ErrorCode = "NOT_FOUND"           // 资源不存在
	CodeAlreadyExists      ErrorCode = "ALREADY_EXISTS"      // 资源冲突（如用户名已注册）
	CodeVersionConflict    ErrorCode = "VERSION_CONFLICT"    // 版本冲突（同版本号但内容不同，需覆盖或改版本号）
	CodeResourceExhausted  ErrorCode = "RESOURCE_EXHAUSTED"  // 限流 / 配额不足
	CodeDeadlineExceeded   ErrorCode = "DEADLINE_EXCEEDED"   // 超时
	CodeUnavailable        ErrorCode = "UNAVAILABLE"         // 服务不可用（上游失败/过载）
	CodeCancelled          ErrorCode = "CANCELLED"           // 请求被取消
	CodeInternal           ErrorCode = "INTERNAL"            // 内部错误（兜底）
)

// 整型业务码（HTTP 错误响应 body 的 code 字段，对外契约的一部分）。
// 编码规则：<HTTP状态> <两位序号>，如 40401 = 404 + 01。
const (
	BizOK                 = 0
	BizInvalidArgument    = 40001
	BizFailedPrecondition = 40002
	BizUnauthenticated    = 40101
	BizPermissionDenied   = 40301
	BizNotFound           = 40401
	BizAlreadyExists      = 40901
	BizVersionConflict    = 40902
	BizResourceExhausted  = 42901
	BizCancelled          = 49901
	BizInternal           = 50001
	BizUnavailable        = 50301
	BizDeadlineExceeded   = 50401
)

// BizCode 返回错误码对应的整型业务码（HTTP 响应体 code 字段）。
func (c ErrorCode) BizCode() int {
	switch c {
	case CodeInvalidArgument:
		return BizInvalidArgument
	case CodeFailedPrecondition:
		return BizFailedPrecondition
	case CodeUnauthenticated:
		return BizUnauthenticated
	case CodePermissionDenied:
		return BizPermissionDenied
	case CodeNotFound:
		return BizNotFound
	case CodeAlreadyExists:
		return BizAlreadyExists
	case CodeVersionConflict:
		return BizVersionConflict
	case CodeResourceExhausted:
		return BizResourceExhausted
	case CodeCancelled:
		return BizCancelled
	case CodeUnavailable:
		return BizUnavailable
	case CodeDeadlineExceeded:
		return BizDeadlineExceeded
	case CodeOK:
		return BizOK
	default:
		return BizInternal
	}
}

// HTTPStatus 返回错误码对应的 HTTP 状态码。
func (c ErrorCode) HTTPStatus() int {
	switch c {
	case CodeInvalidArgument, CodeFailedPrecondition:
		return http.StatusBadRequest
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodePermissionDenied:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeAlreadyExists:
		return http.StatusConflict
	case CodeVersionConflict:
		return http.StatusConflict
	case CodeResourceExhausted:
		return http.StatusTooManyRequests
	case CodeCancelled:
		return 499 // Client Closed Request（nginx 惯例）
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	case CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case CodeOK:
		return http.StatusOK
	default:
		return http.StatusInternalServerError
	}
}

// GRPCCode 返回错误码对应的 gRPC status code。
func (c ErrorCode) GRPCCode() codes.Code {
	switch c {
	case CodeInvalidArgument:
		return codes.InvalidArgument
	case CodeFailedPrecondition:
		return codes.FailedPrecondition
	case CodeUnauthenticated:
		return codes.Unauthenticated
	case CodePermissionDenied:
		return codes.PermissionDenied
	case CodeNotFound:
		return codes.NotFound
	case CodeAlreadyExists:
		return codes.AlreadyExists
	case CodeVersionConflict:
		return codes.AlreadyExists
	case CodeResourceExhausted:
		return codes.ResourceExhausted
	case CodeCancelled:
		return codes.Canceled
	case CodeUnavailable:
		return codes.Unavailable
	case CodeDeadlineExceeded:
		return codes.DeadlineExceeded
	case CodeOK:
		return codes.OK
	default:
		return codes.Internal
	}
}

// grpcCodeToErrorCode 反向映射：gRPC status code → 业务错误码（客户端侧）。
func grpcCodeToErrorCode(c codes.Code) ErrorCode {
	switch c {
	case codes.InvalidArgument:
		return CodeInvalidArgument
	case codes.FailedPrecondition:
		return CodeFailedPrecondition
	case codes.Unauthenticated:
		return CodeUnauthenticated
	case codes.PermissionDenied:
		return CodePermissionDenied
	case codes.NotFound:
		return CodeNotFound
	case codes.AlreadyExists:
		return CodeAlreadyExists
	case codes.ResourceExhausted:
		return CodeResourceExhausted
	case codes.Canceled:
		return CodeCancelled
	case codes.Unavailable:
		return CodeUnavailable
	case codes.DeadlineExceeded:
		return CodeDeadlineExceeded
	default:
		return CodeInternal
	}
}

// ---------------------------------------------------------------------------
// 错误类型
// ---------------------------------------------------------------------------

// Error 统一应用错误。
type Error struct {
	Code      ErrorCode // 业务错误码
	Message   string    // 面向调用方的可读信息（不含内部细节）
	RequestID string    // 全链路追踪 ID（gateway 生成，贯穿各服务）
	Cause     error     // 底层原因（内部日志用，不回传给调用方）
}

// Error 实现 error 接口。格式：[CODE] message (request_id=xxx): cause
func (e *Error) Error() string {
	base := fmt.Sprintf("[%s] %s", e.Code, e.Message)
	if e.RequestID != "" {
		base += fmt.Sprintf(" (request_id=%s)", e.RequestID)
	}
	if e.Cause != nil {
		return base + ": " + e.Cause.Error()
	}
	return base
}

// Unwrap 暴露底层原因，支持 errors.Is / errors.As。
func (e *Error) Unwrap() error { return e.Cause }

// WithRequestID 设置全链路追踪 ID（链式调用，方便一行完成构造）。
func (e *Error) WithRequestID(id string) *Error {
	e.RequestID = id
	return e
}

// WithCause 附加底层原因（链式调用）。
func (e *Error) WithCause(cause error) *Error {
	e.Cause = cause
	return e
}

// GRPCStatus 让 *Error 直接作为 gRPC status 错误传播（服务端返回时自动携带）。
// 业务错误码通过 ErrorInfo.Reason 传递，request_id 通过 ErrorInfo.Metadata 传递。
func (e *Error) GRPCStatus() *status.Status {
	st := status.New(e.Code.GRPCCode(), e.Message)
	md := map[string]string{"request_id": e.RequestID}
	if detailed, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   string(e.Code),
		Metadata: md,
	}); err == nil {
		return detailed
	}
	return st
}

// ---------------------------------------------------------------------------
// 构造与提取
// ---------------------------------------------------------------------------

// New 创建指定错误码、无底层原因的错误。
func New(code ErrorCode, msg string) *Error {
	return &Error{Code: code, Message: msg}
}

// Wrap 包装底层错误并附加业务上下文（保留原因链，内部日志可见）。
func Wrap(code ErrorCode, msg string, cause error) *Error {
	return &Error{Code: code, Message: msg, Cause: cause}
}

// CodeOf 提取错误码。优先识别 *Error；其次识别 gRPC status 错误；
// 其余一律视为 CodeInternal。
func CodeOf(err error) ErrorCode {
	if err == nil {
		return CodeInternal
	}
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	if st, ok := status.FromError(err); ok {
		return grpcCodeToErrorCode(st.Code())
	}
	return CodeInternal
}

// HTTPStatus 将错误映射为 HTTP 状态码（gateway / handler 统一出口）。
func HTTPStatus(err error) int {
	return CodeOf(err).HTTPStatus()
}

// HTTPBody 将错误转为 HTTP 错误响应体，返回 (HTTP 状态码, 响应体)。
// 响应体固定为 {code, message, request_id}，是对外契约的一部分。
func HTTPBody(err error) (int, map[string]any) {
	var appErr *Error
	if !errors.As(err, &appErr) {
		appErr = Wrap(CodeInternal, "internal error", err)
	}
	return appErr.Code.HTTPStatus(), map[string]any{
		"code":       appErr.Code.BizCode(),
		"message":    appErr.Message,
		"request_id": appErr.RequestID,
	}
}

// FromGRPCError 从 gRPC 调用错误恢复为 *Error（服务端返回 -> 客户端使用）。
// 若错误不是 gRPC status，则包装为 CodeInternal。
func FromGRPCError(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return Wrap(CodeInternal, "gRPC 调用失败", err)
	}
	code := grpcCodeToErrorCode(st.Code())
	reqID := ""
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			code = ErrorCode(ei.Reason)
			reqID = ei.Metadata["request_id"]
		}
	}
	return &Error{Code: code, Message: st.Message(), RequestID: reqID, Cause: err}
}

// ---------------------------------------------------------------------------
// request_id 上下文透传
// ---------------------------------------------------------------------------

type ctxKeyRequestID struct{}

// NewContextWithRequestID 将 request_id 写入 context（middleware 调用）。
func NewContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID{}, id)
}

// RequestIDFromContext 从 context 读取 request_id（未设置时返回空串）。
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(ctxKeyRequestID{}).(string); ok {
		return id
	}
	return ""
}
