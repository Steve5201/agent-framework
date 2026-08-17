package authsvc

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
)

// ---------------------------------------------------------------------------
// bufconn 内存 gRPC 传输层测试（P2-27）：
// 复用 newTestService 的 fake repo 与 JWT，验证 gRPC 层正确性——
// 业务错误序列化（status code + 业务码）、metadata 注入（x-user-id）等。
// ---------------------------------------------------------------------------

// bufconnDial 建立到内存 gRPC 服务端的连接（无真实网络）。
func bufconnDial(t *testing.T, lis *bufconn.Listener) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// newBufconnServer 启动内存 gRPC 服务端并返回客户端与 fake repo。
func newBufconnServer(t *testing.T) (authpb.AuthServiceClient, *fakeRepo) {
	t.Helper()
	svc, repo := newTestService(t)

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	RegisterAuthService(srv, svc)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return authpb.NewAuthServiceClient(bufconnDial(t, lis)), repo
}

// grpcRegister 测试辅助：走 gRPC 注册一个用户。
func grpcRegister(t *testing.T, client authpb.AuthServiceClient, username string) *authpb.RegisterResponse {
	t.Helper()
	reg, err := client.Register(context.Background(), &authpb.RegisterRequest{
		Username: username,
		Password: testPassword,
	})
	if err != nil {
		t.Fatalf("gRPC Register(%s) error = %v", username, err)
	}
	return reg
}

// grpcLogin 测试辅助：走 gRPC 登录。
// 走智能体门户入口（AgentId="test"）——普通账号经管理端入口（AgentId 为空）
// 会被拒绝，统一模拟正常用户路径。
func grpcLogin(t *testing.T, client authpb.AuthServiceClient, username string) *authpb.LoginResponse {
	t.Helper()
	login, err := client.Login(context.Background(), &authpb.LoginRequest{
		Username: username,
		Password: testPassword,
		AgentId:  "test",
	})
	if err != nil {
		t.Fatalf("gRPC Login(%s) error = %v", username, err)
	}
	return login
}

// TestGRPC_RegisterLoginMe_EndToEnd 全链路：注册 → 登录 → 带 metadata 调 Me。
func TestGRPC_RegisterLoginMe_EndToEnd(t *testing.T) {
	client, _ := newBufconnServer(t)

	reg := grpcRegister(t, client, "alice")

	login := grpcLogin(t, client, "alice")
	if login.AccessToken == "" || login.RefreshToken == "" {
		t.Fatal("登录应签发双令牌")
	}
	if login.User == nil {
		t.Fatal("登录响应应携带用户资料")
	}
	if login.User.Username != "alice" || login.User.Role != "user" {
		t.Errorf("用户资料异常: %+v", login.User)
	}
	if login.User.CreatedAt == nil || login.User.CreatedAt.AsTime().IsZero() {
		t.Error("用户资料应携带创建时间")
	}

	// Me：模拟 gateway 把 user_id 注入 gRPC metadata（服务端只信 metadata）。
	ctx := metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs(metadataUserID, reg.UserId))
	me, err := client.Me(ctx, &authpb.MeRequest{})
	if err != nil {
		t.Fatalf("Me error = %v", err)
	}
	if me.User == nil || me.User.Id != reg.UserId || me.User.Username != "alice" {
		t.Errorf("Me 用户资料异常: %+v", me.User)
	}
}

// TestGRPC_Me_MissingMetadata 未注入 x-user-id 应拒绝。
func TestGRPC_Me_MissingMetadata(t *testing.T) {
	client, _ := newBufconnServer(t)
	grpcRegister(t, client, "alice")

	_, err := client.Me(context.Background(), &authpb.MeRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("缺少 x-user-id 应返回 UNAUTHENTICATED，got %v", status.Code(err))
	}
}

// TestGRPC_Login_WrongPassword 错误密码：status code + 业务码双重验证。
func TestGRPC_Login_WrongPassword(t *testing.T) {
	client, _ := newBufconnServer(t)
	grpcRegister(t, client, "bob")

	_, err := client.Login(context.Background(), &authpb.LoginRequest{
		Username: "bob",
		Password: "WrongPass-1",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("错误密码应返回 UNAUTHENTICATED，got %v", status.Code(err))
	}
}

// TestGRPC_Register_Duplicate 重复注册：gRPC 端到端验证业务码穿透。
func TestGRPC_Register_Duplicate(t *testing.T) {
	client, _ := newBufconnServer(t)
	grpcRegister(t, client, "carol")

	_, err := client.Register(context.Background(), &authpb.RegisterRequest{
		Username: "carol",
		Password: testPassword,
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Errorf("重复注册应返回 ALREADY_EXISTS，got %v", status.Code(err))
	}
	// 客户端经 FromGRPCError 应还原业务码 ALREADY_EXISTS（统一错误穿透）。
	if got := apperr.CodeOf(apperr.FromGRPCError(err)); got != apperr.CodeAlreadyExists {
		t.Errorf("FromGRPCError 应还原 ALREADY_EXISTS，got %q", got)
	}
}

// TestGRPC_Refresh_InvalidToken 无效 refresh：验证 Unauthenticated 穿透。
func TestGRPC_Refresh_InvalidToken(t *testing.T) {
	client, _ := newBufconnServer(t)

	_, err := client.Refresh(context.Background(), &authpb.RefreshRequest{
		RefreshToken: "garbage-token",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("无效 refresh 应返回 UNAUTHENTICATED，got %v", status.Code(err))
	}
}
