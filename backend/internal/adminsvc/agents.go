package adminsvc

// 智能体管理模块：智能体注册表（阶段3·多租户）。
//
// 数据存于 auth-service 的 PostgreSQL（agents 表），管理端只做转发。
// 仅最高超管（super_admin）可创建智能体；列表按调用者角色返回可见范围
// （超管全部 / 其它管理员仅自己归属），权限校验在 auth-service 内完成。
//
// REST 契约（鉴权由 gateway RequireAdmin 保证）：
//
//	GET    /v1/admin/agents             列出智能体 {agents: [...]}
//	POST   /v1/admin/agents             创建智能体 {id, name, description, model, owner_user_id}
//	GET    /v1/admin/agents/{id}        智能体详情
//	PATCH  /v1/admin/agents/{id}        更新元数据 {name, description, model, avatar, welcome, system_prompt, reasoning_effort}
//	DELETE /v1/admin/agents/{id}        软删除（仅最高超管）
//	POST   /v1/admin/agents/{id}/status 启停 {status: 0|1}（仅最高超管）
//
// owner_user_id：该智能体的超管（agent_admin）。创建时该用户被授予
// agent_admin 角色并绑定智能体标签，成为智能体管理员组的负责人。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
)

// agentsModule 智能体管理模块（仅最高超管可见/可操作）。
type agentsModule struct{ s *Service }

func newAgentsModule(s *Service) Module { return agentsModule{s: s} }

func (m agentsModule) Key() string { return "agents" }
func (m agentsModule) Name() string {
	return "智能体管理"
}
func (m agentsModule) Description() string {
	return "创建/管理智能体及其超管（仅最高超管）"
}
func (m agentsModule) Implemented() bool { return m.s.auth != nil }

func (m agentsModule) Register(mux *http.ServeMux, _ *Service) {
	mux.HandleFunc("GET /v1/admin/agents", m.s.handleAdminListAgents)
	mux.HandleFunc("POST /v1/admin/agents", m.s.handleAdminCreateAgent)
	mux.HandleFunc("GET /v1/admin/agents/{id}", m.s.handleAdminGetAgent)
	mux.HandleFunc("PATCH /v1/admin/agents/{id}", m.s.handleAdminUpdateAgent)
	mux.HandleFunc("DELETE /v1/admin/agents/{id}", m.s.handleAdminDeleteAgent)
	mux.HandleFunc("POST /v1/admin/agents/{id}/status", m.s.handleAdminSetAgentStatus)
	mux.HandleFunc("POST /v1/admin/agents/{id}/owner", m.s.handleAdminBindAgentOwner)
	mux.HandleFunc("GET /v1/admin/agents/{id}/usage", m.s.handleAdminAgentUsage)
	mux.HandleFunc("GET /v1/admin/agents/{id}/defaults", m.s.handleAdminGetAgentDefaults)
	mux.HandleFunc("PUT /v1/admin/agents/{id}/defaults", m.s.handleAdminPutAgentDefaults)
}

// createAgentRequest 创建智能体请求体。
type createAgentRequest struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Model           string `json:"model"`
	OwnerUserID     string `json:"owner_user_id"`
	Avatar          string `json:"avatar"`
	Welcome         string `json:"welcome"`
	SystemPrompt    string `json:"system_prompt"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// updateAgentRequest 更新智能体请求体（name 必填，其余空串=清空）。
type updateAgentRequest struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	Model           string `json:"model"`
	Avatar          string `json:"avatar"`
	Welcome         string `json:"welcome"`
	SystemPrompt    string `json:"system_prompt"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// handleAdminListAgents GET /v1/admin/agents。
