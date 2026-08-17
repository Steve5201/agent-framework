package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
)

// fileOpsArgsJSON 构造 file_ops 调用参数。
func fileOpsArgsJSON(action, path, content string, recursive bool) json.RawMessage {
	m := map[string]any{
		"action": action,
		"path":   path,
	}
	if content != "" {
		m["content"] = content
	}
	if recursive {
		m["recursive"] = true
	}
	b, _ := json.Marshal(m)
	return b
}

// fileOpsTreeArgsJSON 构造 tree 调用参数（list + tree=true）。
func fileOpsTreeArgsJSON(path string, depth int) json.RawMessage {
	m := map[string]any{
		"action": "list",
		"path":   path,
		"tree":   true,
	}
	if depth > 0 {
		m["depth"] = depth
	}
	b, _ := json.Marshal(m)
	return b
}

// fileOpsSearchArgsJSON 构造 search 调用参数。
func fileOpsSearchArgsJSON(path, query string, nameOnly bool, maxResults int) json.RawMessage {
	m := map[string]any{
		"action": "search",
		"path":   path,
		"query":  query,
	}
	if nameOnly {
		m["name_only"] = true
	}
	if maxResults > 0 {
		m["max_results"] = maxResults
	}
	b, _ := json.Marshal(m)
	return b
}

// TestFileOpsTool_EnsuresSandboxWorkspace 用户隔离 + 沙盒模式下：
// 用户工作区首次访问前自动委托 sandbox 初始化（幂等，目录存在后不再触发）。
func TestFileOpsTool_EnsuresSandboxWorkspace(t *testing.T) {
	root := t.TempDir()
	var inits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/exec" || r.Method != http.MethodPost {
			t.Errorf("非法沙盒请求: %s %s", r.Method, r.URL.Path)
		}
		inits++
		// 模拟沙盒 ensureWorkspace：以 root 创建用户目录
		_ = os.MkdirAll(filepath.Join(root, "users", "42"), 0o755)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sandboxExecResponse{ExitCode: 0, Stdout: ""})
	}))
	defer srv.Close()

	tool := &FileOpsTool{Root: root, SandboxURL: srv.URL}
	ctx := llm.WithHeader(context.Background(), userIDHeader, "42")

	// 用户目录尚不存在 → 首次访问触发一次沙盒初始化，之后正常操作
	if _, err := tool.Execute(ctx, fileOpsArgsJSON("list", ".", "", false)); err != nil {
		t.Fatalf("首次 list 失败: %v", err)
	}
	if inits != 1 {
		t.Fatalf("沙盒初始化应恰好触发 1 次，实际 %d", inits)
	}
	ws := filepath.Join(root, "users", "42")
	if st, err := os.Stat(ws); err != nil || !st.IsDir() {
		t.Fatalf("用户工作区应已创建（%s）: %v", ws, err)
	}

	// 目录已存在 → 后续访问零开销，不再触发初始化
	if _, err := tool.Execute(ctx, fileOpsArgsJSON("write", "note.txt", "hi", false)); err != nil {
		t.Fatalf("后续 write 失败: %v", err)
	}
	if inits != 1 {
		t.Fatalf("目录存在后不应再触发初始化，实际 %d 次", inits)
	}
}

// TestFileOpsTool_SandboxInitFailure 沙盒不可达时给出明确错误而非静默降级。
func TestFileOpsTool_SandboxInitFailure(t *testing.T) {
	root := t.TempDir()
	tool := &FileOpsTool{Root: root, SandboxURL: "http://127.0.0.1:1"} // 必然连接失败
	ctx := llm.WithHeader(context.Background(), userIDHeader, "7")

	_, err := tool.Execute(ctx, fileOpsArgsJSON("list", ".", "", false))
	if err == nil || !strings.Contains(err.Error(), "初始化失败") {
		t.Fatalf("沙盒不可达应返回初始化失败错误，实际: %v", err)
	}
}

