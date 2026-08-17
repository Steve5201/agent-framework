package agentsvc

// ---------------------------------------------------------------------------
// HTML 中间层文档生成（业界标准管线：LLM 产出单文件 HTML → 预览 + PDF）。
//
// 背景：render_document（DocumentSpec 直写 docx/pptx）受 python-docx 手排
// 限制，排版表现力有限。HTML 中间层让智能体直接产出单文件 HTML（自包含
// CSS/SVG/公式/图片），浏览器渲染即成品：
//
//	render_html 工具 → 安全净化 → 落盘 .html（主力产物，前端 iframe 预览 + 下载）
//	                    └─ format=pdf → 沙盒 render_pdf profile（Chromium headless）
//
// HTML 落盘不依赖沙盒（纯文本写入），沙盒未配置/未装 playwright 时 PDF 优雅
// 降级为 HTML，保证主链路稳定可用。
//
// 安全设计（LLM 产出的 HTML 是不可信输入，三层防线）：
//  1. sanitizeHTML 移除 script/iframe/object/embed/base/link + on* 属性 +
//     javascript:/data:text/html URL（x/net/html 解析，结构级净化）；
//  2. 前端预览 iframe 使用 sandbox 属性（禁脚本）；
//  3. 沙盒 render_pdf 渲染前再次剥除 script 并禁 JS + 禁网。
// ---------------------------------------------------------------------------

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/tools"
	"github.com/Steve5201/agent-backend/internal/tools/builtin"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
	"go.uber.org/zap"
)

// htmlMaxHTMLSize 单文件 HTML 内容上限（模型产物，防止超大请求压垮落盘/渲染）。
// 直接以 html 参数传入的内容受此上限约束；超过上限的文档应先用 file_ops 落盘，
// 再通过 html_file 引用（上限见 htmlMaxFileSize）。
const htmlMaxHTMLSize = 300 * 1024

// htmlMaxFileSize 引用模式（html_file）内容上限：2MB。
// 长文档（论文草稿等）先由模型用 file_ops 写入工作区，再传路径引用，
// 内容在工具侧从文件读取，绕开模型单次输出的 token 上限与 300KB 直传限制。
const htmlMaxFileSize = 2 << 20

// HTMLDocRequest render_html 工具入参。
type HTMLDocRequest struct {
	Format   string `json:"format"`    // html（默认）| pdf：pdf 时同时导出 PDF
	Title    string `json:"title"`     // 文档标题（文件名），必填
	HTML     string `json:"html"`      // 完整单文件 HTML 文档（≤300KB），与 html_file 二选一
	HTMLFile string `json:"html_file"` // 可选：长文档先用 file_ops 写入的路径（相对用户工作区），二选一
}

// validate 校验入参（早失败原则，与 render_document 一致）。
// lims 为零值时归一为内置默认（同 DocumentSpec.validate）。
func (r *HTMLDocRequest) validate(lims config.DocConfig) error {
	lims = normalizeDocLimits(lims)
	if r.Format != "" && r.Format != "html" && r.Format != "pdf" {
		return fmt.Errorf("format 必须为 html 或 pdf（实际 %q）", r.Format)
	}
	r.Title = strings.TrimSpace(r.Title)
	if r.Title == "" {
		return errors.New("title 不能为空")
	}
	if runeLen(r.Title) > lims.MaxTitleLen {
		return fmt.Errorf("title 过长（≤%d 字）", lims.MaxTitleLen)
	}
	r.HTML = strings.TrimSpace(r.HTML)
	r.HTMLFile = strings.TrimSpace(r.HTMLFile)
	hasHTML, hasFile := r.HTML != "", r.HTMLFile != ""
	switch {
	case hasHTML && hasFile:
		return errors.New("html 与 html_file 只能二选一")
	case !hasHTML && !hasFile:
		return errors.New("html 内容不能为空（或用 html_file 引用已落盘的文档文件）")
	}
	if hasHTML && len(r.HTML) > htmlMaxHTMLSize {
		return fmt.Errorf("html 内容过大（≤%d 字节；更长的文档请先用 file_ops 写入文件，再通过 html_file 引用）", htmlMaxHTMLSize)
	}
	return nil
}

