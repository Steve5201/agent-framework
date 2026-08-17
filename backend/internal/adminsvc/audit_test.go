package adminsvc

// audit_test.go —— 审计日志（阶段4·日志管理）全链路测试：
//  1. AuditStore 落盘与查询（按域隔离 / action 过滤 / user 过滤 / 分页 / 倒序）；
//  2. WithAudit 中间件（写操作记录、GET 不记录、super_admin 目标域回退）；
//  3. GET /v1/admin/logs HTTP 权限（agent_admin 锁定本组、super_admin 全域/指定域）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// newAuditHTTPService 构造带审计中间件的 HTTP 测试服务（LogsDir 指向临时目录，
// 避免测试写入仓库工作目录）。返回 svc、server、审计日志根目录。
func newAuditHTTPService(t *testing.T) (*Service, *httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	svc, err := NewService(Config{
		SkillsDir:     filepath.Join(root, "skills"),
		McpConfigFile: filepath.Join(root, "mcp_servers.json"),
		LogsDir:       filepath.Join(root, "admin-logs"),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	srv := httptest.NewServer(withTestIdentity(svc.WithAudit(mux)))
	t.Cleanup(srv.Close)
	return svc, srv, filepath.Join(root, "admin-logs")
}

// auditEntry 构造测试用审计记录（TS 用固定时刻，便于倒序断言）。
func auditEntry(userID int64, role, action, agent string) AuditEntry {
	return AuditEntry{
		TS:          time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		UserID:      userID,
		Role:        role,
		TargetAgent: agent,
		Action:      action,
		Method:      http.MethodPost,
		Path:        "/v1/admin/test",
		Status:      http.StatusCreated,
	}
}

// ---------------------------------------------------------------------------
// 1. AuditStore：落盘 / 查询
// ---------------------------------------------------------------------------

func TestAuditStore_AppendAndList_AgentIsolation(t *testing.T) {
	root := t.TempDir()
	s := newAuditStore(root, nil)

	// 写两个域的日志。
	if err := s.Append("math", auditEntry(1, "super_admin", "skills.create", "math")); err != nil {
		t.Fatalf("Append math: %v", err)
	}
	if err := s.Append("math", auditEntry(1, "super_admin", "mcp.update", "math")); err != nil {
		t.Fatalf("Append math: %v", err)
	}
	if err := s.Append("physics", auditEntry(2, "agent_admin", "kb.delete", "physics")); err != nil {
		t.Fatalf("Append physics: %v", err)
	}

	// 按域过滤：math 只能看到 math 的 2 条。
	got, total, err := s.List(AuditFilter{AgentIDs: []string{"math"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("math 域期望 2 条，got total=%d len=%d", total, len(got))
	}
	for _, e := range got {
		if e.TargetAgent != "math" {
			t.Errorf("越域返回: %+v", e)
		}
	}

	// 全域扫描：3 条（两个域合并）。
	all, total, err := s.List(AuditFilter{})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if total != 3 || len(all) != 3 {
		t.Fatalf("全域期望 3 条，got total=%d len=%d", total, len(all))
	}
}

func TestAuditStore_FilterAndPaging(t *testing.T) {
	root := t.TempDir()
	s := newAuditStore(root, nil)

	// 写入 5 条，action 与 user 混合。
	entries := []AuditEntry{
		auditEntry(1, "super_admin", "skills.create", "tutor"),
		auditEntry(1, "super_admin", "skills.update", "tutor"),
		auditEntry(2, "admin", "mcp.update", "tutor"),
		auditEntry(2, "admin", "mcp.delete", "tutor"),
		auditEntry(3, "agent_admin", "kb.delete", "tutor"),
	}
	for i, e := range entries {
		e.TS = e.TS.Add(time.Duration(i) * time.Second) // 依次递增 1s，期望倒序
		if err := s.Append("tutor", e); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}

	// action 前缀过滤：mcp → 2 条。
	got, total, err := s.List(AuditFilter{AgentIDs: []string{"tutor"}, Action: "mcp"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("action=mcp 期望 2 条，got total=%d len=%d", total, len(got))
	}

	// user_id 过滤：1 → 2 条（skills.create / skills.update）。
	got, total, _ = s.List(AuditFilter{AgentIDs: []string{"tutor"}, UserID: 1})
	if total != 2 || len(got) != 2 {
		t.Fatalf("user_id=1 期望 2 条，got total=%d len=%d", total, len(got))
	}

	// 分页：page=1 size=2 → 前 2 条（时间倒序：最新的两条）。
	got, total, _ = s.List(AuditFilter{AgentIDs: []string{"tutor"}, Page: 1, PageSize: 2})
	if total != 5 || len(got) != 2 {
		t.Fatalf("分页期望 total=5 len=2，got total=%d len=%d", total, len(got))
	}
	if got[0].UserID != 3 || got[1].UserID != 2 {
		t.Fatalf("倒序错误: 期望 [user3, user2]，got [%d, %d]", got[0].UserID, got[1].UserID)
	}

	// 越界分页：page=99 → 空列表但 total 保留。
	got, total, _ = s.List(AuditFilter{AgentIDs: []string{"tutor"}, Page: 99, PageSize: 50})
	if len(got) != 0 || total != 5 {
		t.Fatalf("越界分页期望 len=0 total=5，got len=%d total=%d", len(got), total)
	}
}

func TestAuditStore_RejectBadAgentID(t *testing.T) {
	root := t.TempDir()
	s := newAuditStore(root, nil)

	// 目录穿越 / 非法 ID 一律拒绝（防写盘逃逸）。
	if err := s.Append("../../etc", auditEntry(1, "super_admin", "x", "../../etc")); err == nil {
		t.Fatal("期望非法 agentID 被拒绝")
	}
	if err := s.Append("a/b", auditEntry(1, "super_admin", "x", "a/b")); err == nil {
		t.Fatal("期望含斜杠 agentID 被拒绝")
	}

	// List 同样忽略非法域（不 panic、不越权）。
	got, total, err := s.List(AuditFilter{AgentIDs: []string{"../evil", "tutor"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 || len(got) != 0 {
		t.Fatalf("非法域+空 tutor 期望 0 条，got total=%d len=%d", total, len(got))
	}
}

// ---------------------------------------------------------------------------
// 2. WithAudit 中间件
// ---------------------------------------------------------------------------

func TestWithAudit_RecordsWritesOnly(t *testing.T) {
	svc, srv, logsRoot := newAuditHTTPService(t)
	_ = svc

	// 写操作：POST /v1/admin/skills（super_admin 未带 agent_id → 回退默认域 tutor）。
	resp, err := http.Post(srv.URL+"/v1/admin/skills", "application/json",
		http.NoBody) // body 缺失会 400，但审计仍应记录（动作已发生）
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()

	// 只读操作：GET /v1/admin/modules → 不记录。
	getResp, err := http.Get(srv.URL + "/v1/admin/modules")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = getResp.Body.Close()

	// 读回 tutor 域日志：应有 1 条 POST 记录。
	entries, total, err := newAuditStore(logsRoot, nil).List(AuditFilter{AgentIDs: []string{"tutor"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 {
		t.Fatalf("期望 1 条写操作记录，got total=%d", total)
	}
	e := entries[0]
	if e.Action != "skills.create" {
		t.Errorf("action 期望 skills.create，got %q", e.Action)
	}
	if e.TargetAgent != "tutor" {
		t.Errorf("super_admin 未指定域应回退 tutor，got %q", e.TargetAgent)
	}
	if e.Role != "super_admin" {
		t.Errorf("role 期望 super_admin，got %q", e.Role)
	}
}

// ---------------------------------------------------------------------------
// 3. GET /v1/admin/logs HTTP 权限与过滤
// ---------------------------------------------------------------------------

// doLogsReq 发送日志查询请求（携带角色/域头）并解析响应。
func doLogsReq(t *testing.T, srv *httptest.Server, role, agentID, query string) (code int, logs []AuditEntry, total int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/admin/logs"+query, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	withRoleHeader(req, role, agentID)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Logs  []AuditEntry `json:"logs"`
		Total int          `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, body.Logs, body.Total
}

func TestHandleListLogs_AgentAdminLocked(t *testing.T) {
	_, srv, logsRoot := newAuditHTTPService(t)

	// 预置两域日志。
	s := newAuditStore(logsRoot, nil)
	if err := s.Append("physics", auditEntry(9, "agent_admin", "skills.create", "physics")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("math", auditEntry(1, "super_admin", "skills.create", "math")); err != nil {
		t.Fatal(err)
	}

	// agent_admin（归属 physics）即使请求带 agent_id=math 也只能看 physics。
	code, logs, total := doLogsReq(t, srv, "agent_admin", "physics", "?agent_id=math")
	if code != http.StatusOK {
		t.Fatalf("期望 200，got %d", code)
	}
	if total != 1 || len(logs) != 1 || logs[0].TargetAgent != "physics" {
		t.Fatalf("agent_admin 应锁定本组 physics 共 1 条，got total=%d len=%d first=%+v", total, len(logs), logs[0])
	}

	// 普通管理员同理锁定自身域。
	code, logs, total = doLogsReq(t, srv, "admin", "physics", "")
	if code != http.StatusOK || total != 1 {
		t.Fatalf("admin 应锁定 physics 共 1 条，got code=%d total=%d", code, total)
	}
}

func TestHandleListLogs_SuperAdminScopes(t *testing.T) {
	_, srv, logsRoot := newAuditHTTPService(t)

	s := newAuditStore(logsRoot, nil)
	if err := s.Append("physics", auditEntry(9, "agent_admin", "skills.create", "physics")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("math", auditEntry(1, "super_admin", "skills.create", "math")); err != nil {
		t.Fatal(err)
	}

	// 全域：2 条。
	code, _, total := doLogsReq(t, srv, "super_admin", "", "")
	if code != http.StatusOK || total != 2 {
		t.Fatalf("super_admin 全域期望 2 条，got code=%d total=%d", code, total)
	}

	// 指定域 agent_id=math：1 条。
	code, logs, total := doLogsReq(t, srv, "super_admin", "", "?agent_id=math")
	if code != http.StatusOK || total != 1 || logs[0].TargetAgent != "math" {
		t.Fatalf("super_admin 指定 math 期望 1 条，got code=%d total=%d logs=%+v", code, total, logs)
	}
}

func TestHandleListLogs_ForbiddenForNonAdmin(t *testing.T) {
	_, srv, _ := newAuditHTTPService(t)
	// 普通用户（role=user，X-Test-Role）→ 403。
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/admin/logs", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	withRoleHeader(req, "user", "")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("普通用户期望 403，got %d", resp.StatusCode)
	}
}
