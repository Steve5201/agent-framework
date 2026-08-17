package ingest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Steve5201/agent-backend/internal/rag/sandboxclient"
	"github.com/xuri/excelize/v2"
)

func TestParseMarkdown(t *testing.T) {
	src := `# 用户手册

## 安装

执行 **go install**，参考[官网](https://example.com)。

` + "```" + `go
fmt.Println("hi")
` + "```" + `

## 使用

- 第一步
- 第二步

![logo](logo.png) 见上方。`
	doc, err := (Parser{}).Parse([]byte(src), "md", "")
	if err != nil {
		t.Fatal(err)
	}
	// h1 作为文档标题（同时保留为标题段，P4-L：纯标题信息不丢失），
	// h2/h3 作为分段。
	if doc.Title != "用户手册" {
		t.Errorf("Title=%q", doc.Title)
	}
	if len(doc.Segments) != 3 {
		t.Fatalf("应 3 段（用户手册/安装/使用），got %d", len(doc.Segments))
	}
	if doc.Segments[0].Heading != "用户手册" || doc.Segments[1].Heading != "安装" || doc.Segments[2].Heading != "使用" {
		t.Errorf("标题分段错误: %+v", doc.Segments)
	}
	text := doc.Text()
	if strings.Contains(text, "**") {
		t.Errorf("markdown 语法未清理:\n%s", text)
	}
	// A3b：图片占位保留（供前端渲染/检索引用），链接等行内语法剥离。
	if !strings.Contains(text, "![logo](logo.png)") {
		t.Errorf("图片占位应保留: %s", text)
	}
	if strings.Contains(text, "[官网](https://example.com)") {
		t.Errorf("普通链接应剥离: %s", text)
	}
	if !strings.Contains(text, "go install") || !strings.Contains(text, "官网") {
		t.Errorf("文本丢失: %s", text)
	}
	if !strings.Contains(text, "fmt.Println") {
		t.Errorf("代码围栏内容应保留: %s", text)
	}
}

// TestParseMarkdown_HeadingOnlyDoc 纯目录/纯标题文档（P4-L 修复，回归用例）：
// pandoc 对 docx 章节目录输出的纯标题 markdown（含空标题裸 `###`，无空格）
// 旧逻辑会把标题段整体丢弃、裸 `###` 混入正文——read_document 解析
// 《场地污染水文地质学》第二章.docx 只返回 `###` 即此根因。修复后所有标题
// 行应完整保留进 Text()，空标题不泄漏。
func TestParseMarkdown_HeadingOnlyDoc(t *testing.T) {
	src := `# 第二章 场地污染水文地质学

## 2.1 概述

### 2.1.1 污染源

### 

## 2.2 地下水流场`
	doc, err := (Parser{}).Parse([]byte(src), "md", "")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "第二章 场地污染水文地质学" {
		t.Errorf("Title=%q", doc.Title)
	}
	// 标题行必须全部保留（Text() 输出标题段）。
	for _, want := range []string{"第二章 场地污染水文地质学", "2.1 概述", "2.1.1 污染源", "2.2 地下水流场"} {
		if !strings.Contains(doc.Text(), want) {
			t.Errorf("标题丢失 %q，正文:\n%s", want, doc.Text())
		}
	}
	// 空标题（裸 ###）不应泄漏为正文。
	if strings.Contains(doc.Text(), "###") {
		t.Errorf("空标题不应泄漏为正文:\n%s", doc.Text())
	}
	// 段数 = 4 个非空标题（空标题段被丢弃）。
	if len(doc.Segments) != 4 {
		t.Fatalf("应 4 段，got %d: %+v", len(doc.Segments), doc.Segments)
	}
}

func TestParseHTML(t *testing.T) {
	src := `<html><head><title>课程简介</title></head><body>
<h1>第一章</h1><p>这是<strong>重点</strong>内容。</p>
<script>alert("x")</script>
<h2>1.1 小节</h2><ul><li>甲</li><li>乙</li></ul>
</body></html>`
	doc, err := (Parser{}).Parse([]byte(src), "html", "")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "课程简介" {
		t.Errorf("Title=%q", doc.Title)
	}
	if len(doc.Segments) != 2 || doc.Segments[0].Heading != "第一章" || doc.Segments[1].Heading != "1.1 小节" {
		t.Fatalf("HTML 标题分段错误: %+v", doc.Segments)
	}
	if strings.Contains(doc.Text(), "alert") {
		t.Error("script 内容不应进入文本")
	}
	if !strings.Contains(doc.Text(), "重点") {
		t.Error("正文文本丢失")
	}
}