func TestFileOpsTool_RoundTrip(t *testing.T) {
	root := t.TempDir()
	tool := &FileOpsTool{Root: root}
	ctx := context.Background()

	// write：自动创建父目录
	out, err := tool.Execute(ctx, fileOpsArgsJSON("write", "docs/report.txt", "内置工具测试内容", false))
	if err != nil {
		t.Fatalf("write 失败: %v", err)
	}
	if !strings.Contains(out, "已写入") {
		t.Fatalf("write 输出异常: %s", out)
	}

	// read：读回内容
	out, err = tool.Execute(ctx, fileOpsArgsJSON("read", "docs/report.txt", "", false))
	if err != nil {
		t.Fatalf("read 失败: %v", err)
	}
	if out != "内置工具测试内容" {
		t.Fatalf("read 输出 = %q", out)
	}

	// stat：查看文件信息
	out, err = tool.Execute(ctx, fileOpsArgsJSON("stat", "docs/report.txt", "", false))
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if !strings.Contains(out, "类型：文件") || !strings.Contains(out, "report.txt") {
		t.Fatalf("stat 输出异常: %s", out)
	}

	// list：列出目录
	out, err = tool.Execute(ctx, fileOpsArgsJSON("list", "docs", "", false))
	if err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	if !strings.Contains(out, "- [文件] report.txt") {
		t.Fatalf("list 输出缺少文件: %s", out)
	}

	// list 递归：子目录内文件也列出（展示统一为斜杠分隔，与 file_ops 路径约定一致）
	out, err = tool.Execute(ctx, fileOpsArgsJSON("list", ".", "", true))
	if err != nil {
		t.Fatalf("list 递归失败: %v", err)
	}
	if !strings.Contains(out, "- [文件] "+filepath.ToSlash(filepath.Join("docs", "report.txt"))) {
		t.Fatalf("list 递归输出缺少子目录文件: %s", out)
	}
}

func TestFileOpsTool_PathEscapeRejected(t *testing.T) {
	root := t.TempDir()
	tool := &FileOpsTool{Root: root}
	ctx := context.Background()

	bad := []string{
		"../../etc/passwd", // 相对路径越界
		"docs/../../../etc/passwd",
		"/etc/passwd", // 绝对路径越界
		"..",          // 根目录上一级
	}
	for _, p := range bad {
		if _, err := tool.Execute(ctx, fileOpsArgsJSON("read", p, "", false)); err == nil {
			t.Errorf("路径 %q 应被拒绝（越出工作目录）", p)
		}
	}
}

func TestFileOpsTool_EdgeCases(t *testing.T) {
	root := t.TempDir()
	tool := &FileOpsTool{Root: root}
	ctx := context.Background()

	// 未知 action
	if _, err := tool.Execute(ctx, fileOpsArgsJSON("delete", "x.txt", "", false)); err == nil ||
		!strings.Contains(err.Error(), "未知 action") {
		t.Errorf("未知 action 应报错，实际 err=%v", err)
	}

	// 读不存在的文件
	if _, err := tool.Execute(ctx, fileOpsArgsJSON("read", "nope.txt", "", false)); err == nil ||
		!strings.Contains(err.Error(), "不存在") {
		t.Errorf("读不存在文件应报错，实际 err=%v", err)
	}

	// 对目录 read / 对文件 list
	if _, err := tool.Execute(ctx, fileOpsArgsJSON("read", ".", "", false)); err == nil ||
		!strings.Contains(err.Error(), "目录") {
		t.Errorf("对目录 read 应报错，实际 err=%v", err)
	}

	// 非 UTF-8 文件（如 png 头）拒绝以文本读取
	binPath := filepath.Join(root, "x.png")
	if err := os.WriteFile(binPath, []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0xFF}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(ctx, fileOpsArgsJSON("read", "x.png", "", false)); err == nil ||
		!strings.Contains(err.Error(), "非 UTF-8") {
		t.Errorf("二进制文件 read 应报错提示媒体渲染，实际 err=%v", err)
	}
}

