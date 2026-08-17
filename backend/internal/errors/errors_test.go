package errors

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// 基础构造 / 链式查找
// ---------------------------------------------------------------------------

// TestNew_ErrorString 验证错误码与消息的格式化。
func TestNew_ErrorString(t *testing.T) {
	e := New(CodeNotFound, "user not found")
	if e.Code != CodeNotFound {
		t.Errorf("Code = %q, want %q", e.Code, CodeNotFound)
	}
	if e.Error() != "[NOT_FOUND] user not found" {
		t.Errorf("Error() = %q", e.Error())
	}
}

// TestWrap_Unwrap 验证包装错误保留原因链。
func TestWrap_Unwrap(t *testing.T) {
	cause := fmt.Errorf("dial tcp refused")
	wrapped := Wrap(CodeInternal, "connect db", cause)

	if !errors.Is(wrapped, cause) {
		t.Error("errors.Is(wrapped, cause) 应为 true")
	}

	var appErr *Error
	if !errors.As(wrapped, &appErr) {
		t.Fatal("errors.As 应提取到 *Error")
	}
	if appErr.Code != CodeInternal {
		t.Errorf("Code = %q, want INTERNAL", appErr.Code)
	}
}

// TestCodeOf_PlainError 验证非应用错误归为 INTERNAL。
func TestCodeOf_PlainError(t *testing.T) {
	if got := CodeOf(fmt.Errorf("raw error")); got != CodeInternal {
		t.Errorf("CodeOf(raw) = %q, want INTERNAL", got)
	}
	if got := CodeOf(nil); got != CodeInternal {
		t.Errorf("CodeOf(nil) = %q, want INTERNAL", got)
	}
}

