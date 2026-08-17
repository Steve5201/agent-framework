// llm-gateway 大模型网关入口：统一出口、用量统计、限流配额（P2-C）。
//
// 定位：agent-service 等内部服务不再直连厂商，而是统一走本网关——
// 真实 API Key 只存在于本服务；每请求写 usage_logs（成本可控）；按用户
// 限流与配额。
//
// 服务形态（HTTP）：
//   - POST /v1/chat/completions   OpenAI 兼容对话（非流式 + SSE 流式）
//   - GET  /v1/usage/agents/{id}   智能体域用量聚合
//   - GET  /v1/usage/overview      平台用量总览（数据管理模块，X-Admin-Token 保护）
//   - GET  /v1/models              公开模型列表（会话配置区渲染用，无密钥）
//   - /v1/admin/models*            模型注册表管理端点（X-Admin-Token 保护）
//   - GET  /healthz                健康检查
//
// 启动示例（需先设置环境变量）：
//
//	$env:DB_PASSWORD='221434'; $env:DEEPSEEK_API_KEY='sk-xxx'; cd backend; go run ./cmd/llm-gateway
//
// 模型注册表（P3 大模型管理）：models 表为单一事实源。首次启动且表为空时，
// 若配置了 DEEPSEEK_API_KEY 则从环境播种一个默认模型（向后兼容）；未配置
// 密钥（本地模型部署）则留空注册表，等待管理端添加模型。
//
// 验证方式：
//
//	curl -X POST http://localhost:8083/v1/chat/completions \
//	  -H "Content-Type: application/json" -H "X-User-Id: 1" \
//	  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"你好"}],"stream":false}'
package main

import (
	"context"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/db"
	"github.com/Steve5201/agent-backend/internal/llmsvc"
	"github.com/Steve5201/agent-backend/internal/logger"
	"github.com/Steve5201/agent-backend/internal/middleware"
	"github.com/Steve5201/agent-backend/internal/server"
	"github.com/Steve5201/agent-backend/migrations"
	"github.com/Steve5201/agent-framework/llm"
)

