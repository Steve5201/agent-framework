// data_test.go —— 数据管理模块（运营分析台）测试。
//
// 覆盖：三端聚合（agent 会话统计 + llm 用量 + auth 用户名回填）、
// llm 端令牌透传、任一端失败降级语义、窗口参数校验、模块状态判定。
package adminsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/Steve5201/agent-backend/internal/identity"
	agentv1 "github.com/Steve5201/agent-backend/internal/proto/agent/v1"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
)

// withDataRole 在请求 context 写入管理员身份（含 user id）：data 模块 handler
// 经 adminCtx 读 identity.UserID 并透传下游 gRPC metadata，仅设置 role 不够。
func withDataRole(req *http.Request, role, userID string) *http.Request {
	ctx := identity.WithRole(req.Context(), role)
	if userID != "" {
		n, _ := strconv.ParseInt(userID, 10, 64)
		ctx = identity.WithUserID(ctx, n)
	}
	return req.WithContext(ctx)
}

// fakeAgentClient 最小 agent 客户端：只实现 AdminSessionStats（数据管理模块测试用）。
type fakeAgentClient struct {
	agentv1.AgentServiceClient
	stats     *agentv1.AdminSessionStatsResponse
	statsErr  error
	statsDays int32 // 记录最近一次请求的窗口
}

func (f *fakeAgentClient) AdminSessionStats(_ context.Context, req *agentv1.AdminSessionStatsRequest, _ ...grpc.CallOption) (*agentv1.AdminSessionStatsResponse, error) {
	f.statsDays = req.GetDays()
	if f.statsErr != nil {
		return nil, f.statsErr
	}
	if f.stats != nil {
		return f.stats, nil
	}
	return &agentv1.AdminSessionStatsResponse{}, nil
}

// fakeAuthClient 最小 auth 客户端：实现 AdminGetUsersByIds（用户名回填测试用）。
type fakeAuthClient struct {
	authpb.AuthServiceClient
	users  []*authpb.User
	getErr error
	gotIDs []string
}

func (f *fakeAuthClient) AdminGetUsersByIds(_ context.Context, req *authpb.AdminGetUsersByIdsRequest, _ ...grpc.CallOption) (*authpb.AdminGetUsersByIdsResponse, error) {
	f.gotIDs = req.GetUserIds()
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &authpb.AdminGetUsersByIdsResponse{Users: f.users}, nil
}

// newTestDataService 组装数据模块测试环境：本地 llm-gateway 桩 + fake 三端。
func newTestDataService(t *testing.T, agent agentv1.AgentServiceClient, auth authpb.AuthServiceClient, llmStub http.HandlerFunc) *Service {
	t.Helper()
	llmSrv := httptest.NewServer(llmStub)
	t.Cleanup(llmSrv.Close)
	s, err := NewService(Config{
		SkillsDir:         t.TempDir(),
		McpConfigFile:     t.TempDir() + "/mcp.json",
		McpServersDir:     t.TempDir(),
		Auth:              auth,
		Agent:             agent,
		LlmGatewayBaseURL: llmSrv.URL,
		LlmAdminToken:     "secret",
		Log:               zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return s
}

// llmOverviewStub 返回固定用量总览 JSON，并校验管理令牌。
func llmOverviewStub(tok string, missingToken *bool) http.HandlerFunc {
	body := `{"summary":{"calls":10,"success":9,"failed":1,"dau":3,"total_tokens":1000,"cost_usd":1.5},` +
		`"daily":[{"date":"2026-08-12","calls":10,"success":9,"failed":1,"dau":3,"total_tokens":1000,"cost_usd":1.5}],` +
		`"by_model":[{"key":"deepseek","calls":9,"total_tokens":900,"cost_usd":1.4}],` +
		`"by_agent":[{"key":"tutor","calls":5,"total_tokens":500,"cost_usd":0.8}],` +
		`"by_user":[{"user_id":1,"calls":5,"total_tokens":500,"cost_usd":0.8}]}`
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Admin-Token") != tok {
			if missingToken != nil {
				*missingToken = true
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}
}

func newDataHandler(s *Service) http.Handler {
	mux := http.NewServeMux()
	newDataModule(s).Register(mux, s)
	return mux
}

// TestData_Overview 正常聚合：会话统计 + 用量总览 + 用户名回填 + 令牌透传。
func TestData_Overview(t *testing.T) {
	var missingToken bool
	agent := &fakeAgentClient{stats: &agentv1.AdminSessionStatsResponse{
		Days:          []*agentv1.SessionDayStat{{Date: "2026-08-12", Sessions: 3}},
		Agents:        []*agentv1.SessionAgentStat{{AgentId: "tutor", Sessions: 3}},
		TotalSessions: 100,
	}}
	auth := &fakeAuthClient{users: []*authpb.User{{Id: "1", Username: "alice"}}}
	s := newTestDataService(t, agent, auth, llmOverviewStub("secret", &missingToken))

	req := withDataRole(httptest.NewRequest(http.MethodGet, "/v1/admin/data/overview?days=7", nil), "super_admin", "1")
	rec := httptest.NewRecorder()
	newDataHandler(s).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	if missingToken {
		t.Fatal("llm-gateway 请求应携带 X-Admin-Token")
	}
	if agent.statsDays != 7 {
		t.Fatalf("会话统计窗口应透传 7, got %d", agent.statsDays)
	}

	var ov dataOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatalf("响应非合法 JSON: %v", err)
	}
	if ov.Sessions == nil || ov.Sessions.Total != 100 || len(ov.Sessions.Days) != 1 || ov.Sessions.Days[0].Sessions != 3 {
		t.Fatalf("会话统计异常: %+v", ov.Sessions)
	}
	if ov.Usage == nil || ov.Usage.Summary.Calls != 10 || ov.Usage.Summary.DAU != 3 {
		t.Fatalf("用量异常: %+v", ov.Usage)
	}
	if ov.UserNames["1"] != "alice" {
		t.Fatalf("用户名回填异常: %+v", ov.UserNames)
	}
	if len(auth.gotIDs) != 1 || auth.gotIDs[0] != "1" {
		t.Fatalf("回填请求 ID 异常: %+v", auth.gotIDs)
	}
}

// TestData_Overview_AgentFail agent 会话统计失败 → 500（主数据不可用）。
func TestData_Overview_AgentFail(t *testing.T) {
	agent := &fakeAgentClient{statsErr: fmt.Errorf("agent down")}
	auth := &fakeAuthClient{}
	s := newTestDataService(t, agent, auth, llmOverviewStub("secret", nil))

	req := withDataRole(httptest.NewRequest(http.MethodGet, "/v1/admin/data/overview", nil), "super_admin", "1")
	rec := httptest.NewRecorder()
	newDataHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("agent 失败应 500, got %d", rec.Code)
	}
}

