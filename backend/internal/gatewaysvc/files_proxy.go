// files_proxy.go —— gateway 媒体代理（/files → agent-service HTTP /files）。
//
// 背景（P2-AN 上传链路重构）：用户气泡图片 URL = getServerUrl()/files/…（前端
// 指向 gateway），而 /files 内容由 agent HTTP 服务（默认 :8182）提供；gateway
// 此前无该路由，用户气泡图片 404 后降级为文件卡片，而 AI 气泡（模型输出
// files_base_url 直连 :8182）渲染正常，同一图片两端渲染不一致。
//
// 修复：gateway 对 /files/ 反向代理到 agent HTTP /files，浏览器/桌面端统一经
// gateway（唯一对外入口）拉取工作区媒体。代理挂在中间件链之外（仅 CORS）：
// 媒体请求不带 token、不参与 IP/用户限流配额（图片并发多、浏览器缓存友好），
// 与 agent 端 /files 无鉴权语义保持一致。
package gatewaysvc

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"go.uber.org/zap"

	"github.com/Steve5201/agent-backend/internal/middleware"
)

// filesProxy 构造 /files/ 反向代理：目标 = AgentHTTPAddr，请求路径（/files/…）
// 原样转发。corsOrigins 与主链一致，保证浏览器跨域下载（fetch blob）可用。
func (c *Clients) filesProxy(corsOrigins []string) http.Handler {
	target, err := url.Parse(c.AgentHTTPAddr)
	if err != nil || target.Host == "" {
		// 配置非法：退化为 404，避免把媒体请求打到错误地址。
		c.Log.Warn("files proxy: 非法 AgentHTTPAddr，已停用 /files 代理",
			zap.String("addr", c.AgentHTTPAddr),
			zap.Error(err))
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.NotFound(w, nil)
		})
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		c.Log.Warn("files proxy upstream error",
			zap.String("path", r.URL.Path),
			zap.String("upstream", c.AgentHTTPAddr),
			zap.Error(err))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"media upstream unavailable"}`))
	}
	return middleware.CORS(middleware.CORSConfig{AllowedOrigins: corsOrigins})(proxy)
}
