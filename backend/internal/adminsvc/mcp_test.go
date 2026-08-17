package adminsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-backend/internal/tools/mcp"
)

func TestMcpStoreRoundtrip(t *testing.T) {
	store := newMcpStore(filepath.Join(t.TempDir(), "mcp_servers.json"), filepath.Join(t.TempDir(), "mcp-servers"))

	// 初始为空
	list, err := store.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("初始 List = %v, %v", list, err)
	}

	cfg := mcp.ServerConfig{
		Name:    "github",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-github"},
	}
	created, err := store.Create(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// transport 缺省补全为 stdio
	if created.Transport != mcp.TransportStdio {
		t.Errorf("transport = %q, want stdio（缺省补全）", created.Transport)
	}

	// List 持久化可见
	list, err = store.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}

	// Get
	got, err := store.Get(context.Background(), "github")
	if err != nil || got.Name != "github" {
		t.Fatalf("Get: %v %+v", err, got)
	}

	// Update（改 http 模式）
	updated, err := store.Update(context.Background(), "github", mcp.ServerConfig{
		Name:      "github",
		Transport: mcp.TransportHTTP,
		URL:       "https://mcp.example.com/github",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Transport != mcp.TransportHTTP {
		t.Errorf("Update 后 transport = %q", updated.Transport)
	}

	// Delete
	if err := store.Delete(context.Background(), "github"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = store.Get(context.Background(), "github")
	if apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("Delete 后 Get: code = %s, want NotFound", apperr.CodeOf(err))
	}
}

func TestMcpStoreDuplicate(t *testing.T) {
	store := newMcpStore(filepath.Join(t.TempDir(), "mcp.json"), filepath.Join(t.TempDir(), "mcp-servers"))
	cfg := mcp.ServerConfig{Name: "s1", Command: "echo"}
	if _, err := store.Create(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	_, err := store.Create(context.Background(), cfg)
	if apperr.CodeOf(err) != apperr.CodeAlreadyExists {
		t.Fatalf("Create duplicate: code = %s, want AlreadyExists", apperr.CodeOf(err))
	}
}

func TestMcpStoreInvalidConfig(t *testing.T) {
	store := newMcpStore(filepath.Join(t.TempDir(), "mcp.json"), filepath.Join(t.TempDir(), "mcp-servers"))
	cases := []mcp.ServerConfig{
		{Name: ""},                                         // 缺 name
		{Name: "x", Transport: "stdio"},                    // stdio 缺 command
		{Name: "x", Transport: "udp"},                      // 非法 transport
		{Name: "x", Transport: "http"},                     // http 缺 url
		{Name: "x", Command: "c", DefaultPermission: "L9"}, // 非法权限
		{Name: "../evil", Command: "c"},                    // 非法名字（防穿越）
	}
	for i, c := range cases {
		if _, err := store.Create(context.Background(), c); err == nil {
			t.Errorf("case %d (%q) 应拒绝非法配置", i, c.Name)
		}
	}
}

func TestMcpStoreUpdateNameChangeRejected(t *testing.T) {
	store := newMcpStore(filepath.Join(t.TempDir(), "mcp.json"), filepath.Join(t.TempDir(), "mcp-servers"))
	if _, err := store.Create(context.Background(), mcp.ServerConfig{Name: "a", Command: "echo"}); err != nil {
		t.Fatal(err)
	}
	// 通过路径 name=a 更新，body 却叫 b → 拒绝
	_, err := store.Update(context.Background(), "a", mcp.ServerConfig{Name: "b", Command: "echo"})
	if apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("改 name 应拒绝: %v", err)
	}
}

func TestMcpStoreCorruptedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newMcpStore(path, filepath.Join(t.TempDir(), "mcp-servers"))
	if _, err := store.List(context.Background()); err == nil {
		t.Fatal("损坏的配置文件应返回错误")
	}
}

func TestMcpStoreMissingFileIsEmpty(t *testing.T) {
	store := newMcpStore(filepath.Join(t.TempDir(), "mcp.json"), filepath.Join(t.TempDir(), "mcp-servers"))
	list, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("文件不存在应为空列表, got %v", list)
	}
}