// TestData_Overview_LLMFail llm 用量失败（非 200）→ 500。
func TestData_Overview_LLMFail(t *testing.T) {
	agent := &fakeAgentClient{stats: &agentv1.AdminSessionStatsResponse{TotalSessions: 1}}
	auth := &fakeAuthClient{}
	llmStub := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}
	s := newTestDataService(t, agent, auth, llmStub)

	req := withDataRole(httptest.NewRequest(http.MethodGet, "/v1/admin/data/overview", nil), "super_admin", "1")
	rec := httptest.NewRecorder()
	newDataHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("llm 失败应 500, got %d", rec.Code)
	}
}

// TestData_Overview_UserNameFail 用户名回填失败不阻断主数据。
func TestData_Overview_UserNameFail(t *testing.T) {
	agent := &fakeAgentClient{stats: &agentv1.AdminSessionStatsResponse{TotalSessions: 1}}
	auth := &fakeAuthClient{getErr: fmt.Errorf("auth down")}
	s := newTestDataService(t, agent, auth, llmOverviewStub("secret", nil))

	req := withDataRole(httptest.NewRequest(http.MethodGet, "/v1/admin/data/overview", nil), "super_admin", "1")
	rec := httptest.NewRecorder()
	newDataHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("回填失败应仍返回 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"user_names":{}`) {
		t.Fatalf("回填失败时 user_names 应为空: %s", rec.Body.String())
	}
}

// TestData_Overview_BadDays 窗口参数非法 → 400。
func TestData_Overview_BadDays(t *testing.T) {
	agent := &fakeAgentClient{}
	auth := &fakeAuthClient{}
	s := newTestDataService(t, agent, auth, llmOverviewStub("secret", nil))
	for _, days := range []string{"abc", "0", "91"} {
		req := withDataRole(httptest.NewRequest(http.MethodGet, "/v1/admin/data/overview?days="+days, nil), "super_admin", "1")
		rec := httptest.NewRecorder()
		newDataHandler(s).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("days=%s 应 400, got %d", days, rec.Code)
		}
	}
}

// TestData_Overview_NonAdmin 非管理员身份 → 401（adminCtx 鉴权）。
func TestData_Overview_NonAdmin(t *testing.T) {
	agent := &fakeAgentClient{}
	auth := &fakeAuthClient{}
	s := newTestDataService(t, agent, auth, llmOverviewStub("secret", nil))
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/data/overview", nil) // 无身份
	rec := httptest.NewRecorder()
	newDataHandler(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无身份应 401, got %d", rec.Code)
	}
}

// TestDataModule_Implemented 数据源缺失时的模块状态判定。
func TestDataModule_Implemented(t *testing.T) {
	s, err := NewService(Config{
		SkillsDir:     t.TempDir(),
		McpConfigFile: t.TempDir() + "/mcp.json",
		McpServersDir: t.TempDir(),
		Log:           zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// 未配置 Agent / LlmGatewayBaseURL / LlmAdminToken → 未实现
	if newDataModule(s).Implemented() {
		t.Fatal("数据源缺失时应判定未实现")
	}
	// 配置齐备 → 已实现
	full, err := NewService(Config{
		SkillsDir:         t.TempDir(),
		McpConfigFile:     t.TempDir() + "/mcp.json",
		McpServersDir:     t.TempDir(),
		Auth:              &fakeAuthClient{},
		Agent:             &fakeAgentClient{},
		LlmGatewayBaseURL: "http://127.0.0.1:1",
		LlmAdminToken:     "secret",
		Log:               zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("NewService(full): %v", err)
	}
	if !newDataModule(full).Implemented() {
		t.Fatal("数据源齐备时应判定已实现")
	}
}
