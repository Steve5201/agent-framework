package agentsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/Steve5201/agent-backend/internal/config"
	"github.com/Steve5201/agent-backend/internal/tools/builtin"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// P4-I 文档生成能力（P4-J 阶段2·富文本块）
//
// 链路：render_document 工具（单智能体）或编排完成后的自动产出（orchestrate）→
// DocumentSpec（JSON 中间层）→ 沙盒 profile（render_docx / render_pptx）渲染 →
// 产物落 users/<uid>/chat-docs/<fileID>/ → 前端经 gateway /files 下载。
//
// DocumentSpec 契约（scripts/parsers/render_common.py 同款）：
//
//	{
//	  "format":   "docx" | "pptx",
//	  "title":    "标题",
//	  "subtitle": "副标题（可选）",
//	  "sections": [{ "heading": "章节标题",
//	                 "body": "正文（可选，\\n 分段轻量标记）",
//	                 "blocks": [ {六种富文本块，见 DocBlock，可选} ] }],
//	  "footer":   "页脚文本（可选，仅 docx）"
//	}
//
// blocks 与 body 的关系：blocks 非空时渲染优先用 blocks；为空回退 body（旧契约）。
// image 块的 rag-media 相对路径在渲染前由 resolveDocMedia 解析为容器内 /work
// 绝对路径并校验存在性（缺失图片宽容跳过），svg:// 内联 SVG 由沙盒脚本转 PNG。
// ---------------------------------------------------------------------------

// DocBlock DocumentSpec 章节内的一块内容（P4-J 阶段2·富文本块）。
// 支持类型：
//
//	paragraph: 普通段落（text）
//	list:      无序列表（items）
//	table:     表格（headers + rows，首行可作表头）
//	image:     插图（src：rag-media/<docID>/<file> 知识库媒体，或 svg://<内联SVG>；
//	           caption 图注；width 显示宽度 px，可选）
//	formula:   公式（text：LaTeX 子集，matplotlib mathtext 渲染为图片插入）
//	code:      代码块（text + language）
//
// 与 body 的关系：sections[i].blocks 非空时渲染优先用 blocks；
// blocks 为空时回退用 body（旧契约，向后兼容）。
type DocBlock struct {
	Type    string     `json:"type"`             // paragraph|list|table|image|formula|code
	Text    string     `json:"text"`             // paragraph / code 内容 / formula LaTeX
	Items   []string   `json:"items"`            // list 项
	Headers []string   `json:"headers"`          // table 表头
	Rows    [][]string `json:"rows"`             // table 数据行
	Src     string     `json:"src"`              // image 来源（rag-media 路径或 svg:// 内联）
	Caption string     `json:"caption"`          // image 图注（可选）
	Width   int        `json:"width"`            // image 显示宽度 px（可选，0=按原图）
	Lang    string     `json:"language"`         // code 语言（可选，仅展示用）
	Render  string     `json:"render,omitempty"` // formula 渲染方式（P4-L）：image=图片（默认，稳健）| native=OMML 原生公式（可编辑/可复制）
}

// DocSection DocumentSpec 的单个章节。
type DocSection struct {
	Heading string     `json:"heading"`
	Body    string     `json:"body"`
	Blocks  []DocBlock `json:"blocks,omitempty"`
}

// DocumentSpec 渲染文档的结构化契约（JSON 中间层，与沙盒脚本对齐）。
type DocumentSpec struct {
	Format   string       `json:"format"`   // docx | pptx
	Title    string       `json:"title"`    // 必填
	Subtitle string       `json:"subtitle"` // 可选
	Sections []DocSection `json:"sections"` // 必填，1..50 节
	Footer   string       `json:"footer"`   // 可选（仅 docx 生效）
}

// 受支持的 block 类型白名单。
var docBlockTypes = map[string]bool{
	"paragraph": true,
	"list":      true,
	"table":     true,
	"image":     true,
	"formula":   true,
	"code":      true,
}