func TestFileOpsTool_Search(t *testing.T) {
	root := t.TempDir()
	tool := &FileOpsTool{Root: root}
	ctx := context.Background()

	// 准备：docs/report.txt（内容含关键词）、notes/2026.md（内容匹配）、子目录名匹配
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "report.txt"), []byte("季度报告，含内置工具测试内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes", "2026.md"), []byte("工作计划与内置工具测试内容"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 按文件名匹配
	out, err := tool.Execute(ctx, fileOpsSearchArgsJSON(".", "report", false, 0))
	if err != nil {
		t.Fatalf("search 失败: %v", err)
	}
	if !strings.Contains(out, filepath.ToSlash(filepath.Join("docs", "report.txt"))) || !strings.Contains(out, "文件名匹配") {
		t.Fatalf("文件名搜索应命中 report.txt: %s", out)
	}

	// 按内容匹配（文件名不命中、内容命中）
	out, err = tool.Execute(ctx, fileOpsSearchArgsJSON(".", "工作计划", false, 0))
	if err != nil {
		t.Fatalf("search 内容匹配失败: %v", err)
	}
	if !strings.Contains(out, filepath.ToSlash(filepath.Join("notes", "2026.md"))) || !strings.Contains(out, "内容匹配") {
		t.Fatalf("内容搜索应命中 notes/2026.md: %s", out)
	}

	// name_only=true 时不再匹配内容
	out, err = tool.Execute(ctx, fileOpsSearchArgsJSON(".", "工作计划", true, 0))
	if err != nil {
		t.Fatalf("search name_only 失败: %v", err)
	}
	if strings.Contains(out, "2026.md") {
		t.Fatalf("name_only 不应命中内容匹配: %s", out)
	}

	// 无命中
	out, err = tool.Execute(ctx, fileOpsSearchArgsJSON(".", "不存在的关键词xyz", false, 0))
	if err != nil {
		t.Fatalf("search 无命中失败: %v", err)
	}
	if !strings.Contains(out, "未找到") {
		t.Fatalf("无命中应提示未找到: %s", out)
	}

	// 缺少 query 参数 → 报错
	if _, err := tool.Execute(ctx, fileOpsArgsJSON("search", ".", "", false)); err == nil ||
		!strings.Contains(err.Error(), "query") {
		t.Errorf("search 缺 query 应报错，实际 err=%v", err)
	}
}

