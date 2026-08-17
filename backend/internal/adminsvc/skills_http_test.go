package adminsvc

// skills_http_test.go —— 走完整 HTTP handler 的实测验证：
//  1. 上传 zip 后目录结构（嵌套 ref/docs/assets）完整保留；
//  2. 同名同版本上传 → VERSION_CONFLICT，?overwrite=true 覆盖；
//  3. 中文名 zip 自动提取技能名。
//
// 用真实 multipart 请求 + httptest，覆盖 handler → store → 磁盘全链路。

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Steve5201/agent-backend/internal/identity"
)

// newHTTPService 构造带路由的测试服务（SkillsDir/McpConfigFile 指向临时目录）。
// 用 withTestIdentity 中间件包裹：httptest.NewServer 的客户端 context 不会传到
// 服务端 handler，身份经 X-Test-Role / X-Test-Agent 请求头重建（模拟 gateway
// RequireAuth 的 context 注入）。
func newHTTPService(t *testing.T) (*Service, *httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	svc, err := NewService(Config{
		SkillsDir:     filepath.Join(root, "skills"),
		McpConfigFile: filepath.Join(root, "mcp_servers.json"),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	srv := httptest.NewServer(withTestIdentity(mux))
	t.Cleanup(srv.Close)
	return svc, srv, root
}

// withTestIdentity 测试身份中间件：从请求头读取 X-Test-Role / X-Test-Agent，
// 写入 identity context（缺省最高超管、无 agent）。模拟 gateway RequireAuth。
func withTestIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get("X-Test-Role")
		if role == "" {
			role = "super_admin"
		}
		ctx := identity.WithRole(r.Context(), role)
		if agent := r.Header.Get("X-Test-Agent"); agent != "" {
			ctx = identity.WithAgentID(ctx, agent)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withRole 在请求 context 写入身份（进程内直连 handler 的场景，如 kb_test /
// agentScopeFor 单测；进程内调用 context 会原样传给 handler）。
func withRole(req *http.Request, role, agentID string) *http.Request {
	ctx := identity.WithRole(req.Context(), role)
	if agentID != "" {
		ctx = identity.WithAgentID(ctx, agentID)
	}
	return req.WithContext(ctx)
}

// withRoleHeader 经请求头标记身份（httptest.NewServer 场景，配合 withTestIdentity）。
func withRoleHeader(req *http.Request, role, agentID string) *http.Request {
	req.Header.Set("X-Test-Role", role)
	if agentID != "" {
		req.Header.Set("X-Test-Agent", agentID)
	}
	return req
}

// multipartUpload 构造 multipart 上传请求（file 字段 + 可选其它字段 + 可选 query）。
func multipartUpload(t *testing.T, srv *httptest.Server, fields map[string]string, fileField, fileName string, data []byte, query string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := w.CreateFormFile(fileField, fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/admin/skills/upload?"+query, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// doReq 发送请求并返回状态码与 JSON body。
// 未指定 X-Test-Role 时默认补最高超管（无智能体归属 → 资源域回退默认域 tutor）；
// 已用 withRoleHeader 指定角色/域的请求保持原样（如 agent_admin 域锁定测试）。
func doReq(t *testing.T, srv *httptest.Server, req *http.Request) (int, map[string]any) {
	t.Helper()
	if req.Header.Get("X-Test-Role") == "" {
		req.Header.Set("X-Test-Role", "super_admin")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestSkillUploadHTTP_PreservesStructure(t *testing.T) {
	_, srv, root := newHTTPService(t)

	skillMD := sk("nested-skill", "嵌套结构技能", "1.0.0", "详见 docs/guide.md 与 ref/a.md")
	zipData := makeZip(t, []string{
		"nested-skill/SKILL.md|" + skillMD,
		"nested-skill/ref/a.md|# 参考 A",
		"nested-skill/docs/guide.md|# 指南",
		"nested-skill/assets/img/pic.png|pngdata",
	})
	req := multipartUpload(t, srv, nil, "file", "nested.zip", zipData, "")

	status, body := doReq(t, srv, req)
	if status != http.StatusCreated {
		t.Fatalf("上传状态 = %d, body=%+v", status, body)
	}
	// 磁盘结构断言：嵌套目录 + 文件全部保留，且与 SKILL.md 同级。
	// 多租户：默认域 tutor → <skills>/tutor/<name>/<file>。
	for _, rel := range []string{
		"nested-skill/SKILL.md",
		"nested-skill/ref/a.md",
		"nested-skill/docs/guide.md",
		"nested-skill/assets/img/pic.png",
	} {
		if _, err := os.Stat(filepath.Join(root, "skills", "tutor", rel)); err != nil {
			t.Fatalf("结构未保留 %s: %v", rel, err)
		}
	}
	// 内容正确
	if b, _ := os.ReadFile(filepath.Join(root, "skills", "tutor", "nested-skill", "ref", "a.md")); string(b) != "# 参考 A" {
		t.Fatalf("ref/a.md 内容错误: %q", b)
	}
}

func TestSkillUploadHTTP_SameNameSameVersionConflict(t *testing.T) {
	_, srv, root := newHTTPService(t)

	// 首次上传创建 1.0.0
	first := makeZip(t, []string{"my-script/SKILL.md|" + sk("my-script", "v1", "1.0.0", "body1")})
	status, _ := doReq(t, srv, multipartUpload(t, srv, nil, "file", "a.zip", first, ""))
	if status != http.StatusCreated {
		t.Fatalf("首次上传失败: %d", status)
	}

	// 同名同版本（内容不同）→ 409 VERSION_CONFLICT
	same := makeZip(t, []string{"my-script/SKILL.md|" + sk("my-script", "v1改", "1.0.0", "body1-changed")})
	status, body := doReq(t, srv, multipartUpload(t, srv, nil, "file", "b.zip", same, ""))
	if status != http.StatusConflict {
		t.Fatalf("同名同版本应 409，实际 %d body=%+v", status, body)
	}
	if int(body["code"].(float64)) != 40902 {
		t.Fatalf("错误码应为 40902(VERSION_CONFLICT)，实际 %v", body["code"])
	}
	// 当前内容未被改动
	cur, _ := os.ReadFile(filepath.Join(root, "skills", "tutor", "my-script", "SKILL.md"))
	if !strings.Contains(string(cur), "body1") {
		t.Fatalf("冲突后当前内容不应改变: %q", cur)
	}

	// ?overwrite=true → 覆盖成功
	status, body = doReq(t, srv, multipartUpload(t, srv, nil, "file", "c.zip", same, "overwrite=true"))
	if status != http.StatusCreated {
		t.Fatalf("overwrite 上传应成功: %d %+v", status, body)
	}
	cur, _ = os.ReadFile(filepath.Join(root, "skills", "tutor", "my-script", "SKILL.md"))
	if !strings.Contains(string(cur), "body1-changed") {
		t.Fatalf("覆盖后内容未更新: %q", cur)
	}
}

func TestSkillUploadHTTP_ChineseNameAutoDetect(t *testing.T) {
	_, srv, root := newHTTPService(t)

	// 中文名：frontmatter name 自动提取为技能目录名
	skillMD := sk("数据分析助手", "自动分析", "1.0.0", "正文")
	zipData := makeZip(t, []string{"数据分析助手/SKILL.md|" + skillMD})
	status, body := doReq(t, srv, multipartUpload(t, srv, nil, "file", "随便.zip", zipData, ""))
	if status != http.StatusCreated {
		t.Fatalf("中文名上传失败: %d %+v", status, body)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "tutor", "数据分析助手", "SKILL.md")); err != nil {
		t.Fatalf("中文技能目录未建立: %v", err)
	}
}