// validateBlock 校验单个内容块（早失败：非法块类型/超长/非法图片来源均拒绝）。
// lims 为文档限制配置（P4-L：env 可配，零值 = 内置默认）。
func validateDocBlock(i, j int, b *DocBlock, lims config.DocConfig) error {
	if !docBlockTypes[b.Type] {
		return fmt.Errorf("sections[%d].blocks[%d].type 非法（支持 paragraph|list|table|image|formula|code，实际 %q）", i, j, b.Type)
	}
	switch b.Type {
	case "paragraph":
		if runeLen(b.Text) > lims.MaxParagraph {
			return fmt.Errorf("sections[%d].blocks[%d] 段落过长（≤%d 字）", i, j, lims.MaxParagraph)
		}
	case "list":
		if len(b.Items) == 0 || len(b.Items) > lims.MaxListItems {
			return fmt.Errorf("sections[%d].blocks[%d].items 需 1~%d 项", i, j, lims.MaxListItems)
		}
		for k, it := range b.Items {
			if runeLen(it) > lims.MaxListItemLen {
				return fmt.Errorf("sections[%d].blocks[%d].items[%d] 过长（≤%d 字）", i, j, k, lims.MaxListItemLen)
			}
		}
	case "table":
		if len(b.Headers) == 0 || len(b.Headers) > lims.MaxTableCols {
			return fmt.Errorf("sections[%d].blocks[%d].headers 需 1~%d 列", i, j, lims.MaxTableCols)
		}
		if len(b.Rows) > lims.MaxTableRows {
			return fmt.Errorf("sections[%d].blocks[%d].rows 超过上限（≤%d 行）", i, j, lims.MaxTableRows)
		}
		for _, row := range b.Rows {
			if len(row) != len(b.Headers) {
				return fmt.Errorf("sections[%d].blocks[%d] 表格列数与表头不一致（%d vs %d）", i, j, len(row), len(b.Headers))
			}
			for _, cell := range row {
				if runeLen(cell) > lims.MaxTableCell {
					return fmt.Errorf("sections[%d].blocks[%d] 单元格过长（≤%d 字）", i, j, lims.MaxTableCell)
				}
			}
		}
	case "image":
		src := strings.TrimSpace(b.Src)
		if src == "" {
			return fmt.Errorf("sections[%d].blocks[%d].src 不能为空", i, j)
		}
		// 知识库媒体（rag-media/<docID>/<file> 相对路径或其绝对形态
		// /work/rag-media/…，容器共享卷根）或内联 SVG（svg://<内容>）。
		if !strings.HasPrefix(src, "rag-media/") &&
			!strings.HasPrefix(src, "/work/rag-media/") &&
			!strings.HasPrefix(src, "svg://") {
			return fmt.Errorf("sections[%d].blocks[%d].src 仅支持 rag-media/ 路径或 svg:// 内联 SVG", i, j)
		}
		if (strings.HasPrefix(src, "rag-media/") || strings.HasPrefix(src, "/work/rag-media/")) && runeLen(src) > lims.MaxImageSrc {
			return fmt.Errorf("sections[%d].blocks[%d].src 路径过长（≤%d）", i, j, lims.MaxImageSrc)
		}
		if strings.HasPrefix(src, "svg://") && runeLen(src) > lims.MaxSVG {
			return fmt.Errorf("sections[%d].blocks[%d].src 内联 SVG 过大（≤%d 字）", i, j, lims.MaxSVG)
		}
		if runeLen(b.Caption) > lims.MaxCaption {
			return fmt.Errorf("sections[%d].blocks[%d].caption 过长（≤%d 字）", i, j, lims.MaxCaption)
		}
		if b.Width < 0 || b.Width > lims.MaxWidth {
			return fmt.Errorf("sections[%d].blocks[%d].width 需在 0~%d px", i, j, lims.MaxWidth)
		}
	case "formula":
		if runeLen(b.Text) > lims.MaxFormula {
			return fmt.Errorf("sections[%d].blocks[%d] 公式过长（≤%d 字）", i, j, lims.MaxFormula)
		}
		if b.Render != "" && b.Render != "image" && b.Render != "native" {
			return fmt.Errorf("sections[%d].blocks[%d].render 仅支持 image|native（实际 %q）", i, j, b.Render)
		}
	case "code":
		if runeLen(b.Text) > lims.MaxCode {
			return fmt.Errorf("sections[%d].blocks[%d] 代码块过长（≤%d 字）", i, j, lims.MaxCode)
		}
	}
	return nil
}

