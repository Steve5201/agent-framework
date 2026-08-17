// Package ingest 文档解析与分块（P3-A3）。
//
// 格式支持（P3-A3b）：
//   - md / txt / html / xlsx：Go 原生解析（xlsx 用 excelize 转 Markdown 表格）；
//   - pdf / docx / pptx：委托 sandbox-service 预置解析脚本（profile 模式，
//     PyMuPDF / pandoc / python-pptx，见 backend/scripts/parsers），需配置
//     RAG_SANDBOX_URL 且 rag 挂载共享卷；
//   - .doc（OLE 老格式）明确拒绝并提示另存 .docx。
//
// 解析产物为"标题分段"结构，分块器据此生成带标题上下文的 chunk
// （引用溯源的基础）。媒体（图片/视频/音频）提取后落共享卷 rag-media/，
// chunk 内保留图片占位引用供前端渲染。
package ingest

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/Steve5201/agent-backend/internal/rag/sandboxclient"
)

// ErrUnsupportedFileType 不支持的文档格式。
var ErrUnsupportedFileType = errors.New("不支持的文档格式（支持 md/txt/html/xlsx；pdf/docx/pptx 需启用解析沙盒）")

// Segment 按标题划分的内容段。
type Segment struct {
	Heading string // 标题文本（纯文本段为空）
	Level   int    // 1~6；纯文本段为 0
	Text    string // 段内文本（已剥离标记语法）
}

// MediaItem 文档解析提取的媒体文件（图片/视频/音频）。
type MediaItem struct {
	Type string // image | video | audio | other
	Path string // 相对沙盒工作区的持久路径（rag-media/<docID>/<file>）
	Alt  string // 图片占位 alt
}

// ParsedDoc 解析后的结构化文档。
type ParsedDoc struct {
	Title    string
	Segments []Segment
	Media    []MediaItem // A3b：提取的媒体（来自沙盒解析产物）
	Warnings []string    // A3b：解析警告（扫描版 PDF、公式未还原等）
}

// Text 拼接全部段文本（全文）。标题段（Heading 非空）输出标题行，
// 保证"纯目录/纯标题"文档（如 docx 章节目录）解析后正文不丢失。
func (d *ParsedDoc) Text() string {
	var b strings.Builder
	for _, s := range d.Segments {
		if s.Heading != "" {
			b.WriteString(s.Heading)
			b.WriteString("\n")
		}
		b.WriteString(s.Text)
		b.WriteString("\n\n")
	}
	return b.String()
}

// Parser 文档解析器（无状态，可并发）。
// Sandbox 为空时 pdf/docx/pptx 返回 ErrUnsupportedFileType（保持未接入沙盒的旧行为，
// 本地单测/纯 md 环境不受影响）。
type Parser struct {
	Sandbox *sandboxclient.Client
}

// Parse 按格式分发解析。fileType 为空时按内容近似处理为 txt。
// pdf/docx/pptx 委托沙盒预置解析脚本（需要 Parser.Sandbox 非空）。
//
// docID 为数据库文档主键（P3-A6）：非空时用作沙盒 ingest/rag-media 目录名，
// 保证删除文档时可定位并清理其提取的媒体文件（cleanupMedia 按文档 ID 找目录）；
// 空串回退随机临时目录（旧行为，供测试/内部调用）。
func (p Parser) Parse(data []byte, fileType string, docID string) (*ParsedDoc, error) {
	switch strings.ToLower(fileType) {
	case "md", "markdown":
		return parseMarkdown(data), nil
	case "html", "htm":
		return parseHTML(data), nil
	case "xlsx":
		return parseXLSX(data)
	case "pdf", "docx", "pptx":
		if p.Sandbox == nil {
			return nil, fmt.Errorf("%w: %s（需启用解析沙盒 RAG_SANDBOX_URL）", ErrUnsupportedFileType, fileType)
		}
		return p.parseExternal(data, fileType, docID)
	case "txt", "":
		return &ParsedDoc{Segments: []Segment{{Text: string(data)}}}, nil
	default:
		return nil, ErrUnsupportedFileType
	}
}

// parseExternal 委托沙盒解析：产物 markdown 复用 parseMarkdown 分段，
// 媒体/警告透传，扫描版 PDF 返回显式错误提示（避免摄取出空库）。
func (p Parser) parseExternal(data []byte, fileType, docID string) (*ParsedDoc, error) {
	if docID == "" {
		docID = externalDocID(fileType)
	}
	result, err := p.Sandbox.Parse(context.Background(), fileType, data, docID)
	if err != nil {
		return nil, err
	}
	doc := parseMarkdown([]byte(result.Markdown))
	if len(doc.Segments) == 0 {
		if result.ScanOnly {
			return nil, errors.New("文档为扫描版 PDF（无文本层），无法提取正文，建议上传可选中文字的 PDF 或 Word/PPT 源文件")
		}
		return nil, errors.New("文档解析后无有效正文内容")
	}
	if doc.Title == "" {
		doc.Title = result.Title
	}
	for _, m := range result.Media {
		doc.Media = append(doc.Media, MediaItem{Type: m.Type, Path: m.Path, Alt: m.Alt})
	}
	doc.Warnings = append(doc.Warnings, result.Warnings...)
	return doc, nil
}

