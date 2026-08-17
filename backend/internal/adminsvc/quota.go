// 用户配额管理（q3 网关代理）：经 gateway → llm-gateway 的 /v1/admin/quota*。
//
// 数据源在 llm-gateway（llm 库 user_quota 表，按用户覆盖角色默认配额），
// 本模块只做 HTTP 透传（X-Admin-Token 校验在 llm-gateway 侧完成）。
//
// REST 契约（仅最高超管，requireSuperAdmin；agent_admin 进用户管理页但
// 配额操作返回 403——配额直接影响全局 token 成本，与模型管理同级收紧）：
//
//	GET    /v1/admin/quota             列出全部显式配额（含本月用量）
//	PUT    /v1/admin/quota/{user_id}   设置/覆盖配额 {token_quota_month: N}（0=不限）
//	DELETE /v1/admin/quota/{user_id}   删除覆盖（恢复角色默认）
package adminsvc

import (
	"net/http"
	"net/url"
)

// handleQuotaList GET /v1/admin/quota。
func (s *Service) handleQuotaList(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	s.proxyLLM(w, r, http.MethodGet, "/v1/admin/quota", nil)
}

// handleQuotaPut PUT /v1/admin/quota/{user_id}（body: {token_quota_month: N}）。
func (s *Service) handleQuotaPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	path := "/v1/admin/quota/" + url.PathEscape(r.PathValue("user_id"))
	s.proxyLLM(w, r, http.MethodPut, path, r.Body)
}

// handleQuotaDelete DELETE /v1/admin/quota/{user_id}。
func (s *Service) handleQuotaDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	path := "/v1/admin/quota/" + url.PathEscape(r.PathValue("user_id"))
	s.proxyLLM(w, r, http.MethodDelete, path, nil)
}
