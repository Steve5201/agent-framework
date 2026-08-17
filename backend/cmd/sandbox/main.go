// cmd/sandbox —— 独立容器沙盒服务（阶段2·L1+L2）。
//
// 负责统一执行 agent 侧 code_executor 委托的代码（shell / python），进程级隔离：
//   - 每用户独立工作区（/work/users/<user_id>）且每用户映射独立 uid；
//   - unshare -n 禁网络（执行进程无任何网卡）；
//   - prlimit 资源限制（CPU 时间 / 虚拟内存 / 打开文件数）；
//   - setpriv 降权到该用户的专属 uid（SANDBOX_UID_BASE+user_id，uid==gid）；
//   - 超时终止进程组 + 危险命令黑名单/白名单静态拦截。
//
// 安全模型：sandbox 容器自身 cap_drop ALL + cap_add SYS_ADMIN（供 unshare）、
// read_only 根文件系统、不发布宿主端口，仅 compose 内部网络被 agent 调用。
//
// 环境变量（均有默认值）：
//
//	HTTP_PORT                  监听端口（默认 8087，compose 内部）
//	SANDBOX_WORK_ROOT          工作区根目录（默认 /work）
//	SANDBOX_MEMORY_MB          单进程虚拟内存上限（默认 2048；equation_png 等
//	matplotlib/numpy 脚本在 512MB 下 import 即 OOM，见 compose 注释）
//	SANDBOX_CPU_SECONDS        单进程 CPU 时间上限秒（默认 120）
//	SANDBOX_NOFILE             单进程打开文件数上限（默认 1024）
//	SANDBOX_MAX_TIMEOUT_SECONDS 单次执行最大超时（默认 300；300 覆盖
//	matplotlib 字体扫描与复杂脚本，如构建 PPT）
//	SANDBOX_MAX_WORKERS        最大并发执行数（默认 4）
//	SANDBOX_AGENT_UID          协作 agent 用户 uid（默认 100，用户工作区属主=派生uid）
//	SANDBOX_AGENT_GID          协作 agent 用户 gid（默认 101，app 组；用户工作区属组）
//	SANDBOX_UID_BASE           沙盒执行用户 uid 池起点（默认 2000，uid=起点+user_id）
//	SANDBOX_CODE_EXEC_ALLOWLIST 命令白名单正则（逗号分隔，默认空）
//	SANDBOX_PARSERS_DIR        预置解析脚本目录（默认 /opt/rag-parsers，profile 模式使用）
//	SANDBOX_CLEANUP_INTERVAL_HOURS 工作区清理周期小时（默认 6；0=禁用）
//	SANDBOX_CLEANUP_TTL_HOURS  过期目录保留小时（默认 168=7 天；chat-files/ingest 白名单）
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/logger"
	"github.com/Steve5201/agent-backend/internal/sandboxsvc"
	"github.com/Steve5201/agent-backend/internal/server"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.LoadWith("sandbox", 8087, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load config:", err)
		os.Exit(1)
	}
	log := logger.Must(cfg.Env, cfg.LogLevel)

	executor := sandboxsvc.NewExecutor(sandboxsvc.Config{
		WorkRoot:             getenv("SANDBOX_WORK_ROOT", "/work"),
		MemoryLimitMB:        getenvInt64("SANDBOX_MEMORY_MB", 2048),
		ProfileMemoryLimitMB: getenvInt64("SANDBOX_PROFILE_MEMORY_MB", 2048),
		CPUSeconds:           getenvInt64("SANDBOX_CPU_SECONDS", 120),
		NofileLimit:          getenvInt64("SANDBOX_NOFILE", 1024),
		MaxTimeout:           time.Duration(getenvInt("SANDBOX_MAX_TIMEOUT_SECONDS", 300)) * time.Second,
		Allowlist:            splitComma(os.Getenv("SANDBOX_CODE_EXEC_ALLOWLIST")),
		AgentUID:             getenvInt("SANDBOX_AGENT_UID", 100),
		AgentGID:             getenvInt("SANDBOX_AGENT_GID", 101),
		UIDBase:              getenvInt("SANDBOX_UID_BASE", 2000),
		ParsersDir:           getenv("SANDBOX_PARSERS_DIR", "/opt/rag-parsers"),
		Log:                  log,
	})
	srv := sandboxsvc.NewServer(sandboxsvc.ServerConfig{
		MaxWorkers: getenvInt("SANDBOX_MAX_WORKERS", 4),
		Log:        log,
		Executor:   executor,
	})

	// 工作区定期清理（模块三）：TTL 默认 7 天；间隔 0 = 禁用。
	workRoot := getenv("SANDBOX_WORK_ROOT", "/work")
	cleanupInterval := time.Duration(getenvInt("SANDBOX_CLEANUP_INTERVAL_HOURS", 6)) * time.Hour
	cleanupTTL := time.Duration(getenvInt("SANDBOX_CLEANUP_TTL_HOURS", 168)) * time.Hour
	if cleanupInterval > 0 {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		go runCleanupLoop(ctx, sandboxsvc.NewCleaner(workRoot, cleanupTTL, log), cleanupInterval, log)
	}

	addr := ":" + strconv.Itoa(cfg.HTTPPort)
	if err := server.Run(server.Option{
		Name:    "sandbox",
		Addr:    addr,
		Logger:  log,
		Handler: srv.Handler(),
	}); err != nil {
		log.Fatal("sandbox exited", zap.Error(err))
	}
}

// runCleanupLoop 周期执行工作区清理（模块三），直到 ctx 取消（服务优雅退出）。
// 仅在有清理产出时输出 Info，避免周期空转刷日志。
func runCleanupLoop(ctx context.Context, c *sandboxsvc.Cleaner, interval time.Duration, log *zap.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			stats, err := c.Run(ctx)
			if err != nil {
				log.Warn("workspace cleanup failed", zap.Error(err))
				continue
			}
			if stats.DirsDeleted > 0 {
				log.Info("workspace cleanup done", zap.Any("stats", stats))
			}
		}
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