// externalDocID 沙盒解析的临时文档 id（共享卷 ingest 目录命名，无需全局唯一）。
func externalDocID(fileType string) string {
	return fmt.Sprintf("x_%d_%s", time.Now().UnixNano(), fileType)
}

// ---------------------------------------------------------------------------
// Markdown
// ---------------------------------------------------------------------------

var (
	// 空标题正则放宽为 \s*：pandoc 对"空标题"输出裸 `###`（# 后无空格），
	// 旧正则要求 \s+ 会漏判，导致裸 `###` 被当正文塞进 Text（read_document
	// 解析 docx 章节目录只得到 `###` 的根因）。\s* 同时兼容 #foo 这类无空格写法。
	reHeading = regexp.MustCompile(`^#{1,6}\s*(.*)$`)
	reImage   = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLink    = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reBold    = regexp.MustCompile(`\*\*(.*?)\*\*`)
	reItalic  = regexp.MustCompile(`\*(.*?)\*`)
)

func parseMarkdown(data []byte) *ParsedDoc {
	doc := &ParsedDoc{}
	var cur *Segment
	flush := func() {
		// 标题段（Heading 非空）即使 Text 为空也必须保留：纯目录/纯标题文档
		// 的全部信息都在标题里（P4-L 修复：docx 章节目录此前被整体丢弃）。
		if cur != nil && (strings.TrimSpace(cur.Text) != "" || cur.Heading != "") {
			doc.Segments = append(doc.Segments, *cur)
		}
		cur = nil
	}

	lines := strings.Split(string(data), "\n")
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			if !inFence {
				continue
			}
		}
		if inFence {
			if cur == nil {
				cur = &Segment{}
			}
			cur.Text += line + "\n"
			continue
		}
		if m := reHeading.FindStringSubmatch(trimmed); m != nil {
			flush()
			level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			text := cleanMD(m[1])
			// 首个一级标题作为文档标题（h1 通常无正文，单独成段会被丢弃）。
			if level == 1 && doc.Title == "" {
				doc.Title = text
			}
			cur = &Segment{Heading: text, Level: level, Text: ""}
			continue
		}
		if cur == nil {
			cur = &Segment{}
		}
		cur.Text += cleanMD(line) + "\n"
	}
	flush()
	return doc
}

// cleanMD 剥离行内 markdown 语法（链接/粗体/斜体/引用/列表标记）。
// 图片标记保留原样（![alt](path)），供前端渲染与检索引用（A3b 媒体占位）。
func cleanMD(s string) string {
	// 先抽离图片，避免 reLink 把 ![alt](url) 中的 [alt](url) 误剥。
	var imgs []string
	s = reImage.ReplaceAllStringFunc(s, func(m string) string {
		imgs = append(imgs, m)
		return imgPlaceholder
	})
	s = reLink.ReplaceAllString(s, "$1")
	s = reBold.ReplaceAllString(s, "$1")
	s = reItalic.ReplaceAllString(s, "$1")
	if strings.HasPrefix(s, ">") {
		s = strings.TrimPrefix(strings.TrimSpace(s), ">")
	}
	if strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "* ") {
		s = s[2:]
	}
	// 还原图片占位（按抽离顺序逐个还原）。
	for _, im := range imgs {
		s = strings.Replace(s, imgPlaceholder, im, 1)
	}
	return s
}

// imgPlaceholder 图片占位 token（cleanMD 抽离/还原用；正文含 \x00 的场景不存在）。
const imgPlaceholder = "\x00IMG\x00"

// ---------------------------------------------------------------------------
// HTML
// ---------------------------------------------------------------------------

func parseHTML(data []byte) *ParsedDoc {
	doc := &ParsedDoc{}
	root, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		// 解析失败降级为纯文本，不阻塞摄取。
		return &ParsedDoc{Segments: []Segment{{Text: string(data)}}}
	}
	var cur *Segment
	flush := func() {
		if cur != nil && strings.TrimSpace(cur.Text) != "" {
			doc.Segments = append(doc.Segments, *cur)
		}
		cur = nil
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			// 跳过 head/title/script/style 内的文本，避免污染正文分段。
			if n.Parent != nil {
				switch n.Parent.Data {
				case "title", "head", "script", "style":
					return
				}
			}
			text := strings.TrimSpace(n.Data)
			if text == "" {
				return
			}
			if cur == nil {
				cur = &Segment{}
			}
			cur.Text += text + "\n"
			return
		}
		switch n.Data {
		case "script", "style", "nav", "footer":
			return
		case "h1", "h2", "h3", "h4", "h5", "h6":
			flush()
			level := int(n.Data[1] - '0')
			cur = &Segment{Heading: nodeText(n), Level: level}
			return
		case "title":
			if doc.Title == "" {
				doc.Title = nodeText(n)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	flush()
	return doc
}

// nodeText 提取节点内全部文本（含子节点）。
func nodeText(n *html.Node) string {
	var b strings.Builder
	var collect func(*html.Node)
	collect = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(n)
	return strings.TrimSpace(b.String())
}