// loadHTMLFile 引用模式：从用户工作区读取 html_file 指向的 HTML 内容。
// 路径经 tools.ResolveInRoot 校验（防 ../ 逃逸、符号链接越界），内容受
// htmlMaxFileSize 上限约束，读取成功后代替换到 HTML 字段走既有净化/落盘流程。
func (r *HTMLDocRequest) loadHTMLFile(workRoot string, userID int64) error {
	userRoot := filepath.Join(workRoot, "users", fmt.Sprint(userID))
	abs, err := tools.ResolveInRoot(userRoot, r.HTMLFile)
	if err != nil {
		return fmt.Errorf("render_html: html_file 路径非法: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("render_html: 读取 html_file 失败: %w", err)
	}
	if len(data) > htmlMaxFileSize {
		return fmt.Errorf("render_html: html_file 内容过大（≤%d 字节）", htmlMaxFileSize)
	}
	r.HTML = string(data)
	return nil
}

// sanitizeHTML 结构级安全净化：移除可执行/嵌入/外联节点与危险属性。
// 保留样式（style/style 属性）与 SVG/表格等文档所需结构。
func sanitizeHTML(input string) (string, error) {
	doc, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return "", err
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		// 自底向上遍历（先删除子树再处理本节点）。
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			walk(c)
			c = next
		}
		if n.Type != html.ElementNode {
			return
		}
		switch strings.ToLower(n.Data) {
		case "script", "iframe", "object", "embed", "base", "link":
			n.Parent.RemoveChild(n)
			return
		case "meta":
			// 仅拦截自动刷新（http-equiv=refresh）；charset/viewport 等保留。
			for _, a := range n.Attr {
				if strings.EqualFold(a.Key, "http-equiv") &&
					strings.EqualFold(strings.TrimSpace(a.Val), "refresh") {
					n.Parent.RemoveChild(n)
					return
				}
			}
			return
		}
		// 过滤危险属性：on* 事件处理器 + javascript:/data:text/html 协议。
		kept := n.Attr[:0]
		for _, a := range n.Attr {
			key := strings.ToLower(a.Key)
			if strings.HasPrefix(key, "on") {
				continue
			}
			val := strings.ToLower(strings.TrimSpace(a.Val))
			if (key == "href" || key == "src" || key == "xlink:href" || key == "action") &&
				(strings.HasPrefix(val, "javascript:") || strings.HasPrefix(val, "data:text/html")) {
				continue
			}
			kept = append(kept, a)
		}
		n.Attr = kept
	}
	walk(doc)
	var sb strings.Builder
	if err := html.Render(&sb, doc); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// htmlRenderResult 一次 HTML 文档渲染的产物信息。
type htmlRenderResult struct {
	RelPath   string // .html 主产物相对路径（users/<uid>/chat-docs/<fileID>/<file>.html）
	FileName  string
	Bytes     int64
	PdfRel    string // 可选 PDF 产物相对路径（format=pdf 且渲染成功）
	PdfBytes  int64
	PdfFailed bool // 请求了 PDF 但沙盒不可用/渲染失败（已降级为 HTML）
}