// TestChain_WithRequestID_WithCause 验证链式构造可读性（Error() 格式）。
func TestChain_WithRequestID_WithCause(t *testing.T) {
	cause := fmt.Errorf("disk full")
	e := New(CodeResourceExhausted, "quota exceeded").
		WithRequestID("req-123").
		WithCause(cause)

	if e.RequestID != "req-123" {
		t.Errorf("RequestID = %q, want req-123", e.RequestID)
	}
	want := "[RESOURCE_EXHAUSTED] quota exceeded (request_id=req-123): disk full"
	if e.Error() != want {
		t.Errorf("Error() = %q, want %q", e.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// 错误码三态映射
// ---------------------------------------------------------------------------

// TestErrorCode_BizCode 验证每个错误码对应的整型业务码。
func TestErrorCode_BizCode(t *testing.T) {
	cases := []struct {
		code ErrorCode
		want int
	}{
		{CodeOK, BizOK},
		{CodeInvalidArgument, BizInvalidArgument},
		{CodeFailedPrecondition, BizFailedPrecondition},
		{CodeUnauthenticated, BizUnauthenticated},
		{CodePermissionDenied, BizPermissionDenied},
		{CodeNotFound, BizNotFound},
		{CodeAlreadyExists, BizAlreadyExists},
		{CodeVersionConflict, BizVersionConflict},
		{CodeResourceExhausted, BizResourceExhausted},
		{CodeCancelled, BizCancelled},
		{CodeUnavailable, BizUnavailable},
		{CodeDeadlineExceeded, BizDeadlineExceeded},
		{CodeInternal, BizInternal},
	}
	for _, c := range cases {
		if got := c.code.BizCode(); got != c.want {
			t.Errorf("BizCode(%q) = %d, want %d", c.code, got, c.want)
		}
	}
	// 未知错误码兜底为 INTERNAL
	if got := ErrorCode("UNKNOWN_X").BizCode(); got != BizInternal {
		t.Errorf("BizCode(unknown) = %d, want %d", got, BizInternal)
	}
}

// TestErrorCode_HTTPStatus 验证错误码到 HTTP 状态码的映射（含特殊 429/499/503/504）。
func TestErrorCode_HTTPStatus(t *testing.T) {
	cases := []struct {
		code ErrorCode
		want int
	}{
		{CodeOK, http.StatusOK},
		{CodeInvalidArgument, http.StatusBadRequest},
		{CodeFailedPrecondition, http.StatusBadRequest},
		{CodeUnauthenticated, http.StatusUnauthorized},
		{CodePermissionDenied, http.StatusForbidden},
		{CodeNotFound, http.StatusNotFound},
		{CodeAlreadyExists, http.StatusConflict},
		{CodeVersionConflict, http.StatusConflict},
		{CodeResourceExhausted, http.StatusTooManyRequests},
		{CodeCancelled, 499}, // Client Closed Request
		{CodeUnavailable, http.StatusServiceUnavailable},
		{CodeDeadlineExceeded, http.StatusGatewayTimeout},
		{CodeInternal, http.StatusInternalServerError},
	}
	for _, c := range cases {
		if got := c.code.HTTPStatus(); got != c.want {
			t.Errorf("HTTPStatus(%q) = %d, want %d", c.code, got, c.want)
		}
	}
	// 未知错误码兜底为 500
	if got := ErrorCode("UNKNOWN_X").HTTPStatus(); got != http.StatusInternalServerError {
		t.Errorf("HTTPStatus(unknown) = %d, want 500", got)
	}
}

// TestErrorCode_GRPCCode 验证错误码到 gRPC status code 的映射。
func TestErrorCode_GRPCCode(t *testing.T) {
	cases := []struct {
		code ErrorCode
		want codes.Code
	}{
		{CodeOK, codes.OK},
		{CodeInvalidArgument, codes.InvalidArgument},
		{CodeFailedPrecondition, codes.FailedPrecondition},
		{CodeUnauthenticated, codes.Unauthenticated},
		{CodePermissionDenied, codes.PermissionDenied},
		{CodeNotFound, codes.NotFound},
		{CodeAlreadyExists, codes.AlreadyExists},
		{CodeResourceExhausted, codes.ResourceExhausted},
		{CodeCancelled, codes.Canceled},
		{CodeUnavailable, codes.Unavailable},
		{CodeDeadlineExceeded, codes.DeadlineExceeded},
		{CodeInternal, codes.Internal},
	}
	for _, c := range cases {
		if got := c.code.GRPCCode(); got != c.want {
			t.Errorf("GRPCCode(%q) = %v, want %v", c.code, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP 出口
// ---------------------------------------------------------------------------

// TestHTTPStatus 验证错误对象到 HTTP 状态码的映射（函数级入口）。
func TestHTTPStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{New(CodeNotFound, "x"), http.StatusNotFound},
		{New(CodeInvalidArgument, "x"), http.StatusBadRequest},
		{New(CodeUnauthenticated, "x"), http.StatusUnauthorized},
		{New(CodePermissionDenied, "x"), http.StatusForbidden},
		{New(CodeAlreadyExists, "x"), http.StatusConflict},
		{New(CodeResourceExhausted, "x"), http.StatusTooManyRequests},
		{New(CodeInternal, "x"), http.StatusInternalServerError},
		{fmt.Errorf("raw"), http.StatusInternalServerError},
	}

	for _, c := range cases {
		if got := HTTPStatus(c.err); got != c.want {
			t.Errorf("HTTPStatus(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// TestHTTPBody 验证错误响应体 {code, message, request_id}。
func TestHTTPBody(t *testing.T) {
	// 1. 带 request_id 的业务错误
	err := New(CodeNotFound, "会话不存在").WithRequestID("req-abc")
	statusCode, body := HTTPBody(err)
	if statusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", statusCode)
	}
	if body["code"] != BizNotFound {
		t.Errorf("body.code = %v, want %d", body["code"], BizNotFound)
	}
	if body["message"] != "会话不存在" {
		t.Errorf("body.message = %v", body["message"])
	}
	if body["request_id"] != "req-abc" {
		t.Errorf("body.request_id = %v, want req-abc", body["request_id"])
	}

	// 2. 非应用错误兜底为 INTERNAL，message 不泄露内部细节
	_, body2 := HTTPBody(fmt.Errorf("secret: db password leaked"))
	if body2["code"] != BizInternal {
		t.Errorf("body2.code = %v, want %d", body2["code"], BizInternal)
	}
	if body2["message"] != "internal error" {
		t.Errorf("body2.message = %v, want 'internal error'", body2["message"])
	}
}

// ---------------------------------------------------------------------------
// gRPC 出口与恢复
// ---------------------------------------------------------------------------

// TestError_GRPCStatus_WithDetails 验证 *Error 可直接作为 gRPC status 传播，
// 且携带 ErrorInfo（reason=业务码，metadata.request_id）。
func TestError_GRPCStatus_WithDetails(t *testing.T) {
	appErr := New(CodePermissionDenied, "无权限访问").
		WithRequestID("req-777")

	st := appErr.GRPCStatus()
	if st.Code() != codes.PermissionDenied {
		t.Errorf("status.Code = %v, want PermissionDenied", st.Code())
	}
	if st.Message() != "无权限访问" {
		t.Errorf("status.Message = %q", st.Message())
	}

	var found *errdetails.ErrorInfo
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			found = ei
		}
	}
	if found == nil {
		t.Fatal("status.Details 中应包含 ErrorInfo")
	}
	if found.Reason != string(CodePermissionDenied) {
		t.Errorf("ErrorInfo.Reason = %q, want %q", found.Reason, CodePermissionDenied)
	}
	if found.Metadata["request_id"] != "req-777" {
		t.Errorf("ErrorInfo.Metadata.request_id = %q, want req-777", found.Metadata["request_id"])
	}
}

// TestCodeOf_GRPCStatusError 验证 CodeOf 能识别 gRPC status 错误。
func TestCodeOf_GRPCStatusError(t *testing.T) {
	grpcErr := status.Error(codes.NotFound, "session not found")
	if got := CodeOf(grpcErr); got != CodeNotFound {
		t.Errorf("CodeOf(grpc NotFound) = %q, want NOT_FOUND", got)
	}
}

// TestFromGRPCError 验证从 gRPC 错误恢复业务码与 request_id（服务端->客户端）。
func TestFromGRPCError(t *testing.T) {
	// 1. 由 *Error 序列化而来的 gRPC 错误，能完整恢复
	src := New(CodeResourceExhausted, "每分钟限 100 次").WithRequestID("req-555")
	grpcErr := status.ErrorProto(src.GRPCStatus().Proto())

	recovered, ok := FromGRPCError(grpcErr).(*Error)
	if !ok {
		t.Fatalf("FromGRPCError 应返回 *Error，got %T", recovered)
	}
	if recovered.Code != CodeResourceExhausted {
		t.Errorf("recovered.Code = %q, want RESOURCE_EXHAUSTED", recovered.Code)
	}
	if recovered.Message != "每分钟限 100 次" {
		t.Errorf("recovered.Message = %q", recovered.Message)
	}
	if recovered.RequestID != "req-555" {
		t.Errorf("recovered.RequestID = %q, want req-555", recovered.RequestID)
	}

	// 2. 不带 ErrorInfo 的纯 status 错误：按 code 反查
	plain := status.Error(codes.InvalidArgument, "bad param")
	rec2 := FromGRPCError(plain)
	if CodeOf(rec2) != CodeInvalidArgument {
		t.Errorf("CodeOf(plain grpc) = %q, want INVALID_ARGUMENT", CodeOf(rec2))
	}

	// 3. 非 gRPC 错误：包装为 INTERNAL
	wrapped := FromGRPCError(fmt.Errorf("network broken"))
	if CodeOf(wrapped) != CodeInternal {
		t.Errorf("CodeOf(non-grpc) = %q, want INTERNAL", CodeOf(wrapped))
	}

	// 4. nil 直接返回 nil
	if FromGRPCError(nil) != nil {
		t.Error("FromGRPCError(nil) 应为 nil")
	}
}

// ---------------------------------------------------------------------------
// request_id 上下文透传
// ---------------------------------------------------------------------------

// TestRequestIDContext 验证 request_id 在 context 中的写入与读取。
func TestRequestIDContext(t *testing.T) {
	ctx := context.Background()
	if got := RequestIDFromContext(ctx); got != "" {
		t.Errorf("空 context 应返回空串，got %q", got)
	}

	ctx = NewContextWithRequestID(ctx, "req-ctx-1")
	if got := RequestIDFromContext(ctx); got != "req-ctx-1" {
		t.Errorf("RequestIDFromContext = %q, want req-ctx-1", got)
	}
}
