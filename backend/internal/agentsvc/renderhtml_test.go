// renderhtml_test.go —— HTML 中间层文档生成服务层单测。
//
// 覆盖：HTMLDocRequest 校验、sanitizeHTML 安全净化矩阵（script/iframe/on*/
// javascript:/data:text/html 移除、SVG/表格保留、meta refresh 拦截）、
// renderHTMLDocument 全链路（HTML 落盘 + 可选 PDF 沙盒联动与降级）、
// renderHTMLTool 工具执行。使用 fakeRepo + MockProvider + 假沙盒 HTTP 服务，
// 不触真实 DB / 沙盒 / 大模型。
package agentsvc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
)

// TestHTMLDocRequest_Validate HTMLDocRequest 校验矩阵。
func TestHTMLDocRequest_Validate(t *testing.T) {
	lims := defaultDocLimits()
	valid := HTMLDocRequest{Format: "html", Title: "高等数学课件", HTML: "<h1>第一节</h1>"}
	if err := valid.validate(lims); err != nil {
		t.Fatalf("合法请求应通过校验: %v", err)
	}
	valid.Format = "pdf"
	if err := valid.validate(lims); err != nil {
		t.Fatalf("pdf 合法请求应通过校验: %v", err)
	}
	// 引用模式：仅传 html_file 也合法（内容读取在 loadHTMLFile）。
	validRef := HTMLDocRequest{Format: "html", Title: "论文草稿", HTMLFile: "chat-docs/draft.html"}
	if err := validRef.validate(lims); err != nil {
		t.Fatalf("html_file 引用请求应通过校验: %v", err)
	}

	cases := []struct {
		name string
		req  HTMLDocRequest
	}{
		{"非法格式", HTMLDocRequest{Format: "docx", Title: "t", HTML: "<p>x</p>"}},
		{"空标题", HTMLDocRequest{Format: "html", HTML: "<p>x</p>"}},
		{"标题过长", HTMLDocRequest{Format: "html", Title: strings.Repeat("标", 61), HTML: "<p>x</p>"}},
		{"空 HTML", HTMLDocRequest{Format: "html", Title: "t"}},
		{"HTML 过大", HTMLDocRequest{Format: "html", Title: "t", HTML: "<p>" + strings.Repeat("x", htmlMaxHTMLSize) + "</p>"}},
		{"html 与 html_file 冲突", HTMLDocRequest{Format: "html", Title: "t", HTML: "<p>x</p>", HTMLFile: "chat-docs/draft.html"}},
		{"html_file 空标题", HTMLDocRequest{Format: "html", HTMLFile: "chat-docs/draft.html"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.req.validate(lims); err == nil {
				t.Fatalf("%s 应校验失败", tc.name)
			}
		})
	}
}

