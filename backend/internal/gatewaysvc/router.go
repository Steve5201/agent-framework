// router.go —— gateway 路由表与中间件链（P2-50/P2-52/P2-53）。
//
// 中间件链（外 → 内）：
//
//	CORS → RequestID → Logger → Recovery → 全局限流(IP)
//	→ RequireAuth(JWT) → 用户维度限流 → 业务 mux
//
// 设计说明：
//   - IP 限流放最外层：匿名攻击（刷注册/登录）在此被挡；
//   - JWT 校验放第二层：只对需要鉴权的路由生效（白名单直放）；
//   - 用户限流放最内层：此时 user_id 已由 RequireAuth 写入 context。
package gatewaysvc

import (
	"net/http"
	"strconv"

	"github.com/Steve5201/agent-backend/internal/middleware"
	"github.com/Steve5201/agent-backend/internal/ratelimit"
	"github.com/Steve5201/agent-backend/internal/server"
)

// authWhitelist 免鉴权路由（匿名入口 + 运维端点）。
// 注意：目录类前缀（/swagger/、/v1/）会被 skipRoute 前缀匹配拦截，
// 因此只列具体路径，避免误放业务路由。
// 注册只允许分智能体入口 /v1/auth/register/{agent_id}（裸 /register 下线：
// 管理员只能被创建，不能自助注册）。
var authWhitelist = []string{
	"POST /v1/auth/register/{agent_id}",
	"POST /v1/auth/login",
	"POST /v1/auth/login/{agent_id}",
	"POST /v1/auth/refresh",
	"GET /v1/models",
	"GET /v1/agent/domains/{id}",
	"GET /healthz",
	"GET /v1/openapi.yaml",
	"GET /swagger/ui",
	"GET /",
}

// Routes 组装完整 HTTP 路由。
// corsOrigins：跨域白名单；globalLimit/userLimit：IP 与用户维度限流配置。
func (c *Clients) Routes(corsOrigins []string, globalLimit, userLimit ratelimit.Config) http.Handler {
	mux := http.NewServeMux()
	registerRoutes(mux, c)

	// 全局限流（按 IP）：防匿名刷接口。
	globalStore := ratelimit.NewStore(globalLimit)
	// 用户维度限流：防单用户刷爆下游（key 带 user: 前缀避免与 IP 键冲突）。
	userStore := ratelimit.NewStore(userLimit)

	api := middleware.Chain(mux,
		middleware.CORS(middleware.CORSConfig{AllowedOrigins: corsOrigins}),
		middleware.RequestID(),
		middleware.Logger(c.Log),
		middleware.Recovery(c.Log),
		ratelimit.Middleware(globalStore, ratelimit.KeyByIP),
		c.RequireAuth(authWhitelist...),
		ratelimit.Middleware(userStore, userRateKey),
	)

	// /files/ 媒体代理放在中间件链之外（仅 CORS）：浏览器 <img>/fetch 请求不
	// 带 token、不受限流配额影响，与 agent 端 /files 无鉴权语义一致（图片并发
	// 多且浏览器缓存友好）。未配置 AgentHTTPAddr 时退化为纯业务路由（单服务/
	// 测试场景向后兼容）。
	if c.AgentHTTPAddr == "" {
		return api
	}
	root := http.NewServeMux()
	root.Handle("/files/", c.filesProxy(corsOrigins))
	root.Handle("/", api)
	return root
}

