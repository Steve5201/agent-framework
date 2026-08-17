// middleware.go —— gateway 鉴权中间件（P2-51）。
//
// 流程：白名单直放 → 提取 Bearer token → 本地 JWT 校验（access 类型）
// → 解析 user_id 写入 context → 放行。
//
// 设计要点：
//   - gateway 自己校验 JWT，不把 access token 发给下游（下游只信 metadata）；
//   - 校验失败统一返回 UNAUTHENTICATED，不区分"无效/过期"，防探测；
//   - user_id 后续经 userCtx() 注入 gRPC 出站 metadata（x-user-id）。
package gatewaysvc

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/Steve5201/agent-backend/internal/auth"
	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-backend/internal/identity"
	"google.golang.org/grpc/metadata"
)

// userIDFrom 从请求 context 读取已验证的 user_id（中间件写入）。
// 真实用户 > 0；游客（阶段2）为 auth.GuestUserID 派生的负值，仅 0 非法。
func userIDFrom(r *http.Request) (int64, error) {
	uid, ok := identity.UserID(r.Context())
	if !ok {
		return 0, apperr.New(apperr.CodeUnauthenticated, "缺少用户身份")
	}
	return uid, nil
}

// roleFrom 从请求 context 读取已验证的用户角色（RequireAuth 写入）。
func roleFrom(r *http.Request) string {
	return identity.Role(r.Context())
}

// userCtx 在出站 gRPC 调用中注入 x-user-id / x-user-role（下游各服务只信
// 这个来源；agent-service 据此透传 X-User-Role 头给 llm-gateway 做配额角色默认）。
func userCtx(r *http.Request, userID int64) context.Context {
	return metadata.AppendToOutgoingContext(
		r.Context(),
		"x-user-id", strconv.FormatInt(userID, 10),
		"x-user-role", roleFrom(r),
	)
}

// bearerToken 提取 Authorization: Bearer <token>。
func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

// skipRoute 判断请求是否命中白名单。
// 白名单项支持三种匹配方式：
//  1. 精确：      "POST /v1/auth/login"；
//  2. 目录前缀：  "GET /swagger/"（路径部分以 / 结尾，长度 > 1）；
//  3. 路径通配：  "POST /v1/auth/register/{agent_id}"——{xxx} 段匹配任意
//     非空单段值（如 tutor），与 Go 1.22 ServeMux 的路径参数路由一致。
//
// 注意：前缀模式要求路径部分长度 > 1（如 "/swagger/"）：单独的 "/"（根路径）
// 只做精确匹配，否则 "GET /" 会被当作目录前缀误放行所有 GET 请求。
func skipRoute(r *http.Request, whitelist []string) bool {
	for _, item := range whitelist {
		sp := strings.SplitN(item, " ", 2)
		if len(sp) != 2 || sp[0] != r.Method {
			continue
		}
		// 1. 精确匹配
		if sp[1] == r.URL.Path {
			return true
		}
		// 2. 目录前缀匹配（路径部分以 / 结尾且长度 > 1）
		if len(sp[1]) > 1 && strings.HasSuffix(sp[1], "/") && strings.HasPrefix(r.URL.Path, sp[1]) {
			return true
		}
		// 3. 路径通配匹配：{xxx} 段匹配任意非空单段
		if strings.Contains(sp[1], "{") && patternPathMatch(sp[1], r.URL.Path) {
			return true
		}
	}
	return false
}

// patternPathMatch 按 "/" 分段比较 pattern 与 path：
// {name} 段匹配任意非空段；其余段必须完全相等。
// 分段数必须一致（与 ServeMux 的 {x} 语义对齐，不跨段匹配）。
func patternPathMatch(pattern, path string) bool {
	ps := strings.Split(pattern, "/")
	qs := strings.Split(path, "/")
	if len(ps) != len(qs) {
		return false
	}
	for i := range ps {
		if isWildcardSegment(ps[i]) {
			if qs[i] == "" {
				return false // 通配段不允许为空（与 ServeMux {x} 语义一致）
			}
			continue
		}
		if ps[i] != qs[i] {
			return false
		}
	}
	return true
}

// isWildcardSegment 判断段是否为 {name} 形式的通配符段。
func isWildcardSegment(seg string) bool {
	return len(seg) > 2 && seg[0] == '{' && seg[len(seg)-1] == '}'
}

// RequireAuth 返回鉴权中间件。skip 为免鉴权路由白名单。
//
// 白名单示例（注册/登录/刷新是匿名入口；健康检查与文档供运维查看）：
//
//	"POST /v1/auth/register", "POST /v1/auth/login", "POST /v1/auth/refresh",
//	"GET /healthz", "GET /v1/openapi.yaml", "GET /swagger/"
func (c *Clients) RequireAuth(skip ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skipRoute(r, skip) {
				next.ServeHTTP(w, r)
				return
			}

			token := bearerToken(r.Header.Get("Authorization"))
			if token == "" {
				// 阶段2·游客模式：无访问令牌但携带合法 X-Guest-ID 时，以
				// 游客身份放行（负整数 user_id，见 auth.GuestUserID）。
				// 游客只允许访问 /v1/agent/* 对话域；管理端路由仍被
				// RequireAdmin 的角色校验拦截（游客角色为空）。
				if guestUID := auth.GuestUserID(r.Header.Get("X-Guest-ID")); guestUID != 0 {
					ctx := identity.WithUserID(r.Context(), guestUID)
					ctx = identity.WithRole(ctx, "")
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少访问令牌"))
				return
			}
			claims, err := c.JWT.Verify(token, auth.TokenTypeAccess)
			if err != nil {
				// 无效/过期/类型不符统一提示（防探测有效路径）。
				writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "访问令牌无效或已过期"))
				return
			}
			userID, err := strconv.ParseInt(claims.UserID, 10, 64)
			if err != nil || userID <= 0 {
				writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "令牌中用户 ID 非法"))
				return
			}

			ctx := identity.WithUserID(r.Context(), userID)
			// 同时携带角色（JWT 声明），供管理端 RBAC 使用（RequireAdmin）。
			ctx = identity.WithRole(ctx, claims.Role)
			ctx = identity.WithAgentID(ctx, claims.AgentID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin 返回管理员角色校验中间件（须置于 RequireAuth 之后）。
// 校验 JWT 中的 role 声明；非管理员类角色一律 403，避免逐个 handler 重复判断。
// 阶段3·角色体系：super_admin / agent_admin / admin 均为管理员；
// 模块级权限由 adminsvc 按角色裁剪（见 adminsvc/adminsvc.go）。
func (c *Clients) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !identity.IsAdminRole(roleFrom(r)) {
			writeError(w, r, apperr.New(apperr.CodePermissionDenied, "需要管理员权限"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