// defaultDocLimits 文档生成内置默认限制（P4-L）。config.DocConfig 随 Load 从
// DOC_* 环境变量注入；此处兜底零值（直接构造 Service 的单元测试/未接线场景）。
func defaultDocLimits() config.DocConfig {
	return config.DocConfig{
		MaxTitleLen:    60,
		MaxSubtitleLen: 100,
		MaxHeadingLen:  100,
		MaxSections:    50,
		MaxSectionBody: 8000,
		MaxBlocks:      100,
		MaxTotalBody:   200000,
		MaxParagraph:   4000,
		MaxListItems:   50,
		MaxListItemLen: 1000,
		MaxTableCols:   12,
		MaxTableRows:   100,
		MaxTableCell:   500,
		MaxImageSrc:    300,
		MaxSVG:         20000,
		MaxCaption:     200,
		MaxWidth:       2000,
		MaxFormula:     500,
		MaxCode:        8000,
		MaxFooter:      200,
		MaxFileName:    40,
		FormulaRender:  "image",
	}
}

// normalizeDocLimits 把零值限制配置归一为内置默认（允许调用方省略）。
func normalizeDocLimits(lims config.DocConfig) config.DocConfig {
	if lims == (config.DocConfig{}) {
		return defaultDocLimits()
	}
	return lims
}

// applyFormulaRenderDefault 给未指定 render 的公式块填充缺省渲染方式。
// def 为 DOC_FORMULA_RENDER 配置值；空或非法回退 image（稳健默认）。
func applyFormulaRenderDefault(spec *DocumentSpec, def string) {
	switch def {
	case "native", "image":
	default:
		def = "image"
	}
	for i := range spec.Sections {
		for j := range spec.Sections[i].Blocks {
			b := &spec.Sections[i].Blocks[j]
			if b.Type == "formula" && b.Render == "" {
				b.Render = def
			}
		}
	}
}

