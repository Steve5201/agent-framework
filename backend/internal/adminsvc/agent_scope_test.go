package adminsvc

// agent_scope_test.go —— 阶段3·多租户资源域解析与隔离专项测试：
//  1. agentScopeFor：超管显式指定 / 缺省回退、agent_admin/admin 锁定自身归属、
//     非管理员拒绝、非法域 ID（防目录穿越）拒绝；
//  2. SkillStore.For / McpStore.For：不同智能体域物理隔离（目录/文件独立）；
//  3. HTTP 层：超管 ?agent_id= 落盘到指定域；agent_admin 越权指定其它域被忽略。
//
// 运行：cd backend && go test ./internal/adminsvc -run AgentScope -v

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Steve5201/agent-backend/internal/tools/mcp"
)

func TestAgentScopeFor(t *testing.T) {
	t.Run("super_admin 显式指定域", func(t *testing.T) {
		req := withRole(httptest.NewRequest(http.MethodGet, "/", nil), "super_admin", "")
		agent, err := agentScopeFor(req, "math")
		if err != nil || agent != "math" {
			t.Fatalf("got %q, %v", agent, err)
		}
	})
	t.Run("super_admin 缺省回退默认域", func(t *testing.T) {
		req := withRole(httptest.NewRequest(http.MethodGet, "/", nil), "super_admin", "")
		agent, err := agentScopeFor(req, "  ")
		if err != nil || agent != defaultAgentID {
			t.Fatalf("got %q, %v（期望 %q）", agent, err, defaultAgentID)
		}
	})
	t.Run("agent_admin 锁定自身归属", func(t *testing.T) {
		req := withRole(httptest.NewRequest(http.MethodGet, "/", nil), "agent_admin", "math")
		// 尝试在 URL 指定其它域 → 仍锁定 math，防越权。
		agent, err := agentScopeFor(req, "other-agent")
		if err != nil || agent != "math" {
			t.Fatalf("got %q, %v（应锁定 math）", agent, err)
		}
	})
	t.Run("admin 缺省回退默认域", func(t *testing.T) {
		req := withRole(httptest.NewRequest(http.MethodGet, "/", nil), "admin", "")
		agent, err := agentScopeFor(req, "")
		if err != nil || agent != defaultAgentID {
			t.Fatalf("got %q, %v", agent, err)
		}
	})
	t.Run("普通用户拒绝", func(t *testing.T) {
		req := withRole(httptest.NewRequest(http.MethodGet, "/", nil), "user", "")
		if _, err := agentScopeFor(req, ""); err == nil {
			t.Fatal("普通用户不应有资源管理权限")
		}
	})
	t.Run("未注入身份拒绝", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if _, err := agentScopeFor(req, ""); err == nil {
			t.Fatal("无身份上下文应拒绝（网关中间件保证生产环境必有）")
		}
	})
	t.Run("非法域 ID 拒绝（防目录穿越）", func(t *testing.T) {
		req := withRole(httptest.NewRequest(http.MethodGet, "/", nil), "super_admin", "")
		for _, bad := range []string{"../etc", "a/b", "a\\b", "..", ".", "tutor;rm", "汉字"} {
			if _, err := agentScopeFor(req, bad); err == nil {
				t.Fatalf("%q 应被拒绝", bad)
			}
		}
	})
}

func TestSkillStore_For_AgentIsolation(t *testing.T) {
	root := t.TempDir()
	base := newSkillStore(root)

	tutor := base.For("tutor")
	math := base.For("math")
	if _, err := tutor.Create(t.Context(), "tutor-skill", sk("tutor-skill", "tutor 域", "1.0.0", "正文")); err != nil {
		t.Fatal(err)
	}
	if _, err := math.Create(t.Context(), "math-skill", sk("math-skill", "math 域", "1.0.0", "正文")); err != nil {
		t.Fatal(err)
	}

	// 各域列表互不可见。
	if got, _ := tutor.List(t.Context()); len(got) != 1 || got[0].Name != "tutor-skill" {
		t.Fatalf("tutor 域应只见自己的技能: %+v", got)
	}
	if got, _ := math.List(t.Context()); len(got) != 1 || got[0].Name != "math-skill" {
		t.Fatalf("math 域应只见自己的技能: %+v", got)
	}
	// 磁盘物理隔离：<root>/<agentID>/<name>/SKILL.md。
	for _, rel := range []string{"tutor/tutor-skill/SKILL.md", "math/math-skill/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s 不存在: %v", rel, err)
		}
	}
}

