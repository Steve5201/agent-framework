// cors.go —— CORS 跨域中间件（P2-53）。
//
// 背景：web(React :3000) 与 desktop(Tauri) 会跨端口访问 gateway(:8080)，
// 浏览器同源策略要求网关显式声明"允许哪些来源访问"。生产环境必须用
// 白名单收紧（禁止 *），否则任何站点都能拿着用户 token 调用本系统。
package middleware

import (
	"net/http"
	"slices"
	"strings"
)

// CORSConfig 跨域配置。
type CORSConfig struct {
	// AllowedOrigins 允许的来源列表（如 http://localhost:3000）。
	// 空列表 = 拒绝所有跨域（同源直连不受影响）；"*" = 允许全部（仅开发用）。
	AllowedOrigins []string
	// AllowedMethods 允许的 HTTP 方法（默认 GET/POST/PUT/PATCH/DELETE/OPTIONS）。
	AllowedMethods []string
	// AllowedHeaders 允许的请求头（默认含 Authorization/Content-Type/X-Request-Id）。
	AllowedHeaders []string
}

// defaultCORSMethods 默认允许的方法。
var defaultCORSMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}

// defaultCORSHeaders 默认允许的请求头。
// 注意：X-Guest-ID 是游客身份头（未登录时前端注入），必须放行，
// 否则游客对话/游客态登录的预检请求会被浏览器拦截。
var defaultCORSHeaders = []string{"Authorization", "Content-Type", "X-Request-Id", "X-Guest-ID"}

// CORS 返回跨域中间件。
//   - 预检（OPTIONS + Access-Control-Request-Method）直接以 204 结束；
//   - 非预检请求在放行前设置响应头。
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	methods := cfg.AllowedMethods
	if len(methods) == 0 {
		methods = defaultCORSMethods
	}
	headers := cfg.AllowedHeaders
	if len(headers) == 0 {
		headers = defaultCORSHeaders
	}
	allowAll := slices.Contains(cfg.AllowedOrigins, "*")

	// originAllowed 判断来源是否在允许列表（不含 * 时逐个精确匹配）。
	originAllowed := func(origin string) bool {
		if allowAll {
			return true
		}
		return slices.Contains(cfg.AllowedOrigins, origin)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && originAllowed(origin) {
				h := w.Header()
				if allowAll {
					h.Set("Access-Control-Allow-Origin", "*")
				} else {
					h.Set("Access-Control-Allow-Origin", origin)
					h.Add("Vary", "Origin")
				}
				h.Set("Access-Control-Allow-Methods", strings.Join(methods, ", "))
				h.Set("Access-Control-Allow-Headers", strings.Join(headers, ", "))
				// 允许前端读取 request_id（排障全靠它）。
				h.Set("Access-Control-Expose-Headers", middlewareHeaderRequestID)
			}

			// 预检请求：浏览器在真实请求前先发 OPTIONS 探路，直接结束。
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// middlewareHeaderRequestID 复用 HeaderRequestID 常量（避免包内重复定义）。
const middlewareHeaderRequestID = HeaderRequestID