func (s *Service) handleAdminListAgents(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	resp, err := s.auth.ListAgents(ctx, &authpb.ListAgentsRequest{})
	if err != nil {
		writeError(w, r, err)
		return
	}
	agents := make([]map[string]any, 0, len(resp.GetAgents()))
	for _, a := range resp.GetAgents() {
		agents = append(agents, agentView(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

// handleAdminCreateAgent POST /v1/admin/agents。
func (s *Service) handleAdminCreateAgent(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	var body createAgentRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	req := &authpb.CreateAgentRequest{
		Id:              body.ID,
		Name:            body.Name,
		Description:     body.Description,
		Model:           body.Model,
		OwnerUserId:     body.OwnerUserID,
		Avatar:          body.Avatar,
		Welcome:         body.Welcome,
		SystemPrompt:    body.SystemPrompt,
		ReasoningEffort: body.ReasoningEffort,
	}
	resp, err := s.auth.CreateAgent(ctx, req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if s.log != nil {
		s.log.Info("admin create agent",
			zap.String("agent_id", resp.GetAgent().GetId()),
			zap.String("owner_user_id", resp.GetAgent().GetOwnerUserId()))
	}
	writeJSON(w, http.StatusCreated, map[string]any{"agent": agentView(resp.GetAgent())})
}

// handleAdminGetAgent GET /v1/admin/agents/{id}。
func (s *Service) handleAdminGetAgent(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	resp, err := s.auth.GetAgent(ctx, &authpb.GetAgentRequest{Id: r.PathValue("id")})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": agentView(resp.GetAgent())})
}

// handleAdminUpdateAgent PATCH /v1/admin/agents/{id}。
func (s *Service) handleAdminUpdateAgent(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	var body updateAgentRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := s.auth.UpdateAgent(ctx, &authpb.UpdateAgentRequest{
		Id:              r.PathValue("id"),
		Name:            body.Name,
		Description:     body.Description,
		Model:           body.Model,
		Avatar:          body.Avatar,
		Welcome:         body.Welcome,
		SystemPrompt:    body.SystemPrompt,
		ReasoningEffort: body.ReasoningEffort,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	if s.log != nil {
		s.log.Info("admin update agent", zap.String("agent_id", r.PathValue("id")))
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": agentView(resp.GetAgent())})
}

// handleAdminDeleteAgent DELETE /v1/admin/agents/{id}。
func (s *Service) handleAdminDeleteAgent(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	if _, err := s.auth.DeleteAgent(ctx, &authpb.DeleteAgentRequest{Id: r.PathValue("id")}); err != nil {
		writeError(w, r, err)
		return
	}
	if s.log != nil {
		s.log.Info("admin delete agent", zap.String("agent_id", r.PathValue("id")))
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminSetAgentStatus POST /v1/admin/agents/{id}/status。
func (s *Service) handleAdminSetAgentStatus(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	var body struct {
		Status int32 `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := s.auth.SetAgentStatus(ctx, &authpb.SetAgentStatusRequest{
		Id:     r.PathValue("id"),
		Status: body.Status,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	if s.log != nil {
		s.log.Info("admin set agent status",
			zap.String("agent_id", r.PathValue("id")),
			zap.Int32("status", body.Status))
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": agentView(resp.GetAgent())})
}

// bindAgentOwnerRequest 绑定/更换/解绑智能体超管请求体。
// owner_user_id 为空串 = 解绑当前 owner（不授予新 owner）。
type bindAgentOwnerRequest struct {
	OwnerUserID string `json:"owner_user_id"`
}

// handleAdminBindAgentOwner POST /v1/admin/agents/{id}/owner（仅最高超管）。
func (s *Service) handleAdminBindAgentOwner(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	var body bindAgentOwnerRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := s.auth.BindAgentOwner(ctx, &authpb.BindAgentOwnerRequest{
		Id:          r.PathValue("id"),
		OwnerUserId: body.OwnerUserID,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	if s.log != nil {
		s.log.Info("admin bind agent owner",
			zap.String("agent_id", r.PathValue("id")),
			zap.String("owner_user_id", body.OwnerUserID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": agentView(resp.GetAgent())})
}

// agentView proto Agent → JSON 视图。
func agentView(a *authpb.Agent) map[string]any {
	if a == nil {
		return nil
	}
	out := map[string]any{
		"id":            a.Id,
		"name":          a.Name,
		"owner_user_id": a.OwnerUserId,
		"status":        a.Status,
	}
	if t := a.GetCreatedAt(); t != nil && t.IsValid() {
		out["created_at"] = t.AsTime().UTC().Format(time.RFC3339)
	}
	if t := a.GetUpdatedAt(); t != nil && t.IsValid() {
		out["updated_at"] = t.AsTime().UTC().Format(time.RFC3339)
	}
	if a.Description != "" {
		out["description"] = a.Description
	}
	if a.Model != "" {
		out["model"] = a.Model
	}
	if a.Avatar != "" {
		out["avatar"] = a.Avatar
	}
	if a.Welcome != "" {
		out["welcome"] = a.Welcome
	}
	if a.SystemPrompt != "" {
		out["system_prompt"] = a.SystemPrompt
	}
	if a.ReasoningEffort != "" {
		out["reasoning_effort"] = a.ReasoningEffort
	}
	return out
}

// agentUsageDays 用量聚合窗口上限（与 llm-gateway usageMaxDays 一致）。
const agentUsageDays = 90

// handleAdminAgentUsage GET /v1/admin/agents/{id}/usage?days=7。
// 先经 auth-service 校验智能体存在与调用者权限（CanManageAgent），
// 再向 llm-gateway 转发聚合查询；llm-gateway 未配置/不可达 → 503。
func (s *Service) handleAdminAgentUsage(w http.ResponseWriter, r *http.Request) {
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少调用者身份"))
		return
	}
	agentID := r.PathValue("id")
	if _, err := s.auth.GetAgent(ctx, &authpb.GetAgentRequest{Id: agentID}); err != nil {
		writeError(w, r, err)
		return
	}
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 1 || d > agentUsageDays {
			writeError(w, r, apperr.New(apperr.CodeInvalidArgument, "days 需为 1..90 的整数"))
			return
		}
		days = d
	}
	out, err := s.agentUsage(agentID, days)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// agentUsage 向 llm-gateway 转发用量聚合查询（GET {llmURL}/v1/usage/agents/{id}?days=N）。
// 未配置 llmURL / 请求失败 / 非 2xx / 响应解析失败 → CodeUnavailable（503）。
func (s *Service) agentUsage(agentID string, days int) (map[string]any, error) {
	if s.llmURL == "" {
		return nil, apperr.New(apperr.CodeUnavailable, "用量统计服务未配置")
	}
	endpoint := s.llmURL + "/v1/usage/agents/" + url.PathEscape(agentID) + "?days=" + strconv.Itoa(days)
	resp, err := s.http.Get(endpoint)
	if err != nil {
		s.log.Warn("llm-gateway 用量查询失败", zap.String("agent_id", agentID), zap.Error(err))
		return nil, apperr.New(apperr.CodeUnavailable, "用量统计服务不可达")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)) // 排空连接，便于复用
		return nil, apperr.New(apperr.CodeUnavailable, "用量统计服务返回异常（HTTP "+resp.Status+"）")
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, apperr.New(apperr.CodeUnavailable, "用量统计服务响应解析失败")
	}
	return out, nil
}
