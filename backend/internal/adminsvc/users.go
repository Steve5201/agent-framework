package adminsvc

// 用户管理模块：管理员创建/查询用户（经 gateway → auth-service gRPC）。
//
// 与 skill/mcp 的"文件态"不同，用户是"数据库态"数据——存于 auth-service
// 的 PostgreSQL，管理端只做转发，不落本地文件。
//
// REST 契约（全部要求 admin 角色，鉴权由 gateway 中间件保证）：
//
//	POST /v1/admin/users   创建用户 {username, password, role, agent_id, tags[]}
//	GET  /v1/admin/users   分页查询 {page, page_size, keyword}
//
// 语义说明（分智能体注册/登录体系）：
//   - 裸 /v1/auth/register 已下线：管理员只能被管理员创建，不能自助注册；
//   - 创建用户时可指定 agent_id，为该用户打上 {key:"agent", value:<id>} 标签，
//     使其进入对应智能体门户的用户群体（控制前端配置可见性等）；
//   - tags 为自定义标签数组 [{key, value}]，与 agent 标签合并去重。

import (
	"net/http"
	"strconv"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
)

// usersModule 用户管理模块。
type usersModule struct{ s *Service }

func newUsersModule(s *Service) Module { return usersModule{s: s} }

func (m usersModule) Key() string { return "users" }
func (m usersModule) Name() string {
	return "用户管理"
}
func (m usersModule) Description() string {
	return "创建/查询用户账号，支持角色、智能体归属与自定义标签"
}
func (m usersModule) Implemented() bool { return m.s.auth != nil }

func (m usersModule) Register(mux *http.ServeMux, _ *Service) {
	mux.HandleFunc("POST /v1/admin/users", m.s.handleAdminCreateUser)
	mux.HandleFunc("GET /v1/admin/users", m.s.handleAdminListUsers)
	mux.HandleFunc("PUT /v1/admin/users/{id}", m.s.handleAdminResetPassword)
	mux.HandleFunc("DELETE /v1/admin/users/{id}", m.s.handleAdminDeleteUser)
	// 用户配额管理（llm-gateway 代理，仅最高超管，见 quota.go）。
	mux.HandleFunc("GET /v1/admin/quota", m.s.handleQuotaList)
	mux.HandleFunc("PUT /v1/admin/quota/{user_id}", m.s.handleQuotaPut)
	mux.HandleFunc("DELETE /v1/admin/quota/{user_id}", m.s.handleQuotaDelete)
}

// createUserRequest 创建用户请求体。
type createUserRequest struct {
	Username string     `json:"username"`
	Password string     `json:"password"`
	Role     string     `json:"role"`     // user | agent_admin | super_admin（空 = user）
	AgentID  string     `json:"agent_id"` // 可选：追加 agent 标签（agent_admin 会被强制为本组）
	Tags     []tagInput `json:"tags"`     // 可选：自定义标签
}

// tagInput 标签输入（key-value）。
type tagInput struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// handleAdminCreateUser POST /v1/admin/users。
// 调用者身份（identity）注入 gRPC metadata，auth-service 内做分层校验：
// super_admin 可建任意角色；agent_admin 只能在自己智能体组内建 user/admin。
func (s *Service) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	var body createUserRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	req := &authpb.AdminCreateUserRequest{
		Username: body.Username,
		Password: body.Password,
		Role:     body.Role,
		AgentId:  body.AgentID,
	}
	for _, t := range body.Tags {
		req.Tags = append(req.Tags, &authpb.Tag{Key: t.Key, Value: t.Value})
	}
	resp, err := s.auth.AdminCreateUser(ctx, req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if s.log != nil {
		s.log.Info("admin create user", zap.String("user_id", resp.User.GetId()))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": userView(resp.GetUser())})
}

// handleAdminListUsers GET /v1/admin/users?page=1&page_size=20&keyword=alice。
// 结果按调用者管辖范围过滤：super_admin 全局；agent_admin 仅本智能体组。
func (s *Service) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	resp, err := s.auth.AdminListUsers(ctx, &authpb.AdminListUsersRequest{
		Page:     int32(page),
		PageSize: int32(pageSize),
		Keyword:  r.URL.Query().Get("keyword"),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	users := make([]map[string]any, 0, len(resp.GetUsers()))
	for _, u := range resp.GetUsers() {
		users = append(users, userView(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "total": resp.GetTotal()})
}

// handleAdminResetPassword PUT /v1/admin/users/{id}
// 重置指定用户密码。层级校验（当前账号权限须大于被重置账号）在 auth-service
// 内完成；本层只做参数透传与错误映射。
func (s *Service) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	var body struct {
		Password string `json:"password"` // 新密码（>=8 位，含字母与数字）
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := s.auth.AdminUpdateUser(ctx, &authpb.AdminUpdateUserRequest{
		UserId:      r.PathValue("id"),
		NewPassword: body.Password,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": userView(resp.GetUser())})
}

// handleAdminDeleteUser DELETE /v1/admin/users/{id}
// 删除指定用户（禁止删除自己/最后一名最高超管，层级校验在 auth-service 内）。
func (s *Service) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	if _, err := s.auth.AdminDeleteUser(ctx, &authpb.AdminDeleteUserRequest{
		UserId: r.PathValue("id"),
	}); err != nil {
		writeError(w, r, err)
		return
	}
	if s.log != nil {
		s.log.Info("admin delete user", zap.String("user_id", r.PathValue("id")))
	}
	w.WriteHeader(http.StatusNoContent)
}

// userView proto User → JSON 视图（含标签）。
func userView(u *authpb.User) map[string]any {
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
