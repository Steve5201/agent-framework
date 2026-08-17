// 磁盘配额管理模块（模块三·保护区配额）：经 gateway → agent-service 的
// /v1/admin/disk-quota* 代理读写。
//
// 数据源在 agent-service（agent 库 sandbox_disk_quota 表，file_ops 校验侧），
// 本模块只做 HTTP 透传（X-Admin-Token 校验在 agent-service 侧完成，令牌与
// llm-gateway 共享同一 LLM_ADMIN_TOKEN）。
//
// REST 契约（仅最高超管，requireSuperAdmin——配额直接决定磁盘占用，与模型
// 管理同级收紧）：
//
//	GET    /v1/admin/disk-quota            列出全部显式配额
//	PUT    /v1/admin/disk-quota/{user_id}  设置/覆盖配额 {disk_quota_mb: N}（0=不限）
//	DELETE /v1/admin/disk-quota/{user_id}  删除覆盖（恢复角色默认）
package adminsvc

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// diskQuotaModule 磁盘配额管理模块（仅最高超管可见，见 ModuleVisible）。
type diskQuotaModule struct{ s *Service }

func newDiskQuotaModule(s *Service) Module { return diskQuotaModule{s: s} }

func (m diskQuotaModule) Key() string { return "disk-quota" }
func (m diskQuotaModule) Name() string {
	return "磁盘配额"
}
func (m diskQuotaModule) Description() string {
	return "设置用户工作区保护区（protected/）的磁盘配额上限"
}
func (m diskQuotaModule) Implemented() bool { return m.s.agentHTTPBase != "" }

func (m diskQuotaModule) Register(mux *http.ServeMux, _ *Service) {
	mux.HandleFunc("GET /v1/admin/disk-quota", m.s.handleDiskQuotaList)
	mux.HandleFunc("PUT /v1/admin/disk-quota/{user_id}", m.s.handleDiskQuotaPut)
	mux.HandleFunc("DELETE /v1/admin/disk-quota/{user_id}", m.s.handleDiskQuotaDelete)
}

// handleDiskQuotaList GET /v1/admin/disk-quota。
func (s *Service) handleDiskQuotaList(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	s.proxyAgent(w, r, http.MethodGet, "/v1/admin/disk-quota", nil)
}

// handleDiskQuotaPut PUT /v1/admin/disk-quota/{user_id}（body: {disk_quota_mb: N}）。
func (s *Service) handleDiskQuotaPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	path := "/v1/admin/disk-quota/" + url.PathEscape(r.PathValue("user_id"))
	s.proxyAgent(w, r, http.MethodPut, path, r.Body)
}

// handleDiskQuotaDelete DELETE /v1/admin/disk-quota/{user_id}。
func (s *Service) handleDiskQuotaDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	path := "/v1/admin/disk-quota/" + url.PathEscape(r.PathValue("user_id"))
	s.proxyAgent(w, r, http.MethodDelete, path, nil)
}

// proxyAgent 转发请求到 agent-service 管理端点（注入 X-Admin-Token）。
// 状态码与响应体原样透传，让错误文案（如"配额已设置"）直达前端。
func (s *Service) proxyAgent(w http.ResponseWriter, r *http.Request, method, path string, body io.Reader) {
	if s.agentHTTPBase == "" {
		writeError(w, r, apperr.New(apperr.CodeUnavailable,
			"agent-service 未配置（GATEWAY_AGENT_HTTP_ADDR），磁盘配额管理不可用"))
		return
	}
	if s.llmAdminToken == "" {
		writeError(w, r, apperr.New(apperr.CodeUnavailable,
			"管理令牌未配置（LLM_ADMIN_TOKEN），磁盘配额管理已禁用"))
		return
	}
	var payload []byte
	if body != nil {
		b, err := io.ReadAll(io.LimitReader(body, maxBodyBytes))
		if err != nil {
			writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "读取请求体失败"))
			return
		}
		payload = b
	}
	req, err := http.NewRequestWithContext(r.Context(), method,
		strings.TrimRight(s.agentHTTPBase, "/")+path, bytes.NewReader(payload))
	if err != nil {
		writeError(w, r, apperr.New(apperr.CodeInternal, "构造 agent-service 请求失败"))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Token", s.llmAdminToken)
	resp, err := s.http.Do(req)
	if err != nil {
		s.log.Warn("agent-service 请求失败", zap.String("method", method),
			zap.String("path", path), zap.Error(err))
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "agent-service 不可达，请稍后再试"))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode != http.StatusNoContent {
		if _, err := io.Copy(w, resp.Body); err != nil {
			s.log.Warn("agent-service 响应透传失败", zap.Error(err))
		}
	}
}