func main() {
	cfg, err := config.Load("llm-gateway", 8083)
	if err != nil {
		panic(err)
	}
	log := logger.Must(cfg.Env, cfg.LogLevel)
	defer func() { _ = log.Sync() }()

	// 密钥检查放宽（P3 模型管理）：不再强制 DEEPSEEK_API_KEY——本地模型
	// 部署（Ollama 等）无需任何密钥，注册表留空等管理端添加即可。
	// 仅当"无密钥 且 注册表为空"时提示，不阻断启动。
	if cfg.LLM.APIKey == "" {
		log.Warn("环境变量 DEEPSEEK_API_KEY 未设置：若注册表为空，对话请求将返回" +
			"「模型注册表为空」，请通过管理端添加模型（云端模型需密钥，本地模型可留空）")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 数据库：连接池 + 启动即迁移（usage_logs / models 表）。
	pool, err := db.Connect(ctx, cfg.DB.DSN(), db.Options{})
	if err != nil {
		log.Fatal("connect database", zap.Error(err))
	}
	defer pool.Close()
	if err := db.MigrateUp(ctx, cfg.DB.DSN(), "llm", migrations.FS); err != nil {
		log.Fatal("run migrations", zap.Error(err))
	}
	log.Info("database ready and migrated")

	// 模型注册表（P3）：存储 + 运行期镜像；首次空表播种环境默认模型，
	// 并批量播种 ADMIN_MODELS 中登记的管理模型（学校本地网关等多模型接入）。
	modelStore := llmsvc.NewModelStore(pool)
	if err := seedDefaultModel(ctx, modelStore, cfg, log); err != nil {
		log.Fatal("seed default model", zap.Error(err))
	}
	if err := llmsvc.SeedAdminModels(ctx, modelStore, cfg.LLM.AdminModelsJSON, log); err != nil {
		log.Fatal("seed admin models", zap.Error(err))
	}
	registry := llmsvc.NewRegistry()
	if specs, err := modelStore.ListModels(ctx); err != nil {
		log.Fatal("load models", zap.Error(err))
	} else {
		registry.Reload(specs, log)
	}

	// 兼容单上游模式的上游客户端（注册表条目构造失败时的兜底；通常由
	// 注册表条目提供 provider，本客户端仅在旧配置未播种时留作 fallback）。
	fallback, _ := llm.NewOpenAICompatible(llm.Config{
		Name:       "deepseek-upstream",
		BaseURL:    cfg.LLM.UpstreamBaseURL,
		APIKey:     cfg.LLM.APIKey,
		Model:      cfg.LLM.Model,
		Timeout:    cfg.LLM.Timeout,
		MaxRetries: cfg.LLM.MaxRetries,
	})

	// 网关处理器：OpenAI 兼容端点 + 用量落库 + 限流/配额 + 注册表路由。
	handler := llmsvc.NewHandler(llmsvc.HandlerConfig{
		Log:                  log,
		Provider:             fallback,
		Registry:             registry,
		Usage:                llmsvc.NewUsageStore(pool),
		RequestRate:          cfg.LLM.RequestRate,
		RequestBurst:         cfg.LLM.RequestBurst,
		TokenQuotaMonth:      cfg.LLM.TokenQuotaMonth,
		AdminTokenQuotaMonth: cfg.LLM.AdminTokenQuotaMonth,
		QuotaStore:           llmsvc.NewQuotaStore(pool),
		Model:                cfg.LLM.Model,
		PromptPricePer1M:     cfg.LLM.PromptPricePer1M,
		CompletionPricePer1M: cfg.LLM.CompletionPricePer1M,
	})

	mux := http.NewServeMux()
	handler.Register(mux)
	// 模型注册表管理端点（令牌保护）+ 公开模型列表。
	modelAdmin := llmsvc.NewModelAdmin(modelStore, registry, cfg.LLM.AdminToken, log)
	modelAdmin.RegisterAdmin(mux)
	modelAdmin.RegisterPublic(mux)
	// 管理端用量总览（数据管理模块，令牌保护）。
	usageAdmin := llmsvc.NewUsageAdmin(handler.Usage(), cfg.LLM.AdminToken, log)
	usageAdmin.RegisterAdmin(mux)
	// 管理端用户配额（令牌保护）。
	quotaAdmin := llmsvc.NewQuotaAdmin(llmsvc.NewQuotaStore(pool), cfg.LLM.AdminToken, log)
	quotaAdmin.RegisterAdmin(mux)
	server.RegisterHealthz(mux)

	httpHandler := middleware.Chain(mux,
		middleware.RequestID(),
		middleware.Logger(log),
		middleware.Recovery(log),
	)

	if err := server.Run(server.Option{
		Name:    cfg.ServiceName,
		Addr:    fmt.Sprintf(":%d", cfg.HTTPPort),
		Logger:  log,
		Handler: httpHandler,
	}); err != nil {
		log.Fatal("server exit", zap.Error(err))
	}
}

// seedDefaultModel 首次启动（models 表为空）时用环境配置播种默认模型，
// 保证既有部署无需任何管理操作即可对话（向后兼容）；注册表已有条目
// 或未配置 DEEPSEEK_API_KEY（本地模型部署）时跳过。
func seedDefaultModel(ctx context.Context, store llmsvc.ModelStore, cfg *config.Config, log *zap.Logger) error {
	specs, err := store.ListModels(ctx)
	if err != nil {
		return err
	}
	if len(specs) > 0 {
		return nil // 已有模型：尊重管理端配置，不覆盖
	}
	if cfg.LLM.APIKey == "" {
		return nil // 无密钥：留空注册表，等管理端添加（含本地模型）
	}
	spec := llmsvc.ModelSpec{
		Name:                 cfg.LLM.Model,
		ProviderName:         "deepseek",
		BaseURL:              cfg.LLM.UpstreamBaseURL,
		APIKey:               cfg.LLM.APIKey,
		TimeoutSec:           int(cfg.LLM.Timeout.Seconds()),
		MaxRetries:           cfg.LLM.MaxRetries,
		PromptPricePer1M:     cfg.LLM.PromptPricePer1M,
		CompletionPricePer1M: cfg.LLM.CompletionPricePer1M,
		IsDefault:            true,
		Enabled:              true, // 播种模型默认启用（默认位 = 始终可用）
	}
	if err := store.CreateModel(ctx, spec); err != nil && err != llmsvc.ErrModelExists {
		return err
	}
	log.Info("已从环境配置播种默认模型", zap.String("model", spec.Name))
	return nil
}
