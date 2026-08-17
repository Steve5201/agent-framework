// auth 认证服务入口：注册/登录、JWT 双令牌、RBAC 权限（P2-B）。
//
// 服务形态（双端口）：
//   - gRPC 业务端口 :8081（GRPC_PORT 可覆盖）：AuthService（Register/Login/Refresh/Logout/Me）
//   - HTTP 健康检查端口 :8181（HTTP_PORT 可覆盖）：GET /healthz
//     （默认 http_port 与 grpc_port 同为 8081，main 检测到冲突会自动偏移 +100，
//     也可用 HTTP_PORT 环境变量显式指定）
//
// 启动示例（需先设置环境变量）：
//
//	$env:DB_PASSWORD='221434'; $env:JWT_SECRET='dev-secret-please-change'; cd backend; go run ./cmd/auth
//
// 验证方式：
//
//	curl http://localhost:8181/healthz   # HTTP 健康检查
//	grpcurl -plaintext -d '{"username":"alice","password":"Passw0rd-2026"}' \
//	  localhost:8081 auth.v1.AuthService/Register
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/auth"
	"github.com/Steve5201/agent-backend/internal/authsvc"
	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/db"
	"github.com/Steve5201/agent-backend/internal/grpcx"
	"github.com/Steve5201/agent-backend/internal/logger"
	"github.com/Steve5201/agent-backend/internal/middleware"
	"github.com/Steve5201/agent-backend/internal/server"
	"github.com/Steve5201/agent-backend/migrations"
)

// defaultAdminPassword AUTH_ADMIN_PASSWORD 未设置时的内置引导密码。
// 仅用于本地开发/首次启动引导，生产环境必须通过环境变量覆盖并尽快修改。
const defaultAdminPassword = "Admin@2026"

func main() {
	cfg, err := config.Load("auth", 8081)
	if err != nil {
		panic(err)
	}
	log := logger.Must(cfg.Env, cfg.LogLevel)
	defer func() { _ = log.Sync() }()

	// JWT_SECRET 仅 auth-service 需要；缺失时给出明确提示（避免 auth.New 泛化报错）。
	if cfg.JWT.Secret == "" {
		log.Fatal("环境变量 JWT_SECRET 未设置，auth 服务拒绝启动")
	}

	// 1. 生命周期信号（SIGINT/SIGTERM）→ 优雅关闭两个监听。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 2. 数据库：连接池 + 启动即迁移（幂等，已应用的迁移自动跳过）。
	pool, err := db.Connect(ctx, cfg.DB.DSN(), db.Options{})
	if err != nil {
		log.Fatal("connect database", zap.Error(err))
	}
	defer pool.Close()
	if err := db.MigrateUp(ctx, cfg.DB.DSN(), "auth", migrations.FS); err != nil {
		log.Fatal("run migrations", zap.Error(err))
	}
	log.Info("database ready and migrated")

	// 3. 组装认证业务：JWT 管理器 + 领域服务（Repository 注入 PostgreSQL 实现）。
	mgr, err := auth.New(auth.Config{
		Secret:     cfg.JWT.Secret,
		AccessTTL:  cfg.JWT.AccessTTL,
		RefreshTTL: cfg.JWT.RefreshTTL,
		Issuer:     "agent-auth",
	})
	if err != nil {
		log.Fatal("init jwt manager", zap.Error(err))
	}
	svc := authsvc.NewService(authsvc.Config{
		Repo:             authsvc.NewPostgresRepository(pool),
		JWT:              mgr,
		AccessTTL:        cfg.JWT.AccessTTL,
		RefreshTTL:       cfg.JWT.RefreshTTL,
		MaxLoginAttempts: 5,               // 连续失败 5 次锁定（可后续配置化）
		LoginLockWindow:  5 * time.Minute, // 锁定 5 分钟
	})

	// 3.1 播种初始超级管理员（管理端入口）。仅在账号不存在时创建，
	// 已有账号不会被修改。密码为空时使用内置引导默认值并告警（应尽快修改）。
	if cfg.Auth.AdminUsername != "" {
		adminPwd := cfg.Auth.AdminPassword
		usingDefault := adminPwd == ""
		if usingDefault {
			adminPwd = defaultAdminPassword
			log.Warn("AUTH_ADMIN_PASSWORD 未设置，使用内置引导默认密码（生产环境务必通过环境变量覆盖并尽快修改）",
				zap.String("admin_username", cfg.Auth.AdminUsername))
		}
		created, err := svc.EnsureAdmin(ctx, cfg.Auth.AdminUsername, adminPwd)
		if err != nil {
			log.Fatal("seed admin account", zap.Error(err))
		}
		if created {
			log.Info("初始超级管理员已创建",
				zap.String("admin_username", cfg.Auth.AdminUsername),
				zap.Bool("using_default_password", usingDefault))
		}
	}

	// 3.2 播种默认智能体 tutor（owner = 首个最高超管），幂等。
	// 保证 /agent/tutor 等默认智能体域在注册表中有记录，供资源归属使用。
	if err := svc.EnsureDefaultAgent(ctx); err != nil {
		log.Fatal("seed default agent", zap.Error(err))
	}

	// 4. 启动 gRPC 业务服务（统一拦截器：request_id → recovery → 日志）。
	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
	if err != nil {
		log.Fatal("listen gRPC", zap.Error(err))
	}
	grpcSrv := grpcx.NewServer(log)
	authsvc.RegisterAuthService(grpcSrv, svc)

	// 5. 启动 HTTP 健康检查；与 gRPC 同端口时自动偏移（日志明示实际端口）。
	httpPort := cfg.HTTPPort
	if httpPort == cfg.GRPCPort {
		httpPort = cfg.GRPCPort + 100
		log.Warn("HTTP 健康检查端口与 gRPC 冲突，已自动偏移（可用 HTTP_PORT 显式指定）",
			zap.Int("health_port", httpPort),
			zap.Int("grpc_port", cfg.GRPCPort))
	}
	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", httpPort),
		Handler:           newHTTPHandler(log),
		ReadHeaderTimeout: 5 * time.Second, // 防慢速连接耗尽连接池
	}

	errCh := make(chan error, 2)
	go func() { errCh <- grpcx.Serve(grpcSrv, grpcLis, log, cfg.ServiceName) }()
	go func() {
		log.Info("http service listening", zap.String("service", cfg.ServiceName+"-http"), zap.String("addr", httpSrv.Addr))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// 6. 生命周期：任一监听失败即退出；收到信号后优雅关闭两个服务。
	select {
	case err := <-errCh:
		log.Fatal("service exited", zap.Error(err))
	case <-ctx.Done():
		log.Info("shutdown signal received, stopping servers")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// gRPC 优雅停止：等待处理中的 RPC 完成。
	stopped := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(stopped)
	}()
	// HTTP 优雅关闭。
	_ = httpSrv.Shutdown(shutdownCtx)

	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		grpcSrv.Stop() // 超时强停
	}
	log.Info("auth service stopped gracefully")
}

// newHTTPHandler 组装 HTTP 路由（当前仅健康检查，后续如需扩展在此注册）。
func newHTTPHandler(log *zap.Logger) http.Handler {
	mux := http.NewServeMux()
	server.RegisterHealthz(mux)
	// 统一中间件链：request_id → 访问日志 → panic 恢复。
	return middleware.Chain(mux,
		middleware.RequestID(),
		middleware.Logger(log),
		middleware.Recovery(log),
	)
}