func TestParseTxt(t *testing.T) {
	doc, err := (Parser{}).Parse([]byte("纯文本内容"), "txt", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Segments) != 1 || !strings.Contains(doc.Segments[0].Text, "纯文本") {
		t.Errorf("txt 解析错误: %+v", doc.Segments)
	}
}

func TestParseUnsupported(t *testing.T) {
	if _, err := (Parser{}).Parse([]byte("x"), "pdf", ""); err == nil {
		t.Error("pdf 一期应返回不支持")
	}
}

func TestChunker_ByHeading(t *testing.T) {
	doc := &ParsedDoc{
		Title: "手册",
		Segments: []Segment{
			{Heading: "安装", Level: 2, Text: "安装步骤一。\n安装步骤二。"},
			{Heading: "使用", Level: 2, Text: "使用说明。"},
		},
	}
	chunks := (Chunker{}).Chunk(doc, ChunkOptions{})
	if len(chunks) != 2 {
		t.Fatalf("应 2 个 chunk，got %d", len(chunks))
	}
	if chunks[0].Source != "安装" || chunks[0].Metadata["title"] != "手册" {
		t.Errorf("chunk 元数据错误: %+v", chunks[0])
	}
}

func TestChunker_OverlongSplit(t *testing.T) {
	longText := strings.Repeat("句子甲。句子乙。", 100) // 1200 字符
	doc := &ParsedDoc{Segments: []Segment{{Text: longText}}}
	chunks := (Chunker{}).Chunk(doc, ChunkOptions{MaxLen: 300, Overlap: 50})
	if len(chunks) < 4 {
		t.Fatalf("长文本应切为多块，got %d", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c.Content)) > 350 {
			t.Errorf("chunk[%d] 超长: %d", i, len([]rune(c.Content)))
		}
		if strings.TrimSpace(c.Content) == "" {
			t.Errorf("chunk[%d] 内容为空", i)
		}
	}
	// 相邻块应有重叠：次块开头包含首块末尾的字符。
	last := []rune(chunks[0].Content)
	tail := string(last[len(last)-3:])
	if !strings.Contains(chunks[1].Content, tail) {
		t.Errorf("相邻块应重叠，尾部 %q 未出现在次块: %q", tail, chunks[1].Content)
	}
}

func TestSplitByLen_Short(t *testing.T) {
	parts := splitByLen("短文本", 800, 100)
	if len(parts) != 1 || parts[0] != "短文本" {
		t.Errorf("短文本应整体返回: %v", parts)
	}
}

func TestSplitByLen_SentenceBoundary(t *testing.T) {
	text := "第一句。第二句。第三句。第四句。第五句。第六句。" // 每个句子 4 字
	parts := splitByLen(text, 8, 2)
	if len(parts) < 3 {
		t.Fatalf("应多块: %v", parts)
	}
	if !strings.HasSuffix(parts[0], "。") {
		t.Errorf("首块应在句号处切断: %q", parts[0])
	}
	// 重叠：次块开头与首块末尾重叠。
	last := []rune(parts[0])
	prevTail := string(last[len(last)-2:])
	if !strings.HasPrefix(parts[1], prevTail) {
		t.Errorf("应有重叠: %q 应出现在次块开头，实际 %q", prevTail, parts[1])
	}
}

// TestParseXLSX A3b：excelize 把每个工作表转 Markdown 表格段。
func TestParseXLSX(t *testing.T) {
	f := excelize.NewFile()
	if err := f.SetSheetName("Sheet1", "成绩单"); err != nil {
		t.Fatal(err)
	}
	_ = f.SetCellValue("成绩单", "A1", "姓名")
	_ = f.SetCellValue("成绩单", "B1", "分数")
	_ = f.SetCellValue("成绩单", "A2", "张三")
	_ = f.SetCellValue("成绩单", "B2", 95)
	_ = f.SetCellValue("成绩单", "A3", "李四")
	_ = f.SetCellValue("成绩单", "B3", 88)

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}

	doc, err := (Parser{}).Parse(buf.Bytes(), "xlsx", "")
	if err != nil {
		t.Fatalf("xlsx 解析失败: %v", err)
	}
	if doc.Title != "成绩单" {
		t.Errorf("Title=%q", doc.Title)
	}
	if len(doc.Segments) != 1 || doc.Segments[0].Heading != "成绩单" {
		t.Fatalf("应 1 个表格段: %+v", doc.Segments)
	}
	text := doc.Segments[0].Text
	if !strings.Contains(text, "| 姓名") || !strings.Contains(text, "| 张三") || !strings.Contains(text, "---") {
		t.Errorf("Markdown 表格不完整:\n%s", text)
	}
}

