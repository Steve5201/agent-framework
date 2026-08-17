// data.go —— 数据管理模块（运营分析台，super_admin 专属只读模块）。
//
// 定位：平台运行数据的只读观测台，回答三个运营问题：
//  1. 平台用起来了吗？→ 会话活跃度（按日新建会话、全量累计）
//  2. 用户在干什么？  → 调用/DAU/成本与 Top 用户（用量总览）
//  3. 哪个智能体受欢迎？→ 会话按智能体域分布 + 用量按智能体聚合
//
// 与"配置平面"类模块（技能/MCP/KB）不同：本模块不提供任何写操作，
// 数据源来自三个只读服务端（聚合）：
//   - agent-service gRPC AdminSessionStats：会话统计（agent 库 sessions）
//   - llm-gateway  GET /v1/usage/overview：用量总览（llm 库 usage_logs）
//   - auth-service gRPC AdminGetUsersByIds：Top 用户 user_id → 用户名回填
//
// 依赖配置：Agent 客户端、LlmGatewayBaseURL、LlmAdminToken、Auth 客户端
// 任一缺失时 Implemented() 返回 false（前端渲染"规划中"，与占位语义一致）。
package adminsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"go.uber.org/zap"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	agentv1 "github.com/Steve5201/agent-backend/internal/proto/agent/v1"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
)

// dataModule 数据管理模块（只读运营分析）。
type dataModule struct{ s *Service }

func newDataModule(s *Service) Module { return dataModule{s: s} }

func (dataModule) Key() string  { return "data" }
func (dataModule) Name() string { return "数据管理" }
func (dataModule) Description() string {
	return "平台运营分析：会话活跃度、智能体反馈与成本速览"
}

// Implemented 数据源齐备才视为已实现（agent 会话统计 + llm 用量 + 用户名回填）。
func (m dataModule) Implemented() bool {
	return m.s.agent != nil && m.s.auth != nil && m.s.llmURL != "" && m.s.llmAdminToken != ""
}

func (m dataModule) Register(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("GET /v1/admin/data/overview", m.handleOverview)
}

// maxOverviewDays 数据管理窗口上限（与 agent/llm 两端一致）。
const maxOverviewDays = 90

// topUsersLimit 用户名回填数量上限（Top 用户展示）。
const topUsersLimit = 10

// ---------------------------------------------------------------------------
// 对外 JSON 契约（前端 DataPage 渲染）
// ---------------------------------------------------------------------------

// dataOverview 数据管理总览：会话统计 + 用量总览 + 用户名回填。
type dataOverview struct {
	Sessions  *sessionStatsView `json:"sessions"`
	Usage     *llmUsageOverview `json:"usage"`
	UserNames map[string]string `json:"user_names"` // Top 用户 user_id → username
}

// sessionStatsView 会话统计（agent-service AdminSessionStats 的扁平视图）。
type sessionStatsView struct {
	Days   []sessionDayStat   `json:"days"`
	Agents []sessionAgentStat `json:"agents"`
	Total  int64              `json:"total_sessions"`
}

type sessionDayStat struct {
	Date     string `json:"date"`
	Sessions int64  `json:"sessions"`
}

type sessionAgentStat struct {
	AgentID  string `json:"agent_id"` // '' = 管理端域
	Sessions int64  `json:"sessions"`
}

// llmUsageOverview llm-gateway GET /v1/usage/overview 的响应结构（对齐其 JSON）。
type llmUsageOverview struct {
	Summary llmUsageSummary `json:"summary"`
	Daily   []llmDayUsage   `json:"daily"`
	ByModel []llmUsageGroup `json:"by_model"`
	ByAgent []llmUsageGroup `json:"by_agent"`
	ByUser  []llmUserUsage  `json:"by_user"`
}

