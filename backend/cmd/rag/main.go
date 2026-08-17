// rag 检索服务入口（P3-A）：RAG 系统框架核心。
//
// 服务形态（双端口）：
//   - gRPC 业务端口 :8085（GRPC_PORT 可覆盖）：ragv1.RagService
//     （Search/CreateKB/ListKBs/UpsertDocument/ListDocuments/…）
//   - HTTP 健康检查端口 :8185（HTTP_PORT 可覆盖）：GET /healthz
//     （默认 http_port 与 grpc_port 同为 8085，main 检测到冲突会自动偏移 +100）
//
// 调用链：
//
//	gateway:8080 ──REST /v1/admin/kb──▶(内部 gRPC) rag:8085 ──HTTP OpenAI──▶ 硅基流动 embedding
//	agent:8082  ──gRPC kb_search──▶ rag:8085（智能体检索入口）
//
// 启动示例（需先设置环境变量）：
//
//	$env:DB_PASSWORD='...'; $env:SILICONFLOW_API_KEY='...'; cd backend; go run ./cmd/rag
//
// 验证方式：
//
//	curl http://localhost:8185/healthz
//	grpcurl -plaintext -d '{"name":"测试知识库"}' localhost:8085 rag.v1.RagService/CreateKnowledgeBase
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

	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/db"
	"github.com/Steve5201/agent-backend/internal/grpcx"
	"github.com/Steve5201/agent-backend/internal/logger"
	"github.com/Steve5201/agent-backend/internal/middleware"
	ragv1 "github.com/Steve5201/agent-backend/internal/proto/rag/v1"
	"github.com/Steve5201/agent-backend/internal/rag"
	"github.com/Steve5201/agent-backend/internal/rag/embedding"
	"github.com/Steve5201/agent-backend/internal/server"
	"github.com/Steve5201/agent-backend/migrations"
	"github.com/jackc/pgx/v5"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

func main() {
	cfg, err := config.Load("rag", 8085)
	if err != nil {
		panic(err)
	}
	log := logger.Must(cfg.Env, cfg.LogLevel)
	defer func() { _ = log.Sync() }()

	// 1. 生命周期信号 → 优雅关闭 gRPC / HTTP / 摄取 worker。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 2. 数据库：先迁移、后建池。
	//    迁移必须先于连接池建立：pgxvec.RegisterTypes 按连接注册 vector 类型，
	//    要求库里已存在 extension vector（迁移负责创建）；若先建池，池预建的
	//    MinConns 个连接会因类型缺失注册失败。
	if err := db.MigrateUp(ctx, cfg.DB.DSN(), "rag", migrations.FS); err != nil {
		log.Fatal("run migrations", zap.Error(err))
	}
	pool, err := db.Connect(ctx, cfg.DB.DSN(), db.Options{
		// 每个新连接注册 pgvector 类型（Search 走向量距离需要）。
		AfterConnect: func(ctx context.Context, conn *pgx.Conn) error {
			return pgxvec.RegisterTypes(ctx, conn)
		},
	})
	if err != nil {
		log.Fatal("connect database", zap.Error(err))
	}
	defer pool.Close()
	log.Info("database ready and migrated")

	// 3. Embedding Provider（P3-A）：两种模式，未配置时降级而非拒绝启动。
	//    - 本地 Ollama（默认）：无需 APIKey，随 docker 部署；
	//    - 硅基流动（云端）：设置 SILICONFLOW_API_KEY 后 config 自动切换。
	//    构造失败（如端点不可达配置错误）或完全未配置时，使用 UnavailableProvider
	//    降级启动：RAG 相关 RPC 返回"未配置"明确提示，不影响其它服务启动。
	emb, err := embedding.NewOpenAICompatible(embedding.Config{
		BaseURL:    cfg.RAG.EmbeddingBaseURL,
		APIKey:     cfg.RAG.EmbeddingAPIKey,
		Model:      cfg.RAG.EmbeddingModel,
		Dim:        cfg.RAG.EmbeddingDim,
		Timeout:    30 * time.Second,
		MaxRetries: 2,
		Log:        log,
	})
	if err != nil {
		log.Warn("embedding provider 构造失败，RAG 检索/摄取降级为不可用",
			zap.String("base_url", cfg.RAG.EmbeddingBaseURL),
			zap.Error(err))
		emb = embedding.NewUnavailable()
	} else {
		log.Info("embedding provider ready",
			zap.String("provider", emb.Name()),
			zap.Int("dim", emb.Dim()),
			zap.String("base_url", cfg.RAG.EmbeddingBaseURL))
	}

	// 4. RAG 业务服务 + 摄取 worker（后台异步，状态机推进）。
	store := rag.NewStore(pool)
	svc := rag.NewService(store, emb, cfg.RAG, log)
	go svc.RunIngestWorkers(ctx)
	log.Info("ingest workers started", zap.Int("workers", cfg.RAG.IngestWorkers))
	// 无主 rag-media 目录定期清理（模块三）：文档删除后残留的孤儿媒体目录
	// 在宽限期后兜底删除；周期 0 = 禁用（默认 6h）。
	go svc.RunMediaCleanup(ctx, cfg.RAG.MediaCleanupInterval)

	// 5. gRPC 业务服务（统一拦截器：request_id → recovery → 日志）。
	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
	if err != nil {
		log.Fatal("listen gRPC", zap.Error(err))
	}
	grpcSrv := grpcx.NewServer(log)
	ragv1.RegisterRagServiceServer(grpcSrv, rag.NewServer(svc, log))

	// 6. HTTP 健康检查；与 gRPC 同端口时自动偏移（日志明示实际端口）。
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
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() { errCh <- grpcx.Serve(grpcSrv, grpcLis, log, cfg.ServiceName) }()
	go func() {
		log.Info("http service listening", zap.String("service", cfg.ServiceName+"-http"), zap.String("addr", httpSrv.Addr))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// 7. 生命周期：任一监听失败即退出；收到信号后优雅关闭。
	select {
	case err := <-errCh:
		log.Fatal("service exited", zap.Error(err))
	case <-ctx.Done():
		log.Info("shutdown signal received, stopping servers")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stopped := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(stopped)
	}()
	_ = httpSrv.Shutdown(shutdownCtx)

	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		grpcSrv.Stop() // 超时强停
	}
	log.Info("rag service stopped gracefully")
}

// newHTTPHandler 组装 HTTP 路由：健康检查。
func newHTTPHandler(log *zap.Logger) http.Handler {
	mux := http.NewServeMux()
	server.RegisterHealthz(mux)
	return middleware.Chain(mux,
		middleware.RequestID(),
		middleware.Logger(log),
		middleware.Recovery(log),
	)
}
