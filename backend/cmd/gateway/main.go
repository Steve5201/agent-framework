// gateway 网关服务入口：系统唯一对外 HTTP 入口（P2-E）。
//
// 职责：JWT 鉴权校验、路由分发、IP/用户双维限流、SSE 透传；
// 不持有业务数据与模型密钥，全部转发到下游 gRPC 服务。
//
// 服务形态：
//
//	浏览器/客户端 ──HTTP :8080──▶ gateway ──gRPC──▶ auth(:8081) / agent(:8082)
//
// 启动示例（gateway 不连数据库，仅需 JWT_SECRET 与下游服务在运行）：
//
//	$env:JWT_SECRET='dev-secret-change-me'; cd backend; go run ./cmd/gateway
//
// 验证方式：
//
//	curl http://localhost:8080/healthz                       # 健康检查
//	curl http://localhost:8080/v1/openapi.yaml               # 接口文档
//	curl http://localhost:8080/swagger/ui                    # Swagger UI
package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/auth"
	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/gatewaysvc"
	"github.com/Steve5201/agent-backend/internal/logger"
	"github.com/Steve5201/agent-backend/internal/ratelimit"
	"github.com/Steve5201/agent-backend/internal/server"
)

func main() {
	// gateway 不连数据库：跳过 DB_PASSWORD 必填校验。
	cfg, err := config.LoadWith("gateway", 8080, false)
	if err != nil {
		panic(err)
	}
	log := logger.Must(cfg.Env, cfg.LogLevel)
	defer func() { _ = log.Sync() }()

	// 1. 生命周期信号 → 优雅关闭。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 2. JWT 校验器：与 auth-service 使用同一 JWT_SECRET 与 TTL，
	//    gateway 本地校验 access token（不把 token 发给下游）。
	jwtMgr, err := auth.New(auth.Config{
		Secret:     cfg.JWT.Secret,
		AccessTTL:  cfg.JWT.AccessTTL,
		RefreshTTL: cfg.JWT.RefreshTTL,
		Issuer:     "agent-backend",
	})
	if err != nil {
		log.Fatal("init jwt manager", zap.Error(err))
	}

	// 3. 连接下游 gRPC 服务。
	clients, closeClients, err := gatewaysvc.NewClients(ctx, cfg.Gateway.AuthAddr, cfg.Gateway.AgentAddr, cfg.Gateway.RagAddr, jwtMgr, log)
	if err != nil {
		log.Fatal("connect downstream services", zap.Error(err))
	}
	defer closeClients()
	log.Info("downstream connected",
		zap.String("auth_addr", cfg.Gateway.AuthAddr),
		zap.String("agent_addr", cfg.Gateway.AgentAddr),
		zap.String("rag_addr", cfg.Gateway.RagAddr),
	)

	// 3.1 注入管理端（admin panel）文件态配置路径（需与 agent 的 AGENT_SKILLS_DIR
	// / AGENT_MCP_CONFIG_FILE 指向同一目录/文件，agent 监听它们热加载）。
	clients.AdminSkillsDir = cfg.Admin.SkillsDir
	clients.AdminMcpConfigFile = cfg.Admin.McpConfigFile
	clients.AdminMcpServersDir = cfg.Admin.McpServersDir
	clients.AdminLogsDir = cfg.Admin.LogsDir
	// 管理端上传限制（P4-L 收口 env；与 adminsvc 内置默认对齐）。
	clients.AdminKbUploadMaxMB = cfg.Admin.KbUploadMaxMB
	clients.AdminSkillUploadMaxMB = cfg.Admin.SkillUploadMaxMB
	// 用量聚合查询 llm-gateway（P2-AI；与 agent 上游同基址，不带 /v1）。
	clients.LlmGatewayBaseURL = cfg.Gateway.LlmBaseURL
	// 模型注册表管理令牌（P3 大模型管理：与 llm-gateway 共享 LLM_ADMIN_TOKEN）。
	clients.LlmAdminToken = cfg.Gateway.LlmAdminToken
	// agent-service HTTP 基址：gateway 反代其 /files 端点（用户/AI 气泡图片统一
	// 经 gateway 拉取，避免图片 URL 指向 8080 无 /files 路由而渲染失败）。
	clients.AgentHTTPAddr = cfg.Gateway.AgentHTTPAddr
	// 聊天上传单文档大小上限（须与 agent 侧 AGENT_CHAT_DOC_MAX_SIZE_MB 一致）。
	clients.ChatDocMaxBytes = int64(cfg.Gateway.ChatUploadMaxMB) << 20

	// 4. 组装 HTTP 服务（IP 与用户维度共用限流参数，可在 .env 分别覆盖）。
	limit := ratelimit.Config{Rate: cfg.RateLimit.Rate, Burst: cfg.RateLimit.Burst}
	handler := clients.Routes(cfg.Gateway.CORSOrigins, limit, limit)

	// 5. 启动（server.Run 内部处理优雅关闭与退出码）。
	if err := server.Run(server.Option{
		Name:    cfg.ServiceName,
		Addr:    fmt.Sprintf(":%d", cfg.HTTPPort),
		Logger:  log,
		Handler: handler,
	}); err != nil {
		log.Fatal("gateway exited", zap.Error(err))
	}
}