type llmUsageSummary struct {
	Calls       int64   `json:"calls"`
	Success     int64   `json:"success"`
	Failed      int64   `json:"failed"`
	DAU         int64   `json:"dau"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

type llmDayUsage struct {
	Date        string  `json:"date"`
	Calls       int64   `json:"calls"`
	Success     int64   `json:"success"`
	Failed      int64   `json:"failed"`
	DAU         int64   `json:"dau"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

type llmUsageGroup struct {
	Key         string  `json:"key"`
	Calls       int64   `json:"calls"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

type llmUserUsage struct {
	UserID      int64   `json:"user_id"`
	Calls       int64   `json:"calls"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

// ---------------------------------------------------------------------------
// handler
// ---------------------------------------------------------------------------

// handleOverview 聚合返回数据管理总览（GET /v1/admin/data/overview?days=N）。
// 会话统计与用量总览并行拉取；Top 用户用户名回填失败仅告警、不阻断主数据。
func (m dataModule) handleOverview(w http.ResponseWriter, r *http.Request) {
	days, err := parseDataDays(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	ctx, ok := adminCtx(r)
	if !ok {
		writeError(w, r, apperr.New(apperr.CodeUnauthenticated, "缺少管理员身份"))
		return
	}

	type sessResult struct {
		st  *sessionStatsView
		err error
	}
	type usageResult struct {
		ov  *llmUsageOverview
		err error
	}
	sessCh := make(chan sessResult, 1)
	usageCh := make(chan usageResult, 1)
	go func() {
		st, err := m.fetchSessionStats(ctx, days)
		sessCh <- sessResult{st: st, err: err}
	}()
	go func() {
		ov, err := m.fetchLLMOverview(ctx, days)
		usageCh <- usageResult{ov: ov, err: err}
	}()

	sr := <-sessCh
	ur := <-usageCh
	if sr.err != nil {
		writeError(w, r, sr.err)
		return
	}
	if ur.err != nil {
		writeError(w, r, ur.err)
		return
	}

	// Top 用户用户名回填（成功调用降序取前 10；失败不阻塞主数据）。
	userNames := m.resolveTopUserNames(ctx, ur.ov.ByUser)

	writeJSON(w, http.StatusOK, dataOverview{
		Sessions:  sr.st,
		Usage:     ur.ov,
		UserNames: userNames,
	})
}

// resolveTopUserNames 把 Top 用户 user_id 批量回填为用户名（auth-service）。
func (m dataModule) resolveTopUserNames(ctx context.Context, byUser []llmUserUsage) map[string]string {
	names := make(map[string]string, len(byUser))
	if len(byUser) == 0 {
		return names
	}
	top := byUser
	if len(top) > topUsersLimit {
		top = top[:topUsersLimit]
	}
	ids := make([]string, 0, len(top))
	for _, u := range top {
		ids = append(ids, strconv.FormatInt(u.UserID, 10))
	}
	resp, err := m.s.auth.AdminGetUsersByIds(ctx, &authpb.AdminGetUsersByIdsRequest{UserIds: ids})
	if err != nil {
		// 回填失败只告警：Top 用户展示的是使用数据，用户名缺失可接受。
		m.s.log.Warn("回填 Top 用户用户名失败", zap.Error(err))
		return names
	}
	for _, u := range resp.GetUsers() {
		names[u.GetId()] = u.GetUsername()
	}
	return names
}

// fetchSessionStats 调 agent-service 会话统计（近 days 天 + 全量累计）。
func (m dataModule) fetchSessionStats(ctx context.Context, days int) (*sessionStatsView, error) {
	resp, err := m.s.agent.AdminSessionStats(ctx, &agentv1.AdminSessionStatsRequest{Days: int32(days)})
	if err != nil {
		return nil, apperr.FromGRPCError(err)
	}
	st := &sessionStatsView{Total: resp.GetTotalSessions()}
	for _, d := range resp.GetDays() {
		st.Days = append(st.Days, sessionDayStat{Date: d.GetDate(), Sessions: d.GetSessions()})
	}
	for _, a := range resp.GetAgents() {
		st.Agents = append(st.Agents, sessionAgentStat{AgentID: a.GetAgentId(), Sessions: a.GetSessions()})
	}
	return st, nil
}

// fetchLLMOverview 调 llm-gateway 用量总览（带 X-Admin-Token；8083 暴露宿主必须鉴权）。
func (m dataModule) fetchLLMOverview(ctx context.Context, days int) (*llmUsageOverview, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		m.s.llmURL+"/v1/usage/overview?days="+strconv.Itoa(days), nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "构造用量总览请求失败", err)
	}
	req.Header.Set("X-Admin-Token", m.s.llmAdminToken)
	resp, err := m.s.http.Do(req)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeUnavailable, "连接 llm-gateway 失败", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, apperr.New(apperr.CodeInternal,
			fmt.Sprintf("llm-gateway 用量总览失败（HTTP %d）：%s", resp.StatusCode, body))
	}
	var ov llmUsageOverview
	if err := json.NewDecoder(resp.Body).Decode(&ov); err != nil {
		return nil, apperr.Wrap(apperr.CodeInternal, "解析用量总览失败", err)
	}
	return &ov, nil
}

// parseDataDays 解析 days 窗口（缺省 30，范围 1..90）。
func parseDataDays(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("days")
	if raw == "" {
		return 30, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxOverviewDays {
		return 0, apperr.New(apperr.CodeInvalidArgument, "参数 days 须为 1..90 的整数")
	}
	return n, nil
}
