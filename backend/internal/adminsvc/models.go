// 大模型管理模块：模型注册表经 gateway → llm-gateway 管理端点代理读写。
//
// 安全约束：API Key 只存在于 llm-gateway（单一事实源）。本模块所有请求
// 携带管理令牌 X-Admin-Token（LLM_ADMIN_TOKEN，与 llm-gateway 共享），
// llm-gateway 校验通过才放行；令牌未配置时模块返回 503 提示，不做任何
// 密钥落地（本模块全程无密钥处理，只做 HTTP 透传）。
//
// REST 契约（全部要求 admin 角色，鉴权由 gateway 中间件保证）：
//
//	GET    /v1/admin/models                    列出全部模型（密钥打码）
//	POST   /v1/admin/models                    创建模型（首个模型自动成为默认）
//	PUT    /v1/admin/models/{name}             更新模型接入参数（api_key 空 = 保留原值）
//	POST   /v1/admin/models/{name}/default     设为默认模型（新默认强制启用）
//	POST   /v1/admin/models/{name}/status      启用/禁用模型（默认模型不可禁用）
//	DELETE /v1/admin/models/{name}             删除模型（默认模型不可删除）
package adminsvc

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-backend/internal/identity"
)

// modelsModule 大模型管理模块（仅最高超管可见/可操作，见 ModuleVisible 与
// requireSuperAdmin：模型管理可经代理读写 API Key 配置，属全局基础设施）。
type modelsModule struct{ s *Service }

func newModelsModule(s *Service) Module { return modelsModule{s: s} }

func (m modelsModule) Key() string { return "models" }
func (m modelsModule) Name() string {
	return "大模型管理"
}
func (m modelsModule) Description() string {
	return "管理模型注册表：云端/本地大模型的接入参数与默认模型"
}
func (m modelsModule) Implemented() bool { return m.s.llmURL != "" }

func (m modelsModule) Register(mux *http.ServeMux, _ *Service) {
	mux.HandleFunc("GET /v1/admin/models", m.s.handleModelList)
	mux.HandleFunc("POST /v1/admin/models", m.s.handleModelCreate)
	mux.HandleFunc("PUT /v1/admin/models/{name}", m.s.handleModelUpdate)
	mux.HandleFunc("POST /v1/admin/models/{name}/default", m.s.handleModelSetDefault)
	mux.HandleFunc("POST /v1/admin/models/{name}/status", m.s.handleModelSetStatus)
	mux.HandleFunc("DELETE /v1/admin/models/{name}", m.s.handleModelDelete)
}

// handleModelList GET /v1/admin/models。
func (s *Service) handleModelList(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	s.proxyLLM(w, r, http.MethodGet, "/v1/admin/models", nil)
}

// handleModelCreate POST /v1/admin/models。
func (s *Service) handleModelCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	s.proxyLLM(w, r, http.MethodPost, "/v1/admin/models", r.Body)
}

// handleModelUpdate PUT /v1/admin/models/{name}。
func (s *Service) handleModelUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	path := "/v1/admin/models/" + url.PathEscape(r.PathValue("name"))
	s.proxyLLM(w, r, http.MethodPut, path, r.Body)
}

// handleModelSetDefault POST /v1/admin/models/{name}/default。
func (s *Service) handleModelSetDefault(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	path := "/v1/admin/models/" + url.PathEscape(r.PathValue("name")) + "/default"
	s.proxyLLM(w, r, http.MethodPost, path, nil)
}

// handleModelSetStatus POST /v1/admin/models/{name}/status（启用/禁用模型）。
func (s *Service) handleModelSetStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	path := "/v1/admin/models/" + url.PathEscape(r.PathValue("name")) + "/status"
	s.proxyLLM(w, r, http.MethodPost, path, r.Body)
}

// handleModelDelete DELETE /v1/admin/models/{name}。
func (s *Service) handleModelDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	path := "/v1/admin/models/" + url.PathEscape(r.PathValue("name"))
	s.proxyLLM(w, r, http.MethodDelete, path, nil)
}

// requireSuperAdmin 校验调用者为最高超管。模型/配额管理等经代理读写全局
// 基础设施（llm-gateway 的 API Key 配置 / 用户 token 配额），与智能体管理
// 同级——仅最高超管。返回 false 时已写出 403，调用方应立即 return。
func (s *Service) requireSuperAdmin(w http.ResponseWriter, r *http.Request) bool {
	if identity.Role(r.Context()) != "super_admin" {
		writeError(w, r, apperr.New(apperr.CodePermissionDenied,
			"仅最高超管可执行此操作"))
		return false
	}
	return true
}

// proxyLLM 转发请求到 llm-gateway 管理端点（注入 X-Admin-Token）。
// 状态码与响应体原样透传，让错误文案（如"同名模型已存在"）直达前端。
func (s *Service) proxyLLM(w http.ResponseWriter, r *http.Request, method, path string, body io.Reader) {
	if s.llmURL == "" {
		writeError(w, r, apperr.New(apperr.CodeUnavailable,
			"llm-gateway 未配置（GATEWAY_LLM_BASE_URL），模型管理不可用"))
		return
	}
	if s.llmAdminToken == "" {
		writeError(w, r, apperr.New(apperr.CodeUnavailable,
			"模型管理令牌未配置（LLM_ADMIN_TOKEN），模型管理已禁用"))
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
		strings.TrimRight(s.llmURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		writeError(w, r, apperr.New(apperr.CodeInternal, "构造模型服务请求失败"))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Token", s.llmAdminToken)
	resp, err := s.http.Do(req)
	if err != nil {
		s.log.Warn("模型服务请求失败", zap.String("method", method),
			zap.String("path", path), zap.Error(err))
		writeError(w, r, apperr.New(apperr.CodeUnavailable, "模型服务不可达，请稍后再试"))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode != http.StatusNoContent {
		if _, err := io.Copy(w, resp.Body); err != nil {
			s.log.Warn("模型服务响应透传失败", zap.Error(err))
		}
	}
}
