// knowledge 知识库服务入口：知识库/文档管理、上传状态机、版本管理。
//
// 启动示例（需先设置环境变量）：
//
//	$env:DB_PASSWORD='221434'; cd backend; go run ./cmd/knowledge
package main

import (
	"fmt"
	"net/http"

	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/logger"
	"github.com/Steve5201/agent-backend/internal/server"
)

func main() {
	cfg, err := config.Load("knowledge", 8084)
	if err != nil {
		panic(err)
	}
	log := logger.Must(cfg.Env, cfg.LogLevel)
	defer func() { _ = log.Sync() }()

	mux := http.NewServeMux()
	server.RegisterHealthz(mux)

	if err := server.Run(server.Option{
		Name:    cfg.ServiceName,
		Addr:    fmt.Sprintf(":%d", cfg.HTTPPort),
		Logger:  log,
		Handler: mux,
	}); err != nil {
		log.Fatal("knowledge exited", zap.Error(err))
	}
}
