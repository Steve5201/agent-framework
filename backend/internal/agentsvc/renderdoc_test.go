// renderdoc_test.go —— P4-D 文档生成服务层单测。
//
// 覆盖：DocumentSpec 校验、文档意图识别、JSON 提取、文件名校验、工具执行
// （含沙盒联动与产物落盘）、编排自动产出。使用 fakeRepo + MockProvider +
// 假沙盒 HTTP 服务，不触真实 DB / 沙盒 / 大模型。
package agentsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Steve5201/agent-backend/internal/rag/sandboxclient"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
	"go.uber.org/zap"
)

// fakeSandbox 假沙盒服务：先响应 ensureWorkspace（code:"true"），再按 profile
// 把产物写到 args[1] 指定路径（模拟渲染脚本产出），返回 exit_code 0。
func fakeSandbox(t *testing.T, expectedProfile string, out []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code    string   `json:"code"`
			Profile string   `json:"profile"`
			Args    []string `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		resp := map[string]any{"exit_code": 0, "duration_ms": 2}
		if body.Code == "true" {
			// ensureWorkspace：直接成功。
		} else if body.Profile == expectedProfile && len(body.Args) == 2 {
			if err := os.WriteFile(body.Args[1], out, 0o644); err != nil {
				resp = map[string]any{"exit_code": 1, "error": err.Error()}
			}
		} else {
			resp = map[string]any{"exit_code": 1, "error": "unexpected profile"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// newRenderTestService 构造带沙盒与临时工作区的测试服务。
func newRenderTestService(t *testing.T, sandboxURL string) (*Service, error) {
	t.Helper()
	svc, err := newTestServiceWithWork(t.TempDir())
	if err != nil {
		return nil, err
	}
	if sandboxURL != "" {
		svc.chatSandbox = sandboxclient.New(sandboxURL, svc.workRoot, 1, svc.log)
	}
	return svc, nil
}

// TestDocumentSpec_Validate DocumentSpec 校验矩阵。
func TestDocumentSpec_Validate(t *testing.T) {
	lims := defaultDocLimits()
	valid := DocumentSpec{
		Format: "docx", Title: "教案", Subtitle: "副标题",
		Sections: []DocSection{{Heading: "一、导入", Body: "正文\n- 要点1\n# 小节"}},
		Footer:   "示例大学",
	}
	if err := valid.validate(lims); err != nil {
		t.Fatalf("合法 spec 应通过校验: %v", err)
	}
	valid.Format = "pptx"
	if err := valid.validate(lims); err != nil {
		t.Fatalf("pptx 合法 spec 应通过校验: %v", err)
	}

	// P4-J 阶段2：六种富文本块齐备的合法 spec。
	rich := DocumentSpec{
		Format: "docx", Title: "富文本教案",
		Sections: []DocSection{{Heading: "一、概念", Blocks: []DocBlock{
			{Type: "paragraph", Text: "第一段"},
			{Type: "list", Items: []string{"要点A", "要点B"}},
			{Type: "table", Headers: []string{"列1", "列2"}, Rows: [][]string{{"a", "b"}, {"c", "d"}}},
			{Type: "image", Src: "rag-media/doc1/a.png", Caption: "架构图", Width: 480},
			{Type: "image", Src: "svg://<svg><circle r='5'/></svg>"},
			{Type: "formula", Text: `\frac{a}{b}`},
			{Type: "code", Text: "fmt.Println(1)", Lang: "go"},
		}}},
	}
	if err := rich.validate(lims); err != nil {
		t.Fatalf("六种富文本块合法 spec 应通过校验: %v", err)
	}
	// 绝对形态的知识库图片路径也合法。
	rich.Sections[0].Blocks[3].Src = "/work/rag-media/doc1/a.png"
	if err := rich.validate(lims); err != nil {
		t.Fatalf("绝对路径知识库图片应通过校验: %v", err)
	}

	cases := []struct {
		name string
		spec DocumentSpec
	}{
		{"空格式", DocumentSpec{Title: "t", Sections: []DocSection{{Heading: "h"}}}},
		{"非法格式", DocumentSpec{Format: "xls", Title: "t", Sections: []DocSection{{Heading: "h"}}}},
		{"空标题", DocumentSpec{Format: "docx", Sections: []DocSection{{Heading: "h"}}}},
		{"空章节", DocumentSpec{Format: "docx", Title: "t"}},
		{"空章节标题", DocumentSpec{Format: "docx", Title: "t", Sections: []DocSection{{Body: "b"}}}},
		{"章节过多", DocumentSpec{Format: "docx", Title: "t", Sections: make([]DocSection, 51)}},
		{"章节正文过长", DocumentSpec{Format: "docx", Title: "t", Sections: []DocSection{{Heading: "h", Body: strings.Repeat("长", 8001)}}}},
		{"非法块类型", DocumentSpec{Format: "docx", Title: "t", Sections: []DocSection{{Heading: "h", Blocks: []DocBlock{{Type: "video", Text: "x"}}}}}},
		{"图片 src 非法", DocumentSpec{Format: "docx", Title: "t", Sections: []DocSection{{Heading: "h", Blocks: []DocBlock{{Type: "image", Src: "http://x/a.png"}}}}}},
		{"图片 src 为空", DocumentSpec{Format: "docx", Title: "t", Sections: []DocSection{{Heading: "h", Blocks: []DocBlock{{Type: "image", Src: ""}}}}}},
		{"表格列数不一致", DocumentSpec{Format: "docx", Title: "t", Sections: []DocSection{{Heading: "h", Blocks: []DocBlock{{Type: "table", Headers: []string{"a", "b"}, Rows: [][]string{{"1"}}}}}}}},
		{"blocks 与 body 均空", DocumentSpec{Format: "docx", Title: "t", Sections: []DocSection{{Heading: "h"}}}},
		{"blocks 超上限", DocumentSpec{Format: "docx", Title: "t", Sections: []DocSection{{Heading: "h", Blocks: make([]DocBlock, 101)}}}},
		{"段落过长", DocumentSpec{Format: "docx", Title: "t", Sections: []DocSection{{Heading: "h", Blocks: []DocBlock{{Type: "paragraph", Text: strings.Repeat("长", 4001)}}}}}},
		{"列表项为空", DocumentSpec{Format: "docx", Title: "t", Sections: []DocSection{{Heading: "h", Blocks: []DocBlock{{Type: "list", Items: nil}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.spec.validate(lims); err == nil {
				t.Fatalf("%s 应校验失败", tc.name)
			}
		})
	}
}

// TestDocumentIntent 文档意图识别：明确 token / 泛化词+动词 / 误判防护。
func TestDocumentIntent(t *testing.T) {
	cases := []struct {
		goal   string
		format string
		ok     bool
	}{
		{"请帮我生成一份 ppt", "pptx", true},
		{"做一份课件吧", "pptx", true},
		{"生成 PPT 演示文稿", "pptx", true},
		{"帮我写一份 word 文档", "docx", true},
		{"生成 docx 文件", "docx", true},
		{"帮我生成一份教学文档", "docx", true},
		{"把内容整理成文档", "docx", true},
		{"总结一下这份文档内容", "", false}, // 泛化词无动词 → 不触发
		{"文档里有几个知识点？", "", false}, // 提问场景 → 不触发
		{"今天的天气怎么样", "", false},   // 无关目标 → 不触发
	}
	for _, tc := range cases {
		got, ok := documentIntent(tc.goal)
		if ok != tc.ok || (ok && got != tc.format) {
			t.Errorf("documentIntent(%q) = (%q, %v), want (%q, %v)",
				tc.goal, got, ok, tc.format, tc.ok)
		}
	}
}

// TestExtractJSONObject 从模型输出提取 JSON 对象：围栏 / 前后缀 / 字符串内花括号。
func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`{"a":1}`, `{"a":1}`},
		{"```json\n{\"a\":1}\n```", `{"a":1}`},
		{"好的，这是结果：{\"a\":{\"b\":\"}\"}} 完毕", `{"a":{"b":"}"}}`},
		{"无对象", ""},
	}
	for _, tc := range cases {
		if got := extractJSONObject(tc.in); got != tc.want {
			t.Errorf("extractJSONObject(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSanitizeDocFileName 文件名清洗：中文保留、非法字符替换、超长截断、空标题兜底。
func TestSanitizeDocFileName(t *testing.T) {
	cases := []struct {
		title  string
		format string
		want   string
	}{
		{"高等数学第一章", "docx", "高等数学第一章.docx"},
		{"a/b\\c:d*e", "pptx", "a_b_c_d_e.pptx"},
		{strings.Repeat("标题", 30), "docx", strings.Repeat("标题", 20) + ".docx"},
		{"   ", "docx", "document.docx"},
	}
	for _, tc := range cases {
		if got := sanitizeDocFileName(tc.title, tc.format); got != tc.want {
			t.Errorf("sanitizeDocFileName(%q, %q) = %q, want %q", tc.title, tc.format, got, tc.want)
		}
	}
}

// TestRenderDocumentTool_Execute 工具执行全链路：参数校验 / 沙盒联动 / 产物落盘。
func TestRenderDocumentTool_Execute(t *testing.T) {
	out := []byte("fake docx binary")
	srv := fakeSandbox(t, "render_docx", out)
	defer srv.Close()

	svc, err := newRenderTestService(t, srv.URL)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	args, _ := json.Marshal(DocumentSpec{
		Format: "docx", Title: "教学教案", Subtitle: "第一讲",
		Sections: []DocSection{{Heading: "一、导入", Body: "正文段落\n- 要点一"}},
	})
	ctx := llm.WithHeader(context.Background(), "X-User-Id", "1")

	res, err := renderDocumentTool{svc: svc}.Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute 应成功: %v", err)
	}
	if !strings.Contains(res, "```doc") || !strings.Contains(res, "users/1/chat-docs/") {
		t.Errorf("结果应包含下载代码块与工作区路径，实际: %s", res)
	}
	// 产物文件应真实落盘（相对路径 → 工作区绝对路径可解析）。
	rel := strings.TrimSpace(strings.Split(strings.Split(res, "```doc")[1], "```")[0])
	full := filepath.Join(svc.effectiveWorkRoot(), filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("产物未落盘: %v", err)
	}
	if info.Size() != int64(len(out)) {
		t.Errorf("产物大小 = %d, want %d", info.Size(), len(out))
	}

	t.Run("非法格式", func(t *testing.T) {
		bad, _ := json.Marshal(DocumentSpec{Format: "xls", Title: "t", Sections: []DocSection{{Heading: "h"}}})
		if _, err := (renderDocumentTool{svc: svc}).Execute(ctx, bad); err == nil {
			t.Fatal("非法格式应报错")
		}
	})
	t.Run("缺少用户上下文", func(t *testing.T) {
		if _, err := (renderDocumentTool{svc: svc}).Execute(context.Background(), args); err == nil {
			t.Fatal("缺少 X-User-Id 应报错")
		}
	})
	t.Run("沙盒未配置", func(t *testing.T) {
		nosvc, err := newRenderTestService(t, "")
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		if _, err := (renderDocumentTool{svc: nosvc}).Execute(ctx, args); err == nil {
			t.Fatal("沙盒未配置应报错")
		}
	})
}

// TestRenderDocumentTool_Registry 经注册表全路径执行（含权限批准）。
func TestRenderDocumentTool_Registry(t *testing.T) {
	srv := fakeSandbox(t, "render_pptx", []byte("fake pptx"))
	defer srv.Close()
	svc, err := newRenderTestService(t, srv.URL)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	reg, err := DefaultToolSet()
	if err != nil {
		t.Fatalf("DefaultToolSet: %v", err)
	}
	if err := svc.ensureRenderToolRegistered(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	args, _ := json.Marshal(DocumentSpec{
		Format: "pptx", Title: "课件",
		Sections: []DocSection{{Heading: "第一节", Body: "内容"}},
	})
	ctx := llm.WithHeader(context.Background(), "X-User-Id", "1")
	res, err := reg.Execute(ctx, schema.ToolCall{Name: "render_document", Arguments: args}, true)
	if err != nil {
		t.Fatalf("registry Execute 应成功: %v", err)
	}
	if res.IsError {
		t.Fatalf("结果不应为错误: %s", res.Content)
	}
	// L2 工具未批准时应拒绝（Permission 语义）。
	if _, err := reg.Execute(ctx, schema.ToolCall{Name: "render_document", Arguments: args}, false); err == nil {
		t.Fatal("L2 工具未批准应被拒绝")
	}
}

// TestAutoRenderDocument 编排自动产出：命中意图 → 提炼 spec → 渲染 → 返回下载区块。
func TestAutoRenderDocument(t *testing.T) {
	srv := fakeSandbox(t, "render_docx", []byte("doc bytes"))
	defer srv.Close()

	// 模型两次回答：synthesizeDocumentSpec 一次（返回合法 JSON spec）。
	provider := &llm.MockProvider{Content: `{"title":"课程总结","subtitle":"","sections":[{"heading":"要点","body":"- 要点一"}],"footer":""}`}
	repo := newFakeRepo()
	svc, err := NewService(Config{
		Repo: repo, Provider: provider, Registry: mustRegistry(t),
		Log: zap.NewNop(), Model: "test-model", MaxRounds: 8, MaxMessages: 20,
		WorkRoot: t.TempDir(), ChatSandboxURL: srv.URL, ChatSandboxUserID: 1,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	appendix := svc.autoRenderDocument(ctx, 1, "帮我写一份课程总结文档", "汇总：课程要点……")
	if !strings.Contains(appendix, "```doc") {
		t.Fatalf("应返回下载区块，实际: %q", appendix)
	}
	if !strings.Contains(appendix, "users/1/chat-docs/") {
		t.Fatalf("下载区块应含工作区路径: %q", appendix)
	}
	rel := strings.TrimSpace(strings.Split(strings.Split(appendix, "```doc")[1], "```")[0])
	if _, err := os.Stat(filepath.Join(svc.effectiveWorkRoot(), filepath.FromSlash(rel))); err != nil {
		t.Fatalf("自动产出文件未落盘: %v", err)
	}

	t.Run("未命中意图不产出", func(t *testing.T) {
		if got := svc.autoRenderDocument(ctx, 1, "今天的天气", "内容"); got != "" {
			t.Fatalf("无关目标不应产出文档: %q", got)
		}
	})
	t.Run("模型返回非法 spec 不产出", func(t *testing.T) {
		bad := &llm.MockProvider{Content: `{"title":""}`}
		bsvc, _ := NewService(Config{
			Repo: newFakeRepo(), Provider: bad, Registry: mustRegistry(t),
			Log: zap.NewNop(), Model: "test-model", MaxRounds: 8, MaxMessages: 20,
			WorkRoot: t.TempDir(), ChatSandboxURL: srv.URL, ChatSandboxUserID: 1,
		})
		if got := bsvc.autoRenderDocument(ctx, 1, "帮我生成一份文档", "素材"); got != "" {
			t.Fatalf("非法 spec 不应产出: %q", got)
		}
	})
}

// mustRegistry 构造可用注册表（测试便捷函数）。
func mustRegistry(t *testing.T) *tool.Registry {
	t.Helper()
	reg, err := DefaultToolSet()
	if err != nil {
		t.Fatalf("DefaultToolSet: %v", err)
	}
	return reg
}

// TestResolveDocMedia 知识库图片资产解析：存在图片改绝对路径、缺失图片宽容移除、
// svg:// 与其它块类型不受影响（P4-J 阶段2）。
func TestResolveDocMedia(t *testing.T) {
	work := t.TempDir()
	mediaDir := filepath.Join(work, "rag-media", "doc1")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mediaDir, "a.png"), []byte("png"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc, err := newTestServiceWithWork(work)
	if err != nil {
		t.Fatalf("newTestServiceWithWork: %v", err)
	}
	spec := DocumentSpec{Format: "docx", Title: "t", Sections: []DocSection{{Heading: "h", Blocks: []DocBlock{
		{Type: "image", Src: "rag-media/doc1/a.png"},
		{Type: "image", Src: "rag-media/doc1/missing.png"},
		{Type: "paragraph", Text: "普通段落不受影响"},
		{Type: "image", Src: "svg://<svg><circle r='5'/></svg>"},
	}}}}
	removed := svc.resolveDocMedia(work, &spec)
	if removed != 1 {
		t.Fatalf("缺失图片应移除 1 块，实际 %d", removed)
	}
	got := spec.Sections[0].Blocks
	if len(got) != 3 {
		t.Fatalf("移除后应剩 3 块，实际 %d", len(got))
	}
	wantAbs := filepath.ToSlash(filepath.Join(work, "rag-media", "doc1", "a.png"))
	if got[0].Type != "image" || got[0].Src != wantAbs {
		t.Errorf("存在图片应改写为绝对路径 %q，实际 %q", wantAbs, got[0].Src)
	}
	if got[1].Type != "paragraph" || got[2].Type != "image" || !strings.HasPrefix(got[2].Src, "svg://") {
		t.Errorf("段落与 svg 块应原样保留: %+v", got)
	}
}

// TestRenderDocumentSpec_ResolveMediaInSpec 渲染链路验证：spec.json 落盘时
// rag-media 图片 src 已解析为绝对路径、缺失图片块被移除、SkippedMedia 上报。
func TestRenderDocumentSpec_ResolveMediaInSpec(t *testing.T) {
	work := t.TempDir()
	mediaDir := filepath.Join(work, "rag-media", "doc1")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mediaDir, "a.png"), []byte("png"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	srv := fakeSandbox(t, "render_docx", []byte("fake docx"))
	defer srv.Close()
	svc, err := newTestServiceWithWork(work)
	if err != nil {
		t.Fatalf("newTestServiceWithWork: %v", err)
	}
	svc.chatSandbox = sandboxclient.New(srv.URL, work, 1, svc.log)

	spec := DocumentSpec{Format: "docx", Title: "带图教案", Sections: []DocSection{{Heading: "一、图", Blocks: []DocBlock{
		{Type: "image", Src: "rag-media/doc1/a.png", Caption: "架构图"},
		{Type: "image", Src: "rag-media/doc1/missing.png"},
	}}}}
	res, err := svc.renderDocumentSpec(context.Background(), 1, spec)
	if err != nil {
		t.Fatalf("renderDocumentSpec 应成功: %v", err)
	}
	if res.SkippedMedia != 1 {
		t.Fatalf("SkippedMedia 应为 1，实际 %d", res.SkippedMedia)
	}

	// 读取落盘的 spec.json，验证 src 已被解析为绝对路径。
	docDir := filepath.Dir(filepath.Join(work, filepath.FromSlash(res.RelPath)))
	raw, err := os.ReadFile(filepath.Join(docDir, "spec.json"))
	if err != nil {
		t.Fatalf("读取 spec.json: %v", err)
	}
	var saved DocumentSpec
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("解析 spec.json: %v", err)
	}
	savedBlocks := saved.Sections[0].Blocks
	if len(savedBlocks) != 1 {
		t.Fatalf("落盘 spec 应只剩 1 个图片块，实际 %d", len(savedBlocks))
	}
	wantAbs := filepath.ToSlash(filepath.Join(work, "rag-media", "doc1", "a.png"))
	if savedBlocks[0].Src != wantAbs {
		t.Errorf("落盘 src 应为绝对路径 %q，实际 %q", wantAbs, savedBlocks[0].Src)
	}
}