func TestMcpStore_For_AgentIsolation(t *testing.T) {
	baseDir := t.TempDir()
	base := newMcpStore(filepath.Join(baseDir, "mcp_servers.json"), filepath.Join(baseDir, "mcp-servers"))

	tutor := base.For("tutor")
	math := base.For("math")

	cfg := mcp.ServerConfig{Name: "demo", Transport: mcp.TransportStdio, Command: "python3", Args: []string{"main.py"}}
	if _, err := tutor.Create(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := math.Create(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}

	// 配置按域分文件：<目录>/<agentID>/mcp_servers.json。
	for _, rel := range []string{"tutor/mcp_servers.json", "math/mcp_servers.json"} {
		if _, err := os.Stat(filepath.Join(baseDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s 不存在: %v", rel, err)
		}
	}
	// 各自列表互不可见（服务器名相同也互不干扰）。
	if got, _ := tutor.List(t.Context()); len(got) != 1 || got[0].Name != "demo" {
		t.Fatalf("tutor 域配置异常: %+v", got)
	}
	if got, _ := math.List(t.Context()); len(got) != 1 || got[0].Name != "demo" {
		t.Fatalf("math 域配置异常: %+v", got)
	}
}

// TestSkillUploadHTTP_AgentScoped 超管经 ?agent_id= 指定域：落盘与列表均按域隔离。
func TestSkillUploadHTTP_AgentScoped(t *testing.T) {
	_, srv, root := newHTTPService(t)

	zipData := makeZip(t, []string{"math-skill/SKILL.md|" + sk("math-skill", "math 域技能", "1.0.0", "正文")})
	status, body := doReq(t, srv, multipartUpload(t, srv, nil, "file", "m.zip", zipData, "agent_id=math"))
	if status != http.StatusCreated {
		t.Fatalf("上传失败 %d %+v", status, body)
	}
	if body["agent_id"] != "math" {
		t.Fatalf("响应 agent_id 应为 math，实际 %v", body["agent_id"])
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "math", "math-skill", "SKILL.md")); err != nil {
		t.Fatalf("math 域技能目录未建立: %v", err)
	}
	// 默认域不应看到 math 域的资源。
	if _, err := os.Stat(filepath.Join(root, "skills", "tutor", "math-skill")); err == nil {
		t.Fatal("默认域不应出现 math 域技能")
	}

	// 列表按域返回。
	status, body = doReq(t, srv, httpReq(t, srv, "/v1/admin/skills?agent_id=math"))
	if status != http.StatusOK {
		t.Fatalf("列表失败 %d: %+v", status, body)
	}
	skills, _ := body["skills"].([]any)
	if len(skills) != 1 {
		t.Fatalf("math 域应只有 1 个技能，实际 %d", len(skills))
	}
	// 缺省域（tutor）列表应为空。
	status, body = doReq(t, srv, httpReq(t, srv, "/v1/admin/skills"))
	if status != http.StatusOK {
		t.Fatalf("列表失败 %d: %+v", status, body)
	}
	skills, _ = body["skills"].([]any)
	if len(skills) != 0 {
		t.Fatalf("默认域应无技能，实际 %d", len(skills))
	}
}

// httpReq 构造发给测试服务器的 GET 请求（http.NewRequest：避免 httptest 的
// RequestURI 字段导致 http.Client 拒绝）。
func httpReq(t *testing.T, srv *httptest.Server, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// TestSkillUploadHTTP_AgentAdminLocked agent_admin 即使在 URL 指定其它域，
// 资源也必须落到自身归属域（防越权管理其它智能体资源）。
func TestSkillUploadHTTP_AgentAdminLocked(t *testing.T) {
	_, srv, root := newHTTPService(t)

	zipData := makeZip(t, []string{"locked-skill/SKILL.md|" + sk("locked-skill", "锁域", "1.0.0", "正文")})
	req := withRoleHeader(multipartUpload(t, srv, nil, "file", "l.zip", zipData, "agent_id=other"), "agent_admin", "math")
	status, body := doReq(t, srv, req)
	if status != http.StatusCreated {
		t.Fatalf("上传失败 %d %+v", status, body)
	}
	if body["agent_id"] != "math" {
		t.Fatalf("agent_admin 应锁定自身域 math，实际 %v", body["agent_id"])
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "math", "locked-skill", "SKILL.md")); err != nil {
		t.Fatalf("math 域技能目录未建立: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "other", "locked-skill")); err == nil {
		t.Fatal("越权域 other 不应写入任何文件")
	}
}