func TestMcpStoreSetEnabled(t *testing.T) {
	store := newMcpStore(filepath.Join(t.TempDir(), "mcp.json"), filepath.Join(t.TempDir(), "mcp-servers"))
	if _, err := store.Create(context.Background(), mcp.ServerConfig{Name: "s1", Command: "echo"}); err != nil {
		t.Fatal(err)
	}

	// 默认启用
	cfg, err := store.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsEnabled() {
		t.Error("默认应为启用（enabled 缺省）")
	}

	// 禁用
	cfg, err = store.SetEnabled(context.Background(), "s1", false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IsEnabled() {
		t.Error("SetEnabled(false) 后应禁用")
	}
	// 持久化：重新从文件读取
	cfg, err = store.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IsEnabled() {
		t.Error("重新读取后仍应为禁用（已持久化 enabled=false）")
	}

	// 再启用：command=echo 不是合法 MCP server → 真实连接失败 → 不启用并明确禁用
	_, err = store.SetEnabled(context.Background(), "s1", true)
	if err == nil {
		t.Fatal("启用不可连接的 MCP server 应失败（启用 = 真实连接验证）")
	}
	cfg, err = store.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IsEnabled() {
		t.Error("连接失败后不应被启用")
	}
	if cfg.DiscoveryError == "" {
		t.Error("连接失败应记录 discovery_error")
	}

	// 不存在的 server → NotFound
	if _, err := store.SetEnabled(context.Background(), "nope", false); apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Fatalf("SetEnabled nonexistent: code = %s, want NotFound", apperr.CodeOf(err))
	}
}

// TestMcpUploadLocal 上传本地 MCP 代码包：解压到 serversDir/<name>/、
// 自动识别入口、注册 stdio 配置（command=python3, args=[main.py], cwd=代码目录）。
func TestMcpUploadLocal(t *testing.T) {
	serversDir := filepath.Join(t.TempDir(), "mcp-servers")
	store := newMcpStore(filepath.Join(t.TempDir(), "mcp.json"), serversDir)

	zipData := makeZip(t, []string{
		"my-mcp/main.py|print('fake mcp')",
		"my-mcp/requirements.txt|mcp",
		"my-mcp/README.md|docs",
	})
	// 不指定 entry：自动检测（在 my-mcp/ 下找到 main.py）
	cfg, err := store.UploadLocal(context.Background(), "", "", "my-mcp.zip", zipData, false)
	if err != nil {
		t.Fatalf("UploadLocal: %v", err)
	}
	if cfg.Name != "my-mcp" {
		t.Fatalf("默认名应取 zip 文件名 my-mcp，got %q", cfg.Name)
	}
	if cfg.Transport != "stdio" || cfg.Command != "python3" || len(cfg.Args) != 1 || cfg.Args[0] != "main.py" {
		t.Fatalf("stdio 配置错误: %+v", cfg)
	}
	if cfg.Cwd == "" || !strings.HasSuffix(strings.ReplaceAll(cfg.Cwd, "\\", "/"), "mcp-servers/my-mcp") {
		t.Fatalf("cwd 应指向代码目录: %q", cfg.Cwd)
	}
	// 代码落盘（含嵌套子目录）
	for _, rel := range []string{"main.py", "requirements.txt", "README.md"} {
		if _, err := os.Stat(filepath.Join(serversDir, "my-mcp", rel)); err != nil {
			t.Fatalf("MCP 代码未落盘 %s: %v", rel, err)
		}
	}
	// 配置已持久化
	got, err := store.Get(context.Background(), "my-mcp")
	if err != nil || got.Command != "python3" {
		t.Fatalf("配置未持久化: %+v %v", got, err)
	}

	// 同名且未显式 overwrite → 拒绝覆盖（返回 ALREADY_EXISTS，由前端提示确认）。
	if _, err := store.UploadLocal(context.Background(), "my-mcp", "app.py", "x.zip", makeZip(t, []string{"app.py|new"}), false); err == nil {
		t.Fatal("同名上传未带 overwrite 应返回冲突错误")
	}
	// 同名 + overwrite=true → 覆盖更新配置与代码
	cfg2, err := store.UploadLocal(context.Background(), "my-mcp", "app.py", "x.zip", makeZip(t, []string{"app.py|new"}), true)
	if err != nil {
		t.Fatalf("覆盖上传: %v", err)
	}
	if cfg2.Command != "python3" || cfg2.Args[0] != "app.py" {
		t.Fatalf("覆盖后入口应更新为 app.py: %+v", cfg2)
	}
	// 无入口文件 → 拒绝
	if _, err := store.UploadLocal(context.Background(), "bad", "", "b.zip", makeZip(t, []string{"data.txt|hi"}), false); err == nil {
		t.Fatal("无入口文件的 MCP 应拒绝")
	}
	// 非法名字 → 拒绝
	if _, err := store.UploadLocal(context.Background(), "../evil", "", "e.zip", makeZip(t, []string{"main.py|x"}), false); err == nil {
		t.Fatal("非法名字应拒绝")
	}
}