// validate 校验 DocumentSpec（早失败原则：任何非法输入先于落盘/沙盒调用返回）。
// lims 为文档限制配置；零值 = 内置默认（P4-L：env 可配）。
func (s *DocumentSpec) validate(lims config.DocConfig) error {
	lims = normalizeDocLimits(lims)
	if s.Format != "docx" && s.Format != "pptx" {
		return fmt.Errorf("format 必须为 docx 或 pptx（实际 %q）", s.Format)
	}
	s.Title = strings.TrimSpace(s.Title)
	if s.Title == "" {
		return errors.New("title 不能为空")
	}
	if runeLen(s.Title) > lims.MaxTitleLen {
		return fmt.Errorf("title 过长（≤%d 字）", lims.MaxTitleLen)
	}
	s.Subtitle = strings.TrimSpace(s.Subtitle)
	if runeLen(s.Subtitle) > lims.MaxSubtitleLen {
		return fmt.Errorf("subtitle 过长（≤%d 字）", lims.MaxSubtitleLen)
	}
	if len(s.Sections) == 0 {
		return errors.New("sections 至少需要 1 节")
	}
	if len(s.Sections) > lims.MaxSections {
		return fmt.Errorf("sections 超过上限（≤%d 节）", lims.MaxSections)
	}
	totalBody := 0
	for i := range s.Sections {
		sec := &s.Sections[i]
		sec.Heading = strings.TrimSpace(sec.Heading)
		if sec.Heading == "" {
			return fmt.Errorf("sections[%d].heading 不能为空", i)
		}
		if runeLen(sec.Heading) > lims.MaxHeadingLen {
			return fmt.Errorf("sections[%d].heading 过长（≤%d 字）", i, lims.MaxHeadingLen)
		}
		sec.Body = strings.TrimSpace(sec.Body)
		if runeLen(sec.Body) > lims.MaxSectionBody {
			return fmt.Errorf("sections[%d].body 过长（≤%d 字）", i, lims.MaxSectionBody)
		}
		totalBody += runeLen(sec.Body)
		// 富文本块校验（P4-J 阶段2）：blocks 与 body 至少其一，且块级参数合法。
		if len(sec.Blocks) == 0 && sec.Body == "" {
			return fmt.Errorf("sections[%d] 需要 body 或 blocks 至少一项", i)
		}
		if len(sec.Blocks) > lims.MaxBlocks {
			return fmt.Errorf("sections[%d].blocks 超过上限（≤%d 块）", i, lims.MaxBlocks)
		}
		for j := range sec.Blocks {
			if err := validateDocBlock(i, j, &sec.Blocks[j], lims); err != nil {
				return err
			}
		}
	}
	if totalBody > lims.MaxTotalBody {
		return fmt.Errorf("正文总长度超过上限（≤%d 字）", lims.MaxTotalBody)
	}
	s.Footer = strings.TrimSpace(s.Footer)
	if runeLen(s.Footer) > lims.MaxFooter {
		return errors.New("footer 过长（≤200 字）")
	}
	return nil
}

func runeLen(s string) int {
	return len([]rune(s))
}

// renderResult 一次文档渲染的产物信息。
type renderResult struct {
	RelPath      string // 工作区全局相对路径（users/<uid>/chat-docs/<fileID>/<file>）
	FileName     string
	Bytes        int64
	SkippedMedia int // 因缺失被跳过的知识库图片块数量（宽容降级，不阻断渲染）
}

// resolveDocMedia 把 image 块的 rag-media 相对路径解析为容器内绝对路径并检查存在性
// （P4-J 阶段2·图片资产通道）。规则：
//   - svg:// 内联 SVG：不动（脚本内转 PNG）；
//   - rag-media/<docID>/<file>：解析为 workRoot 下绝对路径（容器内 /work 共享卷）；
//     文件存在 → 改写为绝对路径（沙盒脚本 cwd 是 users/<uid>，必须绝对路径才能命中）；
//     文件缺失 → 移除该图片块（宽容降级：跳过缺失图片，文档其余内容正常渲染）。
//
// 返回被移除的图片块数量。
func (s *Service) resolveDocMedia(workRoot string, spec *DocumentSpec) int {
	removed := 0
	for i := range spec.Sections {
		sec := &spec.Sections[i]
		kept := sec.Blocks[:0]
		for _, b := range sec.Blocks {
			if b.Type != "image" || !strings.HasPrefix(b.Src, "rag-media/") {
				kept = append(kept, b)
				continue
			}
			abs := filepath.Join(workRoot, filepath.FromSlash(b.Src))
			if _, err := os.Stat(abs); err != nil {
				s.log.Warn("render_document: 知识库图片缺失，已跳过该图片块",
					zap.String("src", b.Src), zap.Error(err))
				removed++
				continue
			}
			b.Src = filepath.ToSlash(abs)
			kept = append(kept, b)
		}
		sec.Blocks = kept
	}
	return removed
}