func TestFileOpsTool_Tree(t *testing.T) {
	root := t.TempDir()
	tool := &FileOpsTool{Root: root}
	ctx := context.Background()

	// 准备嵌套结构：docs/2026/q1.md、docs/report.md、scripts/run.py
	if err := os.MkdirAll(filepath.Join(root, "docs", "2026"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "2026", "q1.md"), []byte("第一季度总结"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "report.md"), []byte("季度报告"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "run.py"), []byte("print(1)"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 完整树：缩进表示层级，能直接看出嵌套关系（而非"某层有哪些文件"）
	out, err := tool.Execute(ctx, fileOpsTreeArgsJSON(".", 0))
	if err != nil {
		t.Fatalf("tree 失败: %v", err)
	}
	for _, want := range []string{
		"- docs/",
		"- scripts/",
		"  - 2026/",
		"  - report.md（",
		"  - run.py（",
		"    - q1.md（",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("tree 输出缺少 %q：\n%s", want, out)
		}
	}
	// 只显示末段名称 + 缩进，不应出现完整相对路径
	if strings.Contains(out, filepath.Join("docs", "2026")) {
		t.Fatalf("tree 输出不应包含完整相对路径：\n%s", out)
	}

	// depth=1：仅直接子项，不深入 docs/2026
	out, err = tool.Execute(ctx, fileOpsTreeArgsJSON(".", 1))
	if err != nil {
		t.Fatalf("tree depth=1 失败: %v", err)
	}
	if !strings.Contains(out, "- docs/") || strings.Contains(out, "q1.md") {
		t.Fatalf("tree depth=1 应只显示直接子项：\n%s", out)
	}

	// 对文件路径 tree → 报错
	if _, err := tool.Execute(ctx, fileOpsTreeArgsJSON("docs/report.md", 0)); err == nil ||
		!strings.Contains(err.Error(), "不是目录") {
		t.Errorf("对文件 tree 应报错，实际 err=%v", err)
	}
}

func TestFileOpsTool_TreeTruncated(t *testing.T) {
	root := t.TempDir()
	tool := &FileOpsTool{Root: root}
	ctx := context.Background()

	// 创建 maxTreeEntries+1 个文件，触发截断提示
	for i := 0; i < maxTreeEntries+1; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := tool.Execute(ctx, fileOpsTreeArgsJSON(".", 0))
	if err != nil {
		t.Fatalf("tree 截断测试失败: %v", err)
	}
	if !strings.Contains(out, "已截断") {
		t.Fatalf("tree 条目超上限应提示截断：\n%s", out)
	}
}

// TestFileOpsTool_UserIsolation 验证用户隔离（阶段2）：
//   - 带 user_id 的调用落到 <root>/users/<uid> 工作区（llm.WithHeader 注入，
//     与 agentsvc 完全相同的方式）；
//   - 展示路径带 users/<uid>/ 前缀（模型可直接拼 /files URL）；
//   - 不同用户互不可见对方工作区。
func TestFileOpsTool_UserIsolation(t *testing.T) {
	root := t.TempDir()
	tool := &FileOpsTool{Root: root}
	ctxU1 := llm.WithHeader(context.Background(), userIDHeader, "1")
	ctxU2 := llm.WithHeader(context.Background(), userIDHeader, "2")

	// U1 写入文件：应落到 users/1/ 下，且展示路径带前缀（斜杠分隔）。
	u1Prefix := filepath.ToSlash(filepath.Join("users", "1"))
	out, err := tool.Execute(ctxU1, fileOpsArgsJSON("write", "notes.md", "u1-secret", false))
	if err != nil {
		t.Fatalf("U1 write: %v", err)
	}
	if !strings.Contains(out, u1Prefix) {
		t.Fatalf("U1 write 展示路径应含 users/1/ 前缀，实际：%q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "users", "1", "notes.md")); err != nil {
		t.Fatalf("U1 文件应落在 <root>/users/1/ 下：%v", err)
	}

	// 未带 user_id（无上下文）：应落全局根，隔离模式不生效。
	out, err = tool.Execute(context.Background(), fileOpsArgsJSON("write", "global.txt", "g", false))
	if err != nil {
		t.Fatalf("global write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "global.txt")); err != nil {
		t.Fatalf("无 user_id 时写入应落全局根：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "users", "0", "global.txt")); err == nil {
		t.Fatal("无 user_id 时不应落到 users/0/")
	}

	// U1 读取自身工作区文件。
	out, err = tool.Execute(ctxU1, fileOpsArgsJSON("read", "notes.md", "", false))
	if err != nil {
		t.Fatalf("U1 read: %v", err)
	}
	if !strings.Contains(out, "u1-secret") {
		t.Fatalf("U1 应能读到自身文件，实际：%q", out)
	}

	// U2 尝试读取 U1 的文件：隔离后相对路径指向不存在的 users/2/notes.md。
	_, err = tool.Execute(ctxU2, fileOpsArgsJSON("read", "notes.md", "", false))
	if err == nil {
		t.Fatalf("U2 读 U1 文件应失败（隔离缺失）")
	}
	if !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("U2 错误信息应说明文件不存在，实际：%v", err)
	}

	// U1 list：条目路径应带 users/1/ 前缀。
	out, err = tool.Execute(ctxU1, fileOpsArgsJSON("list", ".", "", false))
	if err != nil {
		t.Fatalf("U1 list: %v", err)
	}
	if !strings.Contains(out, u1Prefix) {
		t.Fatalf("U1 list 条目应含 users/1/ 前缀：\n%s", out)
	}
}

// TestFileOpsTool_UserIsolation_PrefixedPath 验证"已带当前用户前缀"的路径解析：
//   - users/<uid>/chat-files/… 直接相对全局根解析（不重复拼接 users/<uid>/users/<uid>/…）；
//   - 其它用户的 users/<xxx>/… 前缀不享受该逻辑，跨用户隔离仍然生效。
//     这是聊天文档注入消息改用全局相对路径（含 users/<uid>/ 前缀）后，file_ops
//     必须配套支持的行为——否则模型会重蹈 users/62/users/62/… 读取失败的覆辙。
func TestFileOpsTool_UserIsolation_PrefixedPath(t *testing.T) {
	root := t.TempDir()
	tool := &FileOpsTool{Root: root}
	ctxU1 := llm.WithHeader(context.Background(), userIDHeader, "1")
	ctxU2 := llm.WithHeader(context.Background(), userIDHeader, "2")

	// U1 以用户相对路径写入（file_ops 隔离根 users/1 下），落盘 chat-files/9/notes.md。
	if _, err := tool.Execute(ctxU1, fileOpsArgsJSON("write", "chat-files/9/notes.md", "u1-note", false)); err != nil {
		t.Fatalf("U1 write(相对): %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "users", "1", "chat-files", "9", "notes.md")); err != nil {
		t.Fatalf("U1 文件应落在 <root>/users/1/chat-files/9/：%v", err)
	}

	// U1 用全局相对路径（含 users/1/ 前缀）读取同一文件：应直接相对全局根解析。
	prefixed := filepath.ToSlash(filepath.Join("users", "1", "chat-files", "9", "notes.md"))
	out, err := tool.Execute(ctxU1, fileOpsArgsJSON("read", prefixed, "", false))
	if err != nil {
		t.Fatalf("U1 read(带前缀) 不应重复拼接而失败：%v", err)
	}
	if !strings.Contains(out, "u1-note") {
		t.Fatalf("U1 read(带前缀) 内容不符：%q", out)
	}

	// U1 list（带前缀目录）：输出条目保持 users/1/ 前缀（模型可直接拼 /files URL）。
	prefixedDir := filepath.ToSlash(filepath.Join("users", "1", "chat-files", "9"))
	out, err = tool.Execute(ctxU1, fileOpsArgsJSON("list", prefixedDir, "", false))
	if err != nil {
		t.Fatalf("U1 list(带前缀): %v", err)
	}
	if !strings.Contains(out, "users/1/chat-files/9/notes.md") {
		t.Fatalf("U1 list 条目应含 users/1/ 前缀：\n%s", out)
	}

	// U2 用 users/1/ 前缀读 U1 文件：不属于当前用户前缀 → 仍隔离（找不到文件）。
	_, err = tool.Execute(ctxU2, fileOpsArgsJSON("read", prefixed, "", false))
	if err == nil {
		t.Fatalf("U2 读 U1 带前缀路径应失败（跨用户隔离）")
	}
	if !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("U2 错误信息应说明文件不存在，实际：%v", err)
	}
}

// TestFileOpsTool_SkillsNamespace 验证 @skills/ 虚拟命名空间：
//   - read/list/stat/search 只读操作可访问技能根目录；
//   - write 被拒绝（技能由管理端维护）；
//   - 展示路径带 @skills/ 前缀，可回填给 file_ops。
func TestFileOpsTool_SkillsNamespace(t *testing.T) {
	workRoot := t.TempDir()
	skillsRoot := filepath.Join(workRoot, "skills")
	skillDir := filepath.Join(skillsRoot, "my-script")
	if err := os.MkdirAll(filepath.Join(skillDir, "ref"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("guide"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "ref", "doc.md"), []byte("参考文档"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 用户隔离模式 + SkillsRoot 注入（模拟真实运行链路）。
	tool := &FileOpsTool{Root: workRoot, SkillsRoot: skillsRoot}
	ctx := llm.WithHeader(context.Background(), userIDHeader, "7")

	// read：能读到技能资源，且清单路径可回填。
	out, err := tool.Execute(ctx, fileOpsArgsJSON("read", "@skills/my-script/ref/doc.md", "", false))
	if err != nil {
		t.Fatalf("@skills read 失败: %v", err)
	}
	if !strings.Contains(out, "参考文档") {
		t.Fatalf("@skills read 输出异常: %s", out)
	}

	// list（递归）：条目路径带 @skills/ 前缀，子目录内文件可见。
	out, err = tool.Execute(ctx, fileOpsArgsJSON("list", "@skills/my-script", "", true))
	if err != nil {
		t.Fatalf("@skills list 失败: %v", err)
	}
	if !strings.Contains(out, "- [文件] @skills/my-script/ref/doc.md") {
		t.Fatalf("@skills list 条目应带 @skills/ 前缀：\n%s", out)
	}

	// stat：类型信息正常。
	out, err = tool.Execute(ctx, fileOpsArgsJSON("stat", "@skills/my-script/ref/doc.md", "", false))
	if err != nil {
		t.Fatalf("@skills stat 失败: %v", err)
	}
	if !strings.Contains(out, "类型：文件") {
		t.Fatalf("@skills stat 输出异常: %s", out)
	}

	// search：只读搜索可用。
	out, err = tool.Execute(ctx, fileOpsSearchArgsJSON("@skills/my-script", "参考", false, 0))
	if err != nil {
		t.Fatalf("@skills search 失败: %v", err)
	}
	if !strings.Contains(out, "doc.md") || !strings.Contains(out, "内容匹配") {
		t.Fatalf("@skills search 应命中内容: %s", out)
	}

	// write：被拒绝。
	if _, err := tool.Execute(ctx, fileOpsArgsJSON("write", "@skills/my-script/ref/new.md", "x", false)); err == nil ||
		!strings.Contains(err.Error(), "只读") {
		t.Fatalf("@skills write 应被拒绝为只读，实际 err=%v", err)
	}

	// 越界：@skills/../../ 应被拒绝。
	if _, err := tool.Execute(ctx, fileOpsArgsJSON("read", "@skills/../../secret.txt", "", false)); err == nil {
		t.Fatalf("@skills 越界路径应被拒绝")
	}

	// 未注入 SkillsRoot：回退默认技能根（<cwd>/skills），该文件不存在 → 报"文件不存在"
	//（证明 @skills 命名空间在无显式配置时仍按默认技能根解析，不落到用户工作区）。
	noCfg := &FileOpsTool{Root: workRoot}
	if _, err := noCfg.Execute(ctx, fileOpsArgsJSON("read", "@skills/my-script/ref/doc.md", "", false)); err == nil ||
		!strings.Contains(err.Error(), "不存在") {
		t.Fatalf("未配置 SkillsRoot 时应按默认技能根解析并报不存在，实际 err=%v", err)
	}
}
