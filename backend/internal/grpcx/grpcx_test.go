package grpcx

import (
	"context"
	"net"
	"testing"

	"github.com/Steve5201/agent-backend/internal/errors"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// nopLogger 静默 logger。
func nopLogger() *zap.Logger { return zap.NewNop() }

// ---------------------------------------------------------------------------
// 拦截器单元测试
// ---------------------------------------------------------------------------

// TestUnaryRequestID_Propagates 验证入站 metadata 中的 request_id 被写入 context。
func TestUnaryRequestID_Propagates(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(metadataRequestID, "req-grpc-in"))
	interceptor := UnaryRequestID()

	var gotID string
	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
		gotID = errors.RequestIDFromContext(ctx)
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if gotID != "req-grpc-in" {
		t.Errorf("request_id = %q, want req-grpc-in", gotID)
	}
}

// TestUnaryRequestID_Generates 验证无 metadata 时生成新 ID。
func TestUnaryRequestID_Generates(t *testing.T) {
	interceptor := UnaryRequestID()
	var gotID string
	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
		gotID = errors.RequestIDFromContext(ctx)
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if gotID == "" {
		t.Error("应生成非空 request_id")
	}
}

// TestUnaryRecovery_Panic 验证 handler panic 被恢复为 Internal 错误。
func TestUnaryRecovery_Panic(t *testing.T) {
	interceptor := UnaryRecovery(nopLogger())
	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/test/Foo"}, func(context.Context, any) (any, error) {
		panic("boom")
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", status.Code(err))
	}
}

// TestUnaryClientRequestID_Inject 验证客户端拦截器把 context 的 request_id 写入出站 metadata。
func TestUnaryClientRequestID_Inject(t *testing.T) {
	ctx := errors.NewContextWithRequestID(context.Background(), "req-out-42")
	interceptor := UnaryClientRequestID()

	var sentID string
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		if md, ok := metadata.FromOutgoingContext(ctx); ok {
			if v := md.Get(metadataRequestID); len(v) > 0 {
				sentID = v[0]
			}
		}
		return nil
	}

	if err := interceptor(ctx, "/test/Foo", nil, nil, nil, invoker); err != nil {
		t.Fatalf("invoker error = %v", err)
	}
	if sentID != "req-out-42" {
		t.Errorf("出站 request_id = %q, want req-out-42", sentID)
	}
}

// TestUnaryClientRequestID_NoID 验证 context 无 request_id 时不影响调用。
func TestUnaryClientRequestID_NoID(t *testing.T) {
	interceptor := UnaryClientRequestID()
	called := false
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		called = true
		return nil
	}
	if err := interceptor(context.Background(), "/test/Foo", nil, nil, nil, invoker); err != nil {
		t.Fatalf("invoker error = %v", err)
	}
	if !called {
		t.Error("无 request_id 也应正常调用")
	}
}

// ---------------------------------------------------------------------------
// 端到端测试（bufconn + 真实生成服务）
// ---------------------------------------------------------------------------

// echoAuthServer 实现 AuthService.Me，回读服务端 context 中的 request_id。
type echoAuthServer struct {
	authpb.UnimplementedAuthServiceServer
	gotID string
}

func (s *echoAuthServer) Me(ctx context.Context, _ *authpb.MeRequest) (*authpb.MeResponse, error) {
	s.gotID = errors.RequestIDFromContext(ctx)
	return &authpb.MeResponse{}, nil
}

// TestEndToEnd_RequestIDPropagation 验证客户端 context → 出站 metadata →
// 服务端入站 metadata → 服务端 context 的全链路透传。
func TestEndToEnd_RequestIDPropagation(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	defer lis.Close()

	srv := NewServer(nopLogger())
	echo := &echoAuthServer{}
	authpb.RegisterAuthServiceServer(srv, echo)

	go func() {
		_ = srv.Serve(lis)
	}()
	defer srv.Stop()

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(UnaryClientRequestID()),
	)
	if err != nil {
		t.Fatalf("NewClient error = %v", err)
	}
	defer conn.Close()

	client := authpb.NewAuthServiceClient(conn)
	ctx := errors.NewContextWithRequestID(context.Background(), "req-e2e-99")
	if _, err := client.Me(ctx, &authpb.MeRequest{}); err != nil {
		t.Fatalf("Me error = %v", err)
	}
	if echo.gotID != "req-e2e-99" {
		t.Errorf("服务端收到 request_id = %q, want req-e2e-99", echo.gotID)
	}
}