// renderHTMLDocument 执行一次 HTML 文档渲染：安全净化 → 落盘 .html → 可选
// 沙盒 PDF（render_pdf profile）。HTML 落盘不依赖沙盒；PDF 失败仅降级不阻断。
func (s *Service) renderHTMLDocument(ctx context.Context, userID int64, req HTMLDocRequest) (*htmlRenderResult, error) {
	if err := req.validate(s.docLimits); err != nil {
		return nil, fmt.Errorf("render_html: 参数非法: %w", err)
	}
	if req.Format == "" {
		req.Format = "html"
	}
	// 引用模式：html 为空时从用户工作区读取 html_file 指向的内容。
	if req.HTML == "" && req.HTMLFile != "" {
		if err := req.loadHTMLFile(s.effectiveWorkRoot(), userID); err != nil {
			return nil, err
		}
	}

	// 第一道防线：结构级净化（移除可执行脚本/外联/危险属性）。
	safe, err := sanitizeHTML(req.HTML)
	if err != nil {
		return nil, fmt.Errorf("render_html: HTML 解析失败: %w", err)
	}

	fileID := fmt.Sprintf("doc_%d_%s", time.Now().UnixMilli(), randSuffix(3))
	htmlName := sanitizeDocFileName(req.Title, "html")
	dir := filepath.Join(s.effectiveWorkRoot(), "users", fmt.Sprint(userID), "chat-docs", fileID)
	if err := ensureGroupWritableDir(dir); err != nil {
		return nil, fmt.Errorf("render_html: 创建工作区目录失败: %w", err)
	}

	htmlAbs := filepath.Join(dir, htmlName)
	if err := os.WriteFile(htmlAbs, []byte(safe), 0o644); err != nil {
		return nil, fmt.Errorf("render_html: 写入 html 失败: %w", err)
	}
	info, err := os.Stat(htmlAbs)
	if err != nil || info.Size() <= 0 {
		return nil, fmt.Errorf("render_html: html 产物异常: %w", err)
	}
	// 防御性放宽可读（与 render_document 一致）：agent（app 用户）经 /files 读取。
	_ = os.Chmod(htmlAbs, 0o644)

	rel := func(name string) string {
		return filepath.ToSlash(filepath.Join("users", fmt.Sprint(userID), "chat-docs", fileID, name))
	}
	res := &htmlRenderResult{RelPath: rel(htmlName), FileName: htmlName, Bytes: info.Size()}

	// 可选 PDF：委托沙盒 render_pdf profile；沙盒未配置/渲染失败 → 降级 HTML。
	if req.Format == "pdf" {
		if s.chatSandbox == nil {
			res.PdfFailed = true
			s.log.Warn("render_html: 请求 PDF 但沙盒渲染服务未配置（RAG_SANDBOX_URL 为空），降级为 HTML")
		} else {
			pdfName := sanitizeDocFileName(req.Title, "pdf")
			pdfAbs := filepath.Join(dir, pdfName)
			// 第二/三道防线在 render_pdf.py（剥 script + 禁 JS + 禁网）。
			if err := s.chatSandbox.ExecProfileAs(ctx, userID, "render_pdf", []string{htmlAbs, pdfAbs}); err != nil {
				res.PdfFailed = true
				s.log.Warn("render_html: PDF 渲染失败，降级为 HTML", zap.Error(err))
			} else if pinfo, err := os.Stat(pdfAbs); err == nil && pinfo.Size() > 0 {
				_ = os.Chmod(pdfAbs, 0o644)
				res.PdfRel = rel(pdfName)
				res.PdfBytes = pinfo.Size()
			} else {
				res.PdfFailed = true
				s.log.Warn("render_html: PDF 产物缺失，降级为 HTML")
			}
		}
	}

	s.log.Info("html document rendered",
		zap.Int64("user_id", userID),
		zap.String("format", req.Format),
		zap.String("rel", res.RelPath),
		zap.Int64("bytes", res.Bytes),
		zap.Bool("pdf_failed", res.PdfFailed),
	)
	return res, nil
}

// renderHTMLTool 网页文档生成工具（HTML 中间层）。
// 由 NewService / ReplaceRegistry 注册（绑定实例，需 workRoot；PDF 需 chatSandbox），
// DefaultToolSet 不注册。作为文档生成主力：HTML 自包含、预览即成品。
type renderHTMLTool struct {
	svc *Service
}

