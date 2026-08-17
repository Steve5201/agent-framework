// 管理端用户配额端点（q3 按用户配额管理）。
//
//   - GET    /v1/admin/quota             列表（含本月用量）
//   - PUT    /v1/admin/quota/{user_id}   设置/覆盖（token_quota_month=0 表示不限）
//   - DELETE /v1/admin/quota/{user_id}   删除覆盖（恢复角色默认）
//
// 安全约束：与模型/用量管理端点一致，须携带 X-Admin-Token（LLM_ADMIN_TOKEN）；
// 未配置时管理端点禁用（503）。配额的写操作应仅由 gateway 管理端
// （super_admin 权限）通过代理调用，llm-gateway 自身不校验角色。
package llmsvc

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// QuotaAdmin 管理端用户配额处理器。
type QuotaAdmin struct {
	store QuotaStore
	token string // LLM_ADMIN_TOKEN；空 = 管理端点禁用
	log   *zap.Logger
}

// NewQuotaAdmin 创建管理端用户配额处理器。
func NewQuotaAdmin(store QuotaStore, token string, log *zap.Logger) *QuotaAdmin {
	return &QuotaAdmin{store: store, token: token, log: log}
}

// RegisterAdmin 注册管理端点（要求 X-Admin-Token）。
func (a *QuotaAdmin) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/quota", a.requireToken(a.handleList))
	mux.HandleFunc("PUT /v1/admin/quota/{user_id}", a.requireToken(a.handlePut))
	mux.HandleFunc("DELETE /v1/admin/quota/{user_id}", a.requireToken(a.handleDelete))
}

// requireToken 管理端点令牌中间件：令牌未配置 → 503；不匹配 → 401。
func (a *QuotaAdmin) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.token == "" {
			writeError(w, "", apperr.New(apperr.CodeUnavailable,
				"配额管理端点未启用：请为 llm-gateway 与 gateway 设置 LLM_ADMIN_TOKEN"))
			return
		}
		got := r.Header.Get("X-Admin-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
			writeError(w, "", apperr.New(apperr.CodeUnauthenticated, "管理令牌无效"))
			return
		}
		next(w, r)
	}
}

// quotaInput 设置配额的接入参数（对外 JSON 契约）。
type quotaInput struct {
	TokenQuotaMonth int64 `json:"token_quota_month"` // 每月 token 配额；0 = 不限
}

// handleList 返回全部显式配额记录（含本月用量）。
func (a *QuotaAdmin) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.List(r.Context())
	if err != nil {
		a.log.Error("quota list failed", zap.Error(err))
		writeError(w, "", apperr.Wrap(apperr.CodeInternal, "查询配额列表失败", err))
		return
	}
	if list == nil {
		list = []UserQuota{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"quotas": list})
}

// handlePut 设置/更新用户显式配额（token_quota_month=0 表示不限）。
func (a *QuotaAdmin) handlePut(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.PathValue("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, "", apperr.New(apperr.CodeInvalidArgument, "user_id 须为正整数"))
		return
	}
	var in quotaInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		writeError(w, "", apperr.New(apperr.CodeInvalidArgument, "请求体不是合法的 JSON"))
		return
	}
	if in.TokenQuotaMonth < 0 {
		writeError(w, "", apperr.New(apperr.CodeInvalidArgument, "token_quota_month 不能为负数（0 = 不限）"))
		return
	}
	// 操作人：管理令牌不经用户体系，写 updated_by=0（网关代理调用时无法
	// 区分到具体管理员；如需审计可在网关层注入操作人，属后续增强）。
	if err := a.store.Set(r.Context(), userID, in.TokenQuotaMonth, 0); err != nil {
		a.log.Error("quota set failed", zap.Error(err), zap.Int64("user_id", userID))
		writeError(w, "", apperr.Wrap(apperr.CodeInternal, "设置配额失败", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":           userID,
		"token_quota_month": in.TokenQuotaMonth,
	})
}

// handleDelete 删除用户显式配额（恢复角色默认）。
func (a *QuotaAdmin) handleDelete(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(r.PathValue("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, "", apperr.New(apperr.CodeInvalidArgument, "user_id 须为正整数"))
		return
	}
	if err := a.store.Clear(r.Context(), userID); err != nil {
		a.log.Error("quota clear failed", zap.Error(err), zap.Int64("user_id", userID))
		writeError(w, "", apperr.Wrap(apperr.CodeInternal, "删除配额覆盖失败", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID})
}