// TestSanitizeHTML sanitize 安全净化矩阵：危险节点/属性移除，文档结构保留。
func TestSanitizeHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string // 应保留的子串
		gone []string // 应消失的子串
	}{
		{
			name: "script 移除",
			in:   "<p>正文</p><script>alert(1)</script><script src='//x.js'/>",
			want: []string{"<p>", "正文"},
			gone: []string{"script", "alert(1)", "x.js"},
		},
		{
			name: "iframe/object/embed 移除",
			in:   "<iframe src='http://evil'></iframe><object data='x'></object><embed src='y'>",
			gone: []string{"iframe", "object", "embed", "evil"},
		},
		{
			name: "on 事件属性移除",
			in:   "<img src='a.png' onerror='alert(1)' onclick=\"x()\">",
			want: []string{`src="a.png"`},
			gone: []string{"onerror", "onclick", "alert"},
		},
		{
			name: "javascript: 协议移除",
			in:   "<a href='javascript:alert(1)'>链接</a><a href='data:text/html;base64,PHNjcmlwdD4='>恶意</a>",
			want: []string{"链接", "恶意"},
			gone: []string{"javascript:", "data:text/html"},
		},
		{
			name: "href 移除后锚点文本保留",
			in:   "<a href='javascript:void(0)'>安全文本</a>",
			want: []string{"安全文本"},
			gone: []string{"void(0)"},
		},
		{
			name: "普通 href 保留",
			in:   "<a href='#section1'>跳转</a>",
			want: []string{`href="#section1"`, "跳转"},
		},
		{
			name: "meta refresh 拦截",
			in:   "<head><meta charset='utf-8'><meta http-equiv='refresh' content='0;url=http://evil'></head>",
			want: []string{"charset"},
			gone: []string{"refresh", "evil"},
		},
		{
			name: "base/link 外联移除",
			in:   "<base href='http://evil'><link rel='stylesheet' href='http://evil/x.css'>",
			gone: []string{"base", "link", "evil"},
		},
		{
			name: "SVG 保留",
			in:   "<svg xmlns='http://www.w3.org/2000/svg'><circle r='5'/></svg>",
			want: []string{"<svg", "circle"},
		},
		{
			name: "表格与行内样式保留",
			in:   "<table><tr><td style='color:red'>数据</td></tr></table>",
			want: []string{"<table", "style=", "数据"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := sanitizeHTML(tc.in)
			if err != nil {
				t.Fatalf("sanitizeHTML: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("输出应包含 %q，实际: %s", w, out)
				}
			}
			for _, g := range tc.gone {
				if strings.Contains(out, g) {
					t.Errorf("输出不应包含 %q，实际: %s", g, out)
				}
			}
		})
	}
}

// TestSanitizeHTML_Malformed 畸形输入不应崩溃（x/net/html 宽容解析）。
func TestSanitizeHTML_Malformed(t *testing.T) {
	for _, in := range []string{"", "<div", "text only", "<script>"} {
		if _, err := sanitizeHTML(in); err != nil {
			t.Errorf("sanitizeHTML(%q) 不应报错: %v", in, err)
		}
	}
}

// TestRenderHTMLDocument_HTMLOnly 纯 HTML（format=html）不触沙盒，产物落盘。
func TestRenderHTMLDocument_HTMLOnly(t *testing.T) {
	svc, err := newRenderTestService(t, "") // 沙盒为空
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	res, err := svc.renderHTMLDocument(context.Background(), 1,
		HTMLDocRequest{Format: "html", Title: "网页讲义", HTML: "<h1>标题</h1><p>正文</p>"})
	if err != nil {
		t.Fatalf("renderHTMLDocument: %v", err)
	}
	if res.PdfRel != "" || res.PdfFailed {
		t.Errorf("html 模式不应触发 PDF，PdfRel=%q PdfFailed=%v", res.PdfRel, res.PdfFailed)
	}
	if !strings.HasSuffix(res.RelPath, ".html") {
		t.Fatalf("主产物应为 .html，实际 %q", res.RelPath)
	}
	full := filepath.Join(svc.effectiveWorkRoot(), filepath.FromSlash(res.RelPath))
	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("产物未落盘: %v", err)
	}
	if info.Size() <= 0 {
		t.Fatal("产物不应为空")
	}
	body, _ := os.ReadFile(full)
	if !strings.Contains(string(body), "网页讲义") && !strings.Contains(string(body), "正文") {
		t.Errorf("产物内容异常: %s", body)
	}
}

// TestRenderHTMLDocument_PDFDegrade 请求 PDF 但沙盒未配置 → 降级 HTML 不阻断。
func TestRenderHTMLDocument_PDFDegrade(t *testing.T) {
	svc, err := newRenderTestService(t, "") // 沙盒为空
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	res, err := svc.renderHTMLDocument(context.Background(), 1,
		HTMLDocRequest{Format: "pdf", Title: "带PDF文档", HTML: "<p>内容</p>"})
	if err != nil {
		t.Fatalf("沙盒缺失应降级而非报错: %v", err)
	}
	if !res.PdfFailed || res.PdfRel != "" {
		t.Errorf("应标记 PdfFailed 且无 PdfRel，实际 PdfFailed=%v PdfRel=%q", res.PdfFailed, res.PdfRel)
	}
	if !strings.HasSuffix(res.RelPath, ".html") {
		t.Errorf("主产物应为 html，实际 %q", res.RelPath)
	}
}