// TestParser_PDFRequiresSandbox 未启用解析沙盒时，pdf 等应明确报错（保留旧行为）。
func TestParser_PDFRequiresSandbox(t *testing.T) {
	_, err := (Parser{}).Parse([]byte("%PDF mock"), "pdf", "")
	if err == nil {
		t.Fatal("无沙盒时 pdf 应报错")
	}
	if !strings.Contains(err.Error(), "RAG_SANDBOX_URL") {
		t.Errorf("错误应提示沙盒配置: %v", err)
	}
}

// TestParser_ExternalScanOnly 扫描版 PDF：沙盒产物无正文 → 返回显式错误。
func TestParser_ExternalScanOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["profile"] != nil {
			args := body["args"].([]any)
			_ = os.WriteFile(args[1].(string), []byte(
				`{"title":"","markdown":"","media":[],"scan_only":true,"warnings":["扫描版"]}`), 0o644)
		}
		_, _ = w.Write([]byte(`{"exit_code":0}`))
	}))
	defer srv.Close()

	parser := Parser{Sandbox: sandboxclient.New(srv.URL, t.TempDir(), 1, nil)}
	_, err := parser.Parse([]byte("%PDF mock"), "pdf", "doc_test")
	if err == nil {
		t.Fatal("扫描版 PDF 应报错")
	}
	if !strings.Contains(err.Error(), "扫描版") {
		t.Errorf("错误应提示扫描版: %v", err)
	}
}

// TestParser_ExternalOK 沙盒解析成功：markdown 复用 parseMarkdown 分段，媒体透传。
func TestParser_ExternalOK(t *testing.T) {
	const product = `{
	  "title": "课件",
	  "markdown": "## 第一章\n公式 $E=mc^2$ 与图片 ![图](rag-media/d1/fig.png)",
	  "media": [{"type":"image","path":"rag-media/d1/fig.png","alt":"图"}],
	  "scan_only": false,
	  "warnings": ["注意：公式未还原"]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["profile"] != nil {
			args := body["args"].([]any)
			_ = os.WriteFile(args[1].(string), []byte(product), 0o644)
			// P3-A6：docID 应透传给沙盒作媒体目录名（rag-media/<docID>/）。
			// 平台无关断言：统一转正斜杠再匹配（Windows 下 mediaDir 为反斜杠）。
			mediaDir := strings.ReplaceAll(args[2].(string), "\\", "/")
			if !strings.Contains(mediaDir, "rag-media/doc_test") {
				t.Errorf("媒体目录应含 docID: %v", args[2])
			}
		}
		_, _ = w.Write([]byte(`{"exit_code":0}`))
	}))
	defer srv.Close()

	parser := Parser{Sandbox: sandboxclient.New(srv.URL, t.TempDir(), 1, nil)}
	doc, err := parser.Parse([]byte("%PDF mock"), "pdf", "doc_test")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if doc.Title != "课件" {
		t.Errorf("Title=%q", doc.Title)
	}
	if len(doc.Segments) != 1 || doc.Segments[0].Heading != "第一章" {
		t.Fatalf("分段错误: %+v", doc.Segments)
	}
	if len(doc.Media) != 1 || doc.Media[0].Path != "rag-media/d1/fig.png" {
		t.Errorf("媒体透传错误: %+v", doc.Media)
	}
	if len(doc.Warnings) != 1 {
		t.Errorf("警告透传错误: %+v", doc.Warnings)
	}
	if !strings.Contains(doc.Segments[0].Text, "![图](rag-media/d1/fig.png)") {
		t.Errorf("图片占位应保留在正文: %s", doc.Segments[0].Text)
	}
}

// TestChunk_MediaMetadata A3b：文档媒体清单汇总进 chunk metadata["media"]。
func TestChunk_MediaMetadata(t *testing.T) {
	doc := &ParsedDoc{
		Title: "课件",
		Segments: []Segment{
			{Heading: "第一章", Level: 1, Text: "内容 ![图](rag-media/d1/fig.png)"},
		},
		Media: []MediaItem{
			{Type: "image", Path: "rag-media/d1/fig.png", Alt: "图"},
			{Type: "image", Path: "rag-media/d1/fig.png", Alt: "图（重复去重）"},
			{Type: "video", Path: "rag-media/d1/v1.mp4", Alt: ""},
		},
	}
	chunks := (Chunker{}).Chunk(doc, ChunkOptions{})
	if len(chunks) != 1 {
		t.Fatalf("应 1 个 chunk，got %d", len(chunks))
	}
	media := chunks[0].Metadata["media"]
	if media != "rag-media/d1/fig.png,rag-media/d1/v1.mp4" {
		t.Errorf("media 元数据错误（应去重）: %q", media)
	}
	if !strings.Contains(chunks[0].Content, "![图](rag-media/d1/fig.png)") {
		t.Errorf("chunk 内容应含图片占位: %q", chunks[0].Content)
	}
}