// registerRoutes 注册全部业务路由（Go 1.22 ServeMux 路径参数）。
func registerRoutes(mux *http.ServeMux, c *Clients) {
	// 健康检查（复用 server 包，含版本信息）。
	server.RegisterHealthz(mux)

	// 认证。注册只保留分智能体入口；登录区分管理员（裸路径）与智能体门户（带 agent_id）。
	mux.HandleFunc("POST /v1/auth/register/{agent_id}", c.Register)
	mux.HandleFunc("POST /v1/auth/login", c.Login)
	mux.HandleFunc("POST /v1/auth/login/{agent_id}", c.Login)
	mux.HandleFunc("POST /v1/auth/refresh", c.Refresh)
	mux.HandleFunc("POST /v1/auth/logout", c.Logout)
	mux.HandleFunc("GET /v1/auth/me", c.Me)
	// 用户自助修改密码（登录态，user_id 来自 JWT，不信任请求体）。
	mux.HandleFunc("PUT /v1/auth/password", c.ChangePassword)

	// 公开模型列表（P3 大模型管理）：代理到 llm-gateway 公开端点，仅供
	// 会话配置区"大模型"下拉渲染——只含名字/供应商/默认位，无任何密钥。
	mux.HandleFunc("GET /v1/models", c.ListPublicModels)

	// 智能体会话。
	mux.HandleFunc("POST /v1/agent/sessions", c.CreateSession)
	mux.HandleFunc("POST /v1/agent/sessions/merge-guest", c.MergeGuestSessions)
	mux.HandleFunc("GET /v1/agent/tools", c.ListTools)
	mux.HandleFunc("GET /v1/agent/resources", c.ListResources)
	// 公开域校验（前端域守卫）：校验 /agent/{id} 是否为已注册智能体域。
	mux.HandleFunc("GET /v1/agent/domains/{id}", c.GetAgentDomain)
	mux.HandleFunc("GET /v1/agent/kbs", c.ListKBs)
	// 智能体默认会话配置（P3 反馈：配置区"大模型"回退链用）。
	mux.HandleFunc("GET /v1/agent/defaults", c.ListAgentDefaults)
	mux.HandleFunc("GET /v1/agent/sessions", c.ListSessions)
	mux.HandleFunc("GET /v1/agent/sessions/{id}", c.GetSession)
	mux.HandleFunc("DELETE /v1/agent/sessions/{id}", c.DeleteSession)
	mux.HandleFunc("PATCH /v1/agent/sessions/{id}", c.RenameSession)
	mux.HandleFunc("GET /v1/agent/sessions/{id}/messages", c.ListSessionMessages)
	mux.HandleFunc("DELETE /v1/agent/sessions/{id}/messages/{mid}", c.DeleteMessage)
	mux.HandleFunc("POST /v1/agent/sessions/{id}/messages/{mid}/regenerate", c.Regenerate)
	mux.HandleFunc("POST /v1/agent/sessions/{id}/messages/{mid}/regenerate-stream", c.StreamRegenerate)
	mux.HandleFunc("POST /v1/agent/sessions/{id}/messages/{mid}/version", c.SetActiveVersion)
	mux.HandleFunc("POST /v1/agent/sessions/{id}/messages/{mid}/branch", c.CreateBranch)
	mux.HandleFunc("POST /v1/agent/sessions/{id}/chat", c.Chat)
	mux.HandleFunc("POST /v1/agent/sessions/{id}/chat/stream", c.StreamChat)
	mux.HandleFunc("POST /v1/agent/sessions/{id}/tool-results", c.SubmitToolResult)
	mux.HandleFunc("POST /v1/agent/sessions/{id}/documents", c.UploadChatDocument)

	// 管理端（admin panel）：模块化，全部要求管理员角色。
	// 模块清单/路由见 internal/adminsvc；新模块在该包内注册，此处无需改动。
	registerAdminRoutes(mux, c)

	// 接口文档（openapi.yaml / Swagger UI）。
	mux.HandleFunc("GET /v1/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		b, _ := docsFS.ReadFile("openapi.yaml")
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /swagger/ui", func(w http.ResponseWriter, _ *http.Request) {
		b, _ := docsFS.ReadFile("swagger.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
	// 根路径：网关是后端入口，直接访问时引导到接口文档，
	// 避免返回未注册路由的 404 或鉴权拦截的 40101 裸 JSON。
	// 用 /{$} 精确匹配根路径，避免与 /v1/admin/ 等子树模式冲突（Go 1.22 ServeMux 规则）。
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/ui", http.StatusFound)
	})
}

// userRateKey 用户维度限流键：优先用已验证 user_id，否则按匿名处理。
// 位于 RequireAuth 之后，正常情况下必然有 user_id。
func userRateKey(r *http.Request) string {
	if uid, err := userIDFrom(r); err == nil {
		return "user:" + strconv.FormatInt(uid, 10)
	}
	return "anon"
}
