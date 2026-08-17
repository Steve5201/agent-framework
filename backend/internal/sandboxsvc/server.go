// server.go —— sandbox-service HTTP 入口（仅 compose 内部网络可达，不发布宿主端口）。
//
// 路由：
//   - GET  /healthz   健康检查（供 compose healthcheck）
//   - POST /v1/exec   执行代码（agent → sandbox 的唯一入口）
//
// 并发控制：信号量限流（MaxWorkers），超出立即 503 拒绝，防并发拖垮容器。
package sandboxsvc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"
)

// ServerConfig HTTP 服务配置。
type ServerConfig struct {
	// MaxWorkers 最大并发执行数（超出返回 503）。
	MaxWorkers int
	// Log 日志实例。
	Log *zap.Logger
	// Executor 沙盒执行器（由 cmd/sandbox 装配）。
	Executor *Executor
}

// Server sandbox-service HTTP 服务。
type Server struct {
	cfg  ServerConfig
	sem  chan struct{}
	log  *zap.Logger
	exec *Executor
}

// NewServer 构造服务（MaxWorkers 缺省 4）。
func NewServer(cfg ServerConfig) *Server {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 4
	}
	if cfg.Log == nil {
		cfg.Log = zap.NewNop()
	}
	return &Server{
		cfg:  cfg,
		sem:  make(chan struct{}, cfg.MaxWorkers),
		log:  cfg.Log,
		exec: cfg.Executor,
	}
}

// Handler 返回路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("POST /v1/exec", s.handleExec)
	return mux
}

// handleExec 执行一次代码：解析 → 校验 → 限流 → 委托执行器 → 返回结构化结果。
// 安全错误（黑名单/白名单/参数校验）返回 HTTP 200 + result.Error，便于
// agent 把明确原因原样报告给模型；系统错误（JSON 解析/限流）返回 4xx/5xx。
func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	// 请求体上限：代码 + 少量余量（16MB），防内存耗尽。
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		httpError(w, http.StatusBadRequest, "读取请求体失败: "+err.Error())
		return
	}

	var req ExecRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpError(w, http.StatusBadRequest, "请求体 JSON 解析失败: "+err.Error())
		return
	}

	// 限流：信号量非阻塞获取，满则 503（并发超限，请稍后重试）。
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		httpError(w, http.StatusServiceUnavailable, "并发执行数已满（上限 "+strconv.Itoa(s.cfg.MaxWorkers)+"），请稍后重试")
		return
	}

	execCtx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	res, err := s.exec.Exec(execCtx, req)
	if err != nil {
		// 执行前校验失败（黑名单/白名单/参数）——对模型有意义，返回 200+Error。
		writeJSON(w, http.StatusOK, ExecResult{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