// TestRenderHTMLDocument_PDFSandboxFail 沙盒返回失败 → 降级 HTML 不阻断。
func TestRenderHTMLDocument_PDFSandboxFail(t *testing.T) {
	// fakeSandbox 期待 profile "render_pdf"；给不匹配 profile 的假沙盒 →
	// ExecProfileAs 失败 → 降级。
	srv := fakeSandbox(t, "render_docx", []byte("nope"))
	defer srv.Close()
	svc, err := newRenderTestService(t, srv.URL)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	res, err := svc.renderHTMLDocument(context.Background(), 1,
		HTMLDocRequest{Format: "pdf", Title: "降级测试", HTML: "<p>x</p>"})
	if err != nil {
		t.Fatalf("沙盒失败应降级而非报错: %v", err)
	}
	if !res.PdfFailed {
		t.Errorf("沙盒失败应标记 PdfFailed，实际 %v", res.PdfFailed)
	}
	if !strings.HasSuffix(res.RelPath, ".html") {
		t.Errorf("主产物应为 html，实际 %q", res.RelPath)
	}
}

// TestRenderHTMLDocument_PDFSuccess 沙盒正常渲染 PDF → 双产物落盘。
func TestRenderHTMLDocument_PDFSuccess(t *testing.T) {
	pdfOut := []byte("%PDF-1.4 fake")
	srv := fakeSandbox(t, "render_pdf", pdfOut)
	defer srv.Close()
	svc, err := newRenderTestService(t, srv.URL)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	res, err := svc.renderHTMLDocument(context.Background(), 1,
		HTMLDocRequest{Format: "pdf", Title: "正式文档", HTML: "<h1>第一章</h1>"})
	if err != nil {
		t.Fatalf("renderHTMLDocument: %v", err)
	}
	if res.PdfFailed || res.PdfRel == "" {
		t.Fatalf("沙盒成功应产出 PDF，PdfFailed=%v PdfRel=%q", res.PdfFailed, res.PdfRel)
	}
	if !strings.HasSuffix(res.PdfRel, ".pdf") {
		t.Errorf("PDF 产物扩展名错误: %q", res.PdfRel)
	}
	pdfFull := filepath.Join(svc.effectiveWorkRoot(), filepath.FromSlash(res.PdfRel))
	if info, err := os.Stat(pdfFull); err != nil || info.Size() != int64(len(pdfOut)) {
		t.Errorf("PDF 产物未正确落盘: %v", err)
	}
	// HTML 主产物同时存在。
	htmlFull := filepath.Join(svc.effectiveWorkRoot(), filepath.FromSlash(res.RelPath))
	if _, err := os.Stat(htmlFull); err != nil {
		t.Errorf("HTML 主产物未落盘: %v", err)
	}
}

