// admin.go —— gateway 管理端（admin panel）路由装配。
//
// 管理端是"文件态配置平面"：adminsvc 把技能/MCP 配置直接落盘，
// agent 通过 fsnotify 监听相应路径热加载（保存即生效，免重启）。
// 全部 /v1/admin/* 路由包在 RequireAdmin 中（解析 JWT 的 role 声明 == admin）。
//
// 模块化：新模块在 internal/adminsvc 内注册即可，本文件无需改动。
package gatewaysvc

import (
	"net/http"

	"github.com/Steve5201/agent-backend/internal/adminsvc"
)

// registerAdminRoutes 装配管理端全部模块路由（/v1/admin/*）。
func registerAdminRoutes(mux *http.ServeMux, c *Clients) {
	svc, err := adminsvc.NewService(adminsvc.Config{
		SkillsDir:      c.AdminSkillsDir,
		McpConfigFile:  c.AdminMcpConfigFile,
		McpServersDir:  c.AdminMcpServersDir,
		LogsDir:        c.AdminLogsDir, // 操作审计日志（阶段4）
		Rag:               c.Rag,          // 知识库模块经 rag-service gRPC（P3-A）
		Auth:              c.Auth,         // 用户管理模块经 auth-service gRPC
		Agent:             c.Agent,        // 数据管理模块：会话统计经 agent-service gRPC
		LlmGatewayBaseURL: c.LlmGatewayBaseURL, // 用量按智能体聚合（P2-AI）
		LlmAdminToken:     c.LlmAdminToken, // 模型注册表管理令牌（P3 大模型管理）
		AgentHTTPBaseURL:  c.AgentHTTPAddr, // 磁盘配额管理代理目标（模块三）
		Log:               c.Log,
		// 上传限制（P4-L 收口 env；0 = adminsvc 内置默认）。
		KbUploadMaxMB:    c.AdminKbUploadMaxMB,
		SkillUploadMaxMB: c.AdminSkillUploadMaxMB,
	})
	if err != nil {
		// 构造失败属于编程错误（路径配置恒有默认值），尽早暴露。
		panic(err)
	}
	adminMux := http.NewServeMux()
	svc.RegisterRoutes(adminMux)
	// WithAudit 包在 RequireAdmin 之内：只记录已通过管理员鉴权的写操作
	//（POST/PUT/DELETE/PATCH），GET 只读不记录，避免查询本身刷爆日志。
	mux.Handle("/v1/admin/", c.RequireAdmin(svc.WithAudit(adminMux)))
}