// Schema 实现 Tool 接口。
func (t renderHTMLTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name: "render_html",
		Description: "把内容渲染为自包含网页文档（.html），支持实时预览与下载。" +
			"作为文档生成的主力工具：生成/导出任何文档时**优先使用本工具**（排版表现力强、可在线预览、可导出 PDF），" +
			"仅当用户明确要求 Word/PPT 文件格式时才改用 render_document。" +
			"当用户要求生成/导出网页、精美排版文档、HTML、PDF，或要求文档有好看的样式/布局/图表时调用。" +
			"参数 title 为文档标题（文件名）。html 与 html_file 二选一：html 为完整单文件 HTML 文档（≤300KB）；" +
			"**长文档（论文草稿等）超过 300KB 时，先调用 file_ops 的 write 把完整 HTML 写入工作区文件（相对路径，如 chat-docs/draft.html），" +
			"再传 html_file 为该相对路径**，工具会从文件读取内容渲染，不受 300KB 限制。" +
			"html 规范：必须自包含——样式用内联 <style>，图表用内联 <svg>，公式渲染为内联 SVG 或图片，图片用 data:URL 内联；" +
			"禁止引用外部资源（外链 CSS/JS/图片/字体）；禁止 <script> 与 iframe；" +
			"语义化标签（h1-h3/p/figure/pre/table），并用 @page 设置 A4 打印与分页。" +
			"format 可选：html=仅网页文档（默认）；pdf=同时导出 PDF（依赖沙盒渲染，失败自动降级为网页）。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"format":{"type":"string","enum":["html","pdf"],"description":"html=仅网页文档（默认，可预览可下载）；pdf=同时导出 PDF（失败自动降级为网页）"},
				"title":{"type":"string","description":"文档标题（必填，≤60 字，用于文件名）"},
				"html":{"type":"string","description":"完整单文件 HTML 文档（≤300KB，自包含内联 CSS/SVG/图片，禁外部资源、禁 script/iframe）；与 html_file 二选一"},
				"html_file":{"type":"string","description":"可选：长文档（>300KB）先用 file_ops 的 write 写入工作区（相对路径），此处传该路径；工具从文件读取内容渲染"}
			},
			"required":["title"]
		}`),
		Required:   []string{"title"},
		Permission: schema.PermissionL2Write,
	}
}

// Execute 实现 Tool 接口：净化 → 落盘 →（可选 PDF）→ 返回下载路径指引。
func (t renderHTMLTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var req HTMLDocRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return "", fmt.Errorf("render_html: 参数解析失败: %w", err)
	}
	userID, ok := builtin.UserIDFromContext(ctx)
	if !ok {
		return "", errors.New("render_html: 缺少用户上下文（X-User-Id）")
	}
	res, err := t.svc.renderHTMLDocument(ctx, userID, req)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("已生成网页文档「")
	b.WriteString(req.Title)
	fmt.Fprintf(&b, "」（%d 字节）。\n", res.Bytes)
	b.WriteString("工作区下载路径：")
	b.WriteString(res.RelPath)
	b.WriteString("\n")
	if res.PdfRel != "" {
		fmt.Fprintf(&b, "已同时导出 PDF：%s\n", res.PdfRel)
	}
	if res.PdfFailed {
		b.WriteString("注意：PDF 导出依赖沙盒渲染（未配置或渲染失败），已自动降级为网页文档。\n")
	}
	b.WriteString("请在最终回复中向用户展示下载入口，格式为 Markdown 代码块（内容为工作区下载路径）：\n")
	b.WriteString("```doc\n" + res.RelPath + "\n```")
	if res.PdfRel != "" {
		b.WriteString("\n```doc\n" + res.PdfRel + "\n```")
	}
	return b.String(), nil
}

// 编译期断言：确保 renderHTMLTool 实现了 Tool 接口。
var _ tool.Tool = renderHTMLTool{}
