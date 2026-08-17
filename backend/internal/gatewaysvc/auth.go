// auth.go —— 认证相关 HTTP handlers（P2-51）。
//
// 本文件是"无状态透传层"：只做 协议转换（HTTP JSON ↔ gRPC）+ 错误映射，
// 业务逻辑（密码校验、令牌轮换、吊销族）全部在 auth-service。
package gatewaysvc

import (
	"encoding/json"
	"io"
	"net/http"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
	"go.uber.org/zap"
)

// maxBodyBytes 请求体上限（1MB）。防超大 body 撑爆内存。
const maxBodyBytes = 1 << 20

// decodeJSON 读取并解析 JSON 请求体（限长，防滥用）。
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		return apperr.New(apperr.CodeInvalidArgument, "请求体 JSON 解析失败")
	}
	return nil
}

// Register POST /v1/auth/register/{agent_id}（匿名入口）。
// agent_id 由 gateway 从路径解析后透传，authsvc 据此写入 agent 标签。
// 注意：裸 /v1/auth/register 已下线——管理员只能被创建，不能自助注册。
func (c *Clients) Register(w http.ResponseWriter, r *http.Request) {
	var req authpb.RegisterRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	req.AgentId = r.PathValue("agent_id")
	resp, err := c.Auth.Register(r.Context(), &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	c.Log.Info("register", zap.String("user_id", resp.UserId), zap.String("agent_id", req.AgentId))
	writeJSON(w, http.StatusCreated, map[string]any{
		"user_id":  resp.UserId,
		"username": resp.Username,
	})
}

// Login POST /v1/auth/login（管理员入口）或 POST /v1/auth/login/{agent_id}（普通用户入口）。
// agent_id 为空 = 管理员登录（仅限已创建的管理员账号）；非空 = 智能体门户登录，
// 用户尚无该 agent 标签时由 authsvc 补写（首次登录即绑定智能体）。
func (c *Clients) Login(w http.ResponseWriter, r *http.Request) {
	var req authpb.LoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	req.AgentId = r.PathValue("agent_id")
	resp, err := c.Auth.Login(r.Context(), &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	c.Log.Info("login ok", zap.String("user_id", resp.User.GetId()), zap.String("agent_id", req.AgentId))
	writeJSON(w, http.StatusOK, loginBody(resp))
}

// Refresh POST /v1/auth/refresh（匿名入口，用 refresh token 换新令牌）。
func (c *Clients) Refresh(w http.ResponseWriter, r *http.Request) {
	var req authpb.RefreshRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := c.Auth.Refresh(r.Context(), &req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  resp.AccessToken,
		"refresh_token": resp.RefreshToken,
		"expires_in":    resp.ExpiresIn,
	})
}

// Logout POST /v1/auth/logout（吊销 refresh token 族）。
func (c *Clients) Logout(w http.ResponseWriter, r *http.Request) {
	var req authpb.LogoutRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := c.Auth.Logout(r.Context(), &req); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Me GET /v1/auth/me（当前用户资料；user_id 来自 JWT，不信任请求体）。
func (c *Clients) Me(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := c.Auth.Me(userCtx(r, userID), &authpb.MeRequest{})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, userBody(resp.GetUser()))
}

// ChangePassword PUT /v1/auth/password（用户自助修改密码）。
// user_id 来自 JWT（RequireAuth 已校验）；旧密码校验与改密后强制下线
// 逻辑在 auth-service 内完成。
func (c *Clients) ChangePassword(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDFrom(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req authpb.ChangePasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := c.Auth.ChangePassword(userCtx(r, userID), &req); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// loginBody 组装登录响应（字段命名与前端契约一致）。
func loginBody(resp *authpb.LoginResponse) map[string]any {
	return map[string]any{
		"access_token":  resp.AccessToken,
		"refresh_token": resp.RefreshToken,
		"expires_in":    resp.ExpiresIn,
		"user":          userBody(resp.GetUser()),
	}
}

// userBody proto User → HTTP JSON（含标签；tags 为空时不下发字段）。
func userBody(u *authpb.User) map[string]any {
	if u == nil {
		return nil
	}
	out := map[string]any{"id": u.Id, "username": u.Username}
	if u.Role != "" {
		out["role"] = u.Role
	}
	if len(u.Tags) > 0 {
		tags := make([]map[string]string, 0, len(u.Tags))
		for _, t := range u.Tags {
			tags = append(tags, map[string]string{"key": t.Key, "value": t.Value})
		}
		out["tags"] = tags
	}
	return out
}
