// Package server 提供 HTTP 服务的基础骨架：
//   - /healthz 健康检查（统一注册）
//   - 优雅关闭（SIGINT/SIGTERM）
//   - 启动/停止日志
//
// 所有微服务复用本包，保证生命周期行为一致。
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
)

// Option 服务启动配置。
type Option struct {
	Name    string        // 服务名（日志标识）
	Addr    string        // 监听地址，如 ":8081"
	Logger  *zap.Logger   // 日志实例
	Handler http.Handler  // 业务路由；为 nil 时使用空 mux
}

// Run 启动 HTTP 服务并阻塞。
// 收到 SIGINT/SIGTERM 后执行优雅关闭（最多等待 10s），返回 nil。
func Run(opt Option) error {
	if opt.Handler == nil {
		opt.Handler = http.NewServeMux()
	}
	srv := &http.Server{
		Addr:              opt.Addr,
		Handler:           opt.Handler,
		ReadHeaderTimeout: 5 * time.Second, // 防止慢速连接耗尽连接池
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		opt.Logger.Info(fmt.Sprintf("%s service listening", opt.Name), zap.String("addr", opt.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		opt.Logger.Info("shutdown signal received, graceful stopping")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		opt.Logger.Info("server stopped gracefully")
		return nil
	}
}

// RegisterHealthz 注册统一健康检查路由 GET /healthz，返回 {"status":"ok"}。
func RegisterHealthz(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}
