// agent 编排服务入口：会话管理、Agent 运行时调度（P2-D）。
//
// 服务形态（双端口）：
//   - gRPC 业务端口 :8082（GRPC_PORT 可覆盖）：AgentService
//     （CreateSession/ListSessions/GetSession/DeleteSession/Chat/StreamChat）
//   - HTTP 健康检查端口 :8182（HTTP_PORT 可覆盖）：GET /healthz
//     （默认 http_port 与 grpc_port 同为 8082，main 检测到冲突会自动偏移 +100，
//     也可用 HTTP_PORT 环境变量显式指定）
//
// 调用链：
//
//	gateway ──gRPC AgentService──▶ agent-service ──HTTP(OpenAI)──▶ llm-gateway ──▶ 厂商
//
// 启动示例（需先设置环境变量）：
//
//	$env:DB_PASSWORD='221434'; cd backend; go run ./cmd/agent
//
// 验证方式（需 llm-gateway 已在 :8083 运行）：
//
//	curl http://localhost:8182/healthz   # HTTP 健康检查
//	grpcurl -plaintext -H 'x-user-id: 1' -d '{"title":"测试"}' \
//	  localhost:8082 agent.v1.AgentService/CreateSession
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

	"github.com/Steve5201/agent-backend/internal/agentsvc"
	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/db"
	"github.com/Steve5201/agent-backend/internal/grpcx"
	"github.com/Steve5201/agent-backend/internal/logger"
	"github.com/Steve5201/agent-backend/internal/middleware"
	"github.com/Steve5201/agent-backend/internal/server"
	"github.com/Steve5201/agent-backend/migrations"
	"github.com/Steve5201/agent-framework/llm"
)

func main() {
	cfg, err := config.Load("agent", 8082)
	if err != nil {
		panic(err)
	}
	log := logger.Must(cfg.Env, cfg.LogLevel)
	defer func() { _ = log.Sync() }()

	// 1. 生命周期信号（SIGINT/SIGTERM）→ 优雅关闭两个监听。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 2. 数据库：连接池 + 启动即迁移（幂等，已应用的迁移自动跳过）。
	pool, err := db.Connect(ctx, cfg.DB.DSN(), db.Options{})
	if err != nil {
		log.Fatal("connect database", zap.Error(err))
	}
	defer pool.Close()
	if err := db.MigrateUp(ctx, cfg.DB.DSN(), "agent", migrations.FS); err != nil {
		log.Fatal("run migrations", zap.Error(err))
	}
	log.Info("database ready and migrated")

	// 3. 上游 Provider：指向 llm-gateway（密钥收敛在网关一侧，agent 不持真实 key）。
	provider, err := llm.NewOpenAICompatible(llm.Config{
		Name:       "llm-gateway",
		BaseURL:    cfg.Agent.LLMBaseURL,
		APIKey:     cfg.Agent.LLMAPIKey, // 占位 key（网关不校验 Authorization）
		Model:      cfg.Agent.Model,
		Timeout:    cfg.LLM.Timeout, // 与 llm-gateway 一致（LLM_TIMEOUT，默认 300s）：编排子任务非流式请求大、生成久，过短易触发上游超时 504
		MaxRetries: 0,               // 0=不重试（上游错误状态由网关映射，agent 层不隐式重试）
	})
	if err != nil {
		log.Fatal("init llm provider", zap.Error(err))
	}

	// 4. 工具集 + 业务服务。
	//    Skill / MCP 外部能力源统一经 tools.ToolProvider 注入（见 agentsvc.WithProviders）。
	//    构建逻辑在 toolset.go（启动与热加载复用同一份实现）。
	reg, closeTools, err := buildToolRegistry(cfg, log)
	if err != nil {
		log.Fatal("build tool registry", zap.Error(err))
	}
	svc, err := agentsvc.NewService(agentsvc.Config{
		Repo:             agentsvc.NewPostgresRepository(pool),
		Provider:         provider,
		Registry:         reg,
		Log:              log,
		Model:            cfg.Agent.Model,
		SystemPrompt:     cfg.Agent.SystemPrompt,
		MaxRounds:        cfg.Agent.MaxRounds,
		MaxMessages:      cfg.Agent.MaxMessages,
		AutoApproveTools: cfg.Agent.AutoApproveTools,
		FilesBaseURL:     cfg.Agent.FilesBaseURL,
		// 聊天上传文档（模块二）：工作区根缺省进程 cwd（容器内 /app = 沙盒 /work），
		// 解析沙盒地址缺省复用 code_executor 沙盒（AGENT_CHAT_SANDBOX_URL 可覆盖）。
		WorkRoot:          cfg.Agent.WorkRoot,
		ChatSandboxURL:    cfg.Agent.ChatSandboxURL,
		ChatSandboxUserID: cfg.RAG.SandboxUserID,
		// 聊天上传文档限制（P4-L 收口 env）：缺省回退内置默认，与 gateway 对齐。
		ChatDocMaxSizeMB:   cfg.Agent.ChatDocMaxSizeMB,
		ChatDocsPerSession: cfg.Agent.ChatDocsPerSession,
		ChatDocInjectRunes: cfg.Agent.ChatDocInjectRunes,
		// 编排子任务韧性（P4-L 收口 env）：超时秒数 + 失败重试次数。
		OrchSubtaskTimeoutSec: cfg.Agent.OrchSubtaskTimeoutSec,
		OrchSubtaskRetries:    cfg.Agent.OrchSubtaskRetries,
		// 文档生成（render_document）限制（P4-L）。
		DocLimits: cfg.Doc,
		// 按域返回资源/工具清单（多智能体切换时配置区跟随）。
		DomainView: newDomainViewer(cfg, log),
	})
	if err != nil {
		log.Fatal("init agent service", zap.Error(err))
	}

	// 4.1 管理端热加载：监听技能目录 + MCP 配置文件，保存即生效（免重启）。
	//     进行中的会话不受影响，新会话立即使用新工具集。
	stopReload := startReloader(svc, cfg, log, closeTools)
	defer stopReload()

	// 5. 启动 gRPC 业务服务（统一拦截器：request_id → recovery → 日志）。
	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
	if err != nil {
		log.Fatal("listen gRPC", zap.Error(err))
	}
	grpcSrv := grpcx.NewServer(log)
	agentsvc.RegisterAgentService(grpcSrv, agentsvc.NewGrpcServer(svc, log))

	// 6. 启动 HTTP 健康检查；与 gRPC 同端口时自动偏移（日志明示实际端口）。
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

	// 7. 生命周期：任一监听失败即退出；收到信号后优雅关闭两个服务。
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
	log.Info("agent service stopped gracefully")
}

// newHTTPHandler 组装 HTTP 路由：健康检查 + /files 本地媒体静态服务。
// 后续如需扩展在此注册。
func newHTTPHandler(log *zap.Logger) http.Handler {
	mux := http.NewServeMux()
	server.RegisterHealthz(mux)
	// /files：只读服务智能体工作目录内文件（与 file_ops 同路径边界），
	// 供前端渲染本地图片/视频（P2-N 本地媒体交叉项）。
	mux.Handle("/files/", filesHandler{log: log})
	// 统一中间件链：request_id → 访问日志 → panic 恢复。
	return middleware.Chain(mux,
		middleware.RequestID(),
		middleware.Logger(log),
		middleware.Recovery(log),
	)
}