// renderDocumentSpec 执行一次文档渲染：写 spec.json → 沙盒 profile → 校验产物。
// 供 render_document 工具与编排自动产出共用（单一入口，行为一致）。
func (s *Service) renderDocumentSpec(ctx context.Context, userID int64, spec DocumentSpec) (*renderResult, error) {
	if err := spec.validate(s.docLimits); err != nil {
		return nil, fmt.Errorf("render_document: 参数非法: %w", err)
	}
	// 公式缺省渲染方式（DOC_FORMULA_RENDER）：未显式指定 render 的公式块
	// 填充缺省（image=图片 | native=OMML 原生可编辑）。
	applyFormulaRenderDefault(&spec, s.docLimits.FormulaRender)
	if s.chatSandbox == nil {
		return nil, errors.New("render_document: 沙盒渲染服务未配置（RAG_SANDBOX_URL 为空）")
	}

	// 知识库图片资产解析（缺失图片宽容跳过，不影响文档渲染）。
	workRoot := s.effectiveWorkRoot()
	skipped := s.resolveDocMedia(workRoot, &spec)

	fileID := fmt.Sprintf("doc_%d_%s", time.Now().UnixMilli(), randSuffix(3))
	fileName := sanitizeDocFileName(spec.Title, spec.Format)
	dir := filepath.Join(workRoot, "users", fmt.Sprint(userID), "chat-docs", fileID)
	if err := ensureGroupWritableDir(dir); err != nil {
		return nil, fmt.Errorf("render_document: 创建工作区目录失败: %w", err)
	}

	specAbs := filepath.Join(dir, "spec.json")
	outAbs := filepath.Join(dir, fileName)
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("render_document: 序列化 spec 失败: %w", err)
	}
	if err := os.WriteFile(specAbs, raw, 0o644); err != nil {
		return nil, fmt.Errorf("render_document: 写入 spec.json 失败: %w", err)
	}

	// 委托沙盒渲染（profile 模式，脚本在 sandbox 容器内执行；prepareProfileDirs
	// 会把输出目录属主纠正为派生 uid，降权渲染进程可写产物）。以真实用户身份
	// 执行：产物落在该用户 2770 私有目录（other=0），必须用其派生 uid 才可写入。
	if err := s.chatSandbox.ExecProfileAs(ctx, userID, "render_"+spec.Format, []string{specAbs, outAbs}); err != nil {
		return nil, err
	}

	info, err := os.Stat(outAbs)
	if err != nil {
		return nil, fmt.Errorf("render_document: 渲染产物缺失（%s）: %w", fileName, err)
	}
	if info.Size() <= 0 {
		return nil, fmt.Errorf("render_document: 渲染产物为空（%s）", fileName)
	}
	// 防御性放宽可读：Docker Desktop bind mount 无 setgid/chown 语义时，脚本
	// chmod 0644 也可能因 umask 被覆盖，agent（app 用户）经 /files 读取需 other 读。
	_ = os.Chmod(outAbs, 0o644)

	rel := filepath.ToSlash(filepath.Join("users", fmt.Sprint(userID), "chat-docs", fileID, fileName))
	s.log.Info("document rendered",
		zap.Int64("user_id", userID),
		zap.String("format", spec.Format),
		zap.String("rel", rel),
		zap.Int64("bytes", info.Size()),
		zap.Int("skipped_media", skipped),
	)
	return &renderResult{RelPath: rel, FileName: fileName, Bytes: info.Size(), SkippedMedia: skipped}, nil
}

// randSuffix 生成 n 字节随机十六进制后缀（文件 ID 防碰撞）。
func randSuffix(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff)
	}
	return hex.EncodeToString(b)
}

// sanitizeDocFileName 由标题生成安全文件名（保留中英文/数字/-_，其余替换为 _）。
func sanitizeDocFileName(title, format string) string {
	runes := []rune(strings.TrimSpace(title))
	if len(runes) > 40 {
		runes = runes[:40]
	}
	name := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, string(runes))
	name = strings.Trim(name, "_.")
	if name == "" {
		name = "document"
	}
	return name + "." + format
}