// TestRenderHTMLTool_Execute 工具执行：参数解析 / 用户上下文 / 下载代码块。
func TestRenderHTMLTool_Execute(t *testing.T) {
	svc, err := newRenderTestService(t, "") // 纯 html 不触沙盒
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	args, _ := json.Marshal(HTMLDocRequest{
		Format: "html", Title: "学习笔记", HTML: "<h2>要点</h2><ul><li>a</li></ul>",
	})
	ctx := llm.WithHeader(context.Background(), "X-User-Id", "1")

	res, err := (renderHTMLTool{svc: svc}).Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute 应成功: %v", err)
	}
	if !strings.Contains(res, "```doc") || !strings.Contains(res, "users/1/chat-docs/") {
		t.Errorf("结果应包含下载代码块与工作区路径，实际: %s", res)
	}

	t.Run("非法格式", func(t *testing.T) {
		bad, _ := json.Marshal(HTMLDocRequest{Format: "xls", Title: "t", HTML: "<p>x</p>"})
		if _, err := (renderHTMLTool{svc: svc}).Execute(ctx, bad); err == nil {
			t.Fatal("非法格式应报错")
		}
	})
	t.Run("缺少用户上下文", func(t *testing.T) {
		if _, err := (renderHTMLTool{svc: svc}).Execute(context.Background(), args); err == nil {
			t.Fatal("缺少 X-User-Id 应报错")
		}
	})
	t.Run("参数解析失败", func(t *testing.T) {
		if _, err := (renderHTMLTool{svc: svc}).Execute(ctx, json.RawMessage(`{invalid`)); err == nil {
			t.Fatal("非法 JSON 应报错")
		}
	})
}

// TestRenderHTMLDocument_HTMLFile 引用模式：长文档先落盘，html_file 引用渲染。
func TestRenderHTMLDocument_HTMLFile(t *testing.T) {
	svc, err := newRenderTestService(t, "") // 沙盒为空
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// 模拟模型先用 file_ops write 落盘的长文档（>300KB 直传上限，绕开限制）。
	longHTML := "<html><body><h1>论文草稿</h1>" + strings.Repeat("<p>段落内容。</p>", 16000) + "</body></html>"
	if len(longHTML) <= htmlMaxHTMLSize {
		t.Fatalf("测试文档应超过直传上限: %d 字节", len(longHTML))
	}
	userRoot := filepath.Join(svc.effectiveWorkRoot(), "users", "1")
	if err := os.MkdirAll(filepath.Join(userRoot, "chat-docs"), 0o755); err != nil {
		t.Fatalf("准备用户工作区失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userRoot, "chat-docs", "draft.html"), []byte(longHTML), 0o644); err != nil {
		t.Fatalf("写参考文件失败: %v", err)
	}

	res, err := svc.renderHTMLDocument(context.Background(), 1,
		HTMLDocRequest{Format: "html", Title: "论文草稿", HTMLFile: "chat-docs/draft.html"})
	if err != nil {
		t.Fatalf("引用渲染失败: %v", err)
	}
	full := filepath.Join(svc.effectiveWorkRoot(), filepath.FromSlash(res.RelPath))
	body, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("读取产物失败: %v", err)
	}
	if len(body) < len(longHTML)/2 {
		t.Fatalf("产物内容过短，疑似截断: 源 %d 字节，产物 %d 字节", len(longHTML), len(body))
	}
	if !strings.Contains(string(body), "论文草稿") {
		t.Errorf("产物应包含落盘文档内容: %s", string(body[:200]))
	}
}

// TestRenderHTMLDocument_HTMLFile_Escape 引用模式路径逃逸：../ 与绝对路径必须拒绝。
func TestRenderHTMLDocument_HTMLFile_Escape(t *testing.T) {
	svc, err := newRenderTestService(t, "")
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// 在工作区外放一个敏感文件，验证无法被 ../ 引用。
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("写敏感文件失败: %v", err)
	}

	for _, p := range []string{"../secret.txt", "users/../secret.txt", outside} {
		_, err := svc.renderHTMLDocument(context.Background(), 1,
			HTMLDocRequest{Format: "html", Title: "逃逸测试", HTMLFile: p})
		if err == nil {
			t.Errorf("路径 %q 应被拒绝（逃逸防护）", p)
		}
	}

	// 文件不存在也应有明确错误。
	_, err = svc.renderHTMLDocument(context.Background(), 1,
		HTMLDocRequest{Format: "html", Title: "缺文件", HTMLFile: "chat-docs/nope.html"})
	if err == nil || !strings.Contains(err.Error(), "读取 html_file 失败") {
		t.Errorf("文件不存在应有明确错误: %v", err)
	}
}