// renderDocumentTool 文档生成工具（P4-I）。
//
// 背景：用户在会话配置了「文档生成」能力时拥有本工具，模型根据用户需求把
// 结构化内容（标题 + 章节）渲染为 Word/PPT。工具由 NewService / ReplaceRegistry
// 注册（绑定实例，需 chatSandbox/workRoot），DefaultToolSet 不注册。
type renderDocumentTool struct {
	svc *Service
}

// Schema 实现 Tool 接口。
func (t renderDocumentTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name: "render_document",
		Description: "把结构化内容渲染为 Word 文档（.docx）或 PPT 演示文稿（.pptx）。" +
			"**仅当用户明确要求 Word/PPT 文件格式时调用**；其它文档生成需求（网页、PDF、精美排版、可在线预览）请优先使用 render_html。" +
			"当用户要求生成/导出教案、讲义、课件、PPT、Word 文件时调用。" +
			"参数 format 指定格式（docx|pptx），title 为文档标题，sections 为章节列表。" +
			"每节可用 blocks 数组渲染富文本（六种块：paragraph 段落 / list 列表 / table 表格 / image 图片 / formula 公式 / code 代码块），blocks 为空时回退用 body 纯文本。" +
			"图片 src 支持知识库媒体路径（rag-media/<docID>/<file>，来自 kb_search 检索结果的「附带媒体」）或自绘内联 SVG（svg://<SVG内容>）；" +
			"公式用纯 LaTeX 数学语法（如 \\frac、\\sum、\\alpha，禁止混入中文——会渲染乱码）；" +
			"公式块 render 字段可选：image=图片公式（默认，稳健，支持任意 LaTeX），native=原生可编辑公式（docx 效果最佳，支持常见 LaTeX 子集，复杂公式建议用 image）。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"format":{"type":"string","enum":["docx","pptx"],"description":"文档格式：docx=Word 文档；pptx=PPT 演示文稿"},
				"title":{"type":"string","description":"文档标题（必填，≤50 字）"},
				"subtitle":{"type":"string","description":"副标题（可选，≤100 字）"},
				"sections":{"type":"array","description":"章节列表（1~50 节）","items":{
					"type":"object",
					"properties":{
						"heading":{"type":"string","description":"章节标题（必填）"},
						"body":{"type":"string","description":"正文纯文本（可选）：换行分段；行首 - 表示列表项；行首 # 表示二级标题。与 blocks 至少其一"},
						"blocks":{"type":"array","description":"富文本块（可选，非空优先于 body，≤100 块）","items":{
							"type":"object",
							"properties":{
								"type":{"type":"string","enum":["paragraph","list","table","image","formula","code"],"description":"块类型"},
								"text":{"type":"string","description":"段落/公式(纯 LaTeX)/代码内容"},
								"render":{"type":"string","enum":["image","native"],"description":"formula 公式渲染方式（可选）：image=图片公式（默认，稳健）；native=原生可编辑公式（docx 推荐，支持常见 LaTeX 子集）"},
								"items":{"type":"array","items":{"type":"string"},"description":"list 列表项（1~50 项）"},
								"headers":{"type":"array","items":{"type":"string"},"description":"table 表头（1~12 列）"},
								"rows":{"type":"array","items":{"type":"array","items":{"type":"string"}},"description":"table 数据行（与表头列数一致）"},
								"src":{"type":"string","description":"image 来源：rag-media/<docID>/<file>（知识库媒体，来自检索附带媒体），或 svg://<内联SVG>"},
								"caption":{"type":"string","description":"image 图注（可选）"},
								"width":{"type":"integer","description":"image 显示宽度 px（可选，0=按原图）"},
								"language":{"type":"string","description":"code 语言（可选）"}
							},
							"required":["type"]
						}}
					},
					"required":["heading"]
				}},
				"footer":{"type":"string","description":"页脚文本（可选，仅 docx 生效）"}
			},
			"required":["format","title","sections"]
		}`),
		Required:   []string{"format", "title", "sections"},
		Permission: schema.PermissionL2Write,
	}
}

// Execute 实现 Tool 接口：校验 spec → 沙盒渲染 → 返回下载路径指引。
func (t renderDocumentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var spec DocumentSpec
	if err := json.Unmarshal(args, &spec); err != nil {
		return "", fmt.Errorf("render_document: 参数解析失败: %w", err)
	}
	userID, ok := builtin.UserIDFromContext(ctx)
	if !ok {
		return "", errors.New("render_document: 缺少用户上下文（X-User-Id）")
	}
	res, err := t.svc.renderDocumentSpec(ctx, userID, spec)
	if err != nil {
		return "", err
	}

	// 结果回给模型：明确告知如何在最终回复中呈现下载入口。
	var b strings.Builder
	b.WriteString("已生成")
	if spec.Format == "docx" {
		b.WriteString(" Word 文档")
	} else {
		b.WriteString(" PPT 演示文稿")
	}
	fmt.Fprintf(&b, "「%s」（%d 节，%d 字节）。\n", spec.Title, len(spec.Sections), res.Bytes)
	fmt.Fprintf(&b, "工作区下载路径：%s\n", res.RelPath)
	if t.svc.filesBaseURL != "" {
		fmt.Fprintf(&b, "完整下载地址：%s/files/%s\n", t.svc.filesBaseURL, res.RelPath)
	}
	if res.SkippedMedia > 0 {
		fmt.Fprintf(&b, "注意：有 %d 张知识库图片缺失，已自动跳过（文档其余内容正常）。\n", res.SkippedMedia)
	}
	b.WriteString("请在最终回复中向用户展示下载入口，格式为 Markdown 代码块（内容为工作区下载路径）：\n")
	b.WriteString("```doc\n" + res.RelPath + "\n```")
	return b.String(), nil
}

// 编译期断言：确保 renderDocumentTool 实现了 Tool 接口。
var _ tool.Tool = renderDocumentTool{}

// ---------------------------------------------------------------------------
// 编排自动产出（P4-I 编排联动）
// ---------------------------------------------------------------------------

// documentIntent 从用户目标中识别是否需要生成文档及格式。
// 明确 token（ppt/pptx/幻灯片/演示文稿/课件 → pptx；word/docx → docx）直接命中；
// 泛化词"文档"需搭配生成类动词，避免把"总结文档内容"误判为生成请求。
func documentIntent(goal string) (format string, ok bool) {
	g := strings.ToLower(goal)
	for _, kw := range []string{"ppt", "pptx", "幻灯片", "演示文稿", "课件"} {
		if strings.Contains(g, kw) {
			return "pptx", true
		}
	}
	for _, kw := range []string{"word", "docx"} {
		if strings.Contains(g, kw) {
			return "docx", true
		}
	}
	if strings.Contains(g, "文档") {
		for _, v := range []string{"生成", "写一份", "写个", "制作", "做一个", "创建", "导出", "输出", "整理成"} {
			if strings.Contains(g, v) {
				return "docx", true
			}
		}
	}
	return "", false
}

// synthesizeDocumentSpec 由编排汇总结果生成 DocumentSpec（一次 LLM 调用，严格 JSON 输出）。
func (s *Service) synthesizeDocumentSpec(ctx context.Context, format, goal, content string) (*DocumentSpec, error) {
	formatLabel := "Word 文档（.docx）"
	if format == "pptx" {
		formatLabel = "PPT 演示文稿（.pptx）"
	}
	sys := "你是文档结构规划助手。根据用户目标与素材，产出渲染" + formatLabel + "所需的严格 JSON（DocumentSpec），" +
		"只输出 JSON 本身，禁止任何解释性文字。JSON 结构：" +
		`{"format":"` + format + `","title":"文档标题","subtitle":"副标题（可空）",` +
		`"sections":[{"heading":"章节标题","body":"正文：用 \n 分段，行首 - 表示列表项，行首 # 表示二级标题",` +
		`"blocks":[{"type":"paragraph","text":"段落"},{"type":"list","items":["要点1","要点2"]},` +
		`{"type":"table","headers":["列1","列2"],"rows":[["a","b"]]},` +
		`{"type":"image","src":"rag-media/<docID>/<file>","caption":"图注（可空）","width":480},` +
		`{"type":"formula","text":"\\frac{a}{b}","render":"native"},{"type":"code","language":"go","text":"代码内容"}]}],"footer":"页脚（可空）"}` +
		"要求：title 简明概括；sections 覆盖素材全部要点且组织成 3~8 节；" +
		"优先使用 blocks 数组产出结构化富文本（段落/列表/表格/图片/公式/代码块），blocks 与 body 至少其一；" +
		"图片 src 用知识库媒体路径 rag-media/<docID>/<file>（素材中提到知识库图片时）或自绘 svg://<内联SVG>；" +
		"公式用纯 LaTeX 数学语法（mathtext 子集，绝对禁止含中文或中文解释前缀，如“公式：”必须去掉）；" +
		"公式 render 可选（默认 image=图片公式；需可编辑/高精度时用 native=原生公式，docx 效果最佳）；JSON 必须合法。"

	userMsg := fmt.Sprintf("用户目标：%s\n\n可用的整理后素材：\n%s", goal, content)
	resp, err := s.provider.Chat(ctx, &llm.Request{
		Model: s.model,
		Messages: []schema.Message{
			{Role: schema.RoleSystem, Content: sys},
			{Role: schema.RoleUser, Content: userMsg},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("文档结构规划失败: %w", err)
	}
	raw := extractJSONObject(resp.Content)
	if raw == "" {
		return nil, errors.New("文档结构规划返回了非 JSON 内容")
	}
	var spec DocumentSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return nil, fmt.Errorf("文档结构规划 JSON 解析失败: %v", err)
	}
	spec.Format = format // 以调用方格式为准，防模型篡改
	if err := spec.validate(s.docLimits); err != nil {
		return nil, fmt.Errorf("文档结构规划结果非法: %w", err)
	}
	return &spec, nil
}

// extractJSONObject 从模型输出中提取首个 JSON 对象字面量（容忍 ```json 围栏与前后缀）。
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	// 跳过围栏/前缀：若 `{` 前有换行（```json 围栏行或正文换行），从换行后开始。
	head := s[:start]
	if i := strings.LastIndex(head, "\n"); i >= 0 {
		start = i + 1
	}
	// 括号配对扫描，容忍字符串内花括号。
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// autoRenderDocument 编排完成后的自动文档产出（P4-I 编排联动）。
//
// 触发条件：用户目标命中 documentIntent（明确要求生成 Word/PPT/文档）。
// 流程：LLM 从编排汇总结果提炼 DocumentSpec → renderDocumentSpec 渲染 →
// 返回追加到最终回答的下载区块（```doc 代码块，前端渲染下载卡片）。
// 任何一步失败只告警不阻断编排主流程（文档是可选的增值产出）。
func (s *Service) autoRenderDocument(ctx context.Context, userID int64, goal, final string) string {
	format, ok := documentIntent(goal)
	if !ok || strings.TrimSpace(final) == "" {
		return ""
	}
	spec, err := s.synthesizeDocumentSpec(ctx, format, goal, final)
	if err != nil {
		s.log.Warn("编排自动生成文档失败（结构规划）", zap.String("format", format), zap.Error(err))
		return ""
	}
	res, err := s.renderDocumentSpec(ctx, userID, *spec)
	if err != nil {
		s.log.Warn("编排自动生成文档失败（渲染）", zap.String("format", format), zap.Error(err))
		return ""
	}
	label := "Word"
	if format == "pptx" {
		label = "PPT"
	}
	return fmt.Sprintf("\n\n---\n\n**已生成%s文档**（%d 字节）\n\n```doc\n%s\n```", label, res.Bytes, res.RelPath)
}
