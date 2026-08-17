package agentsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // 嵌入 IANA 时区数据库：容器（alpine 无 /usr/share/zoneinfo）与 Windows 上 LoadLocation 也能识别 Asia/Shanghai 等

	"github.com/Steve5201/agent-backend/internal/rag/ingest"
	"github.com/Steve5201/agent-backend/internal/tools"
	"github.com/Steve5201/agent-backend/internal/tools/builtin"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// ToolSetOptions 工具集装配选项（运行时配置透传给内置工具）。
type ToolSetOptions struct {
	// WebSearchBackend 搜索后端：bing（默认，国内可直连）| duckduckgo。
	WebSearchBackend string
	// CodeExecAllowlist code_executor 命令白名单（正则列表，逗号分隔配置）；
	// 非空时仅命中白名单的命令可执行，其它拒绝；空 = 仅黑名单限制。
	CodeExecAllowlist []string
	// SandboxURL 沙盒服务地址（阶段2）。非空时 code_executor 代码执行委托
	// 给独立 sandbox-service（禁网+资源限制+每用户工作区）；空 = 进程内执行。
	SandboxURL string
	// SkillsRoot 技能根目录（阶段3·技能资源只读访问）。注入给 file_ops，
	// 使模型能用 @skills/<技能名>/… 虚拟路径读取技能内文档与脚本；
	// 空 = file_ops 按默认 <工作目录>/skills 解析（与 skill Provider 默认一致）。
	SkillsRoot string
	// DiskQuota 写 protected/ 前的磁盘配额校验回调（模块三·保护区配额）。
	// nil = 不校验（历史行为）；装配方（cmd/agent）注入 agentsvc.DiskQuotaEnforcer。
	DiskQuota builtin.CheckDiskQuota
	// Providers 附加工具提供者（Skill / MCP 等外部能力源，按声明顺序注册）。
	Providers []tools.ToolProvider
}

// ToolSetOption 函数式配置项。
type ToolSetOption func(*ToolSetOptions)

// WithWebSearchBackend 指定 web_search 后端（bing|duckduckgo）。
func WithWebSearchBackend(backend string) ToolSetOption {
	return func(o *ToolSetOptions) { o.WebSearchBackend = backend }
}

// WithCodeExecAllowlist 指定 code_executor 命令白名单（正则列表）。
func WithCodeExecAllowlist(patterns []string) ToolSetOption {
	return func(o *ToolSetOptions) { o.CodeExecAllowlist = patterns }
}

// WithSandboxURL 指定沙盒服务地址（阶段2）：非空时代码执行委托 sandbox-service。
func WithSandboxURL(url string) ToolSetOption {
	return func(o *ToolSetOptions) { o.SandboxURL = url }
}

// WithSkillsRoot 指定技能根目录：注入给 file_ops，使 @skills/ 虚拟路径可读技能资源。
// 应与 Skill Provider 的 Root 保持一致（缺省均为 <工作目录>/skills）。
func WithSkillsRoot(root string) ToolSetOption {
	return func(o *ToolSetOptions) { o.SkillsRoot = root }
}

// WithDiskQuota 指定写 protected/ 前的磁盘配额校验回调（模块三·保护区配额）。
// nil = 不校验（历史行为）。
func WithDiskQuota(q builtin.CheckDiskQuota) ToolSetOption {
	return func(o *ToolSetOptions) { o.DiskQuota = q }
}

// WithProviders 追加外部工具提供者（Skill / MCP）。
//
// 这是 MCP / Skill 等外部能力源的统一注入点：管理端后续把"已启用的技能与
// MCP server"转成对应 Provider 实例传入即可，注册路径保持单一可控。
func WithProviders(providers ...tools.ToolProvider) ToolSetOption {
	return func(o *ToolSetOptions) { o.Providers = append(o.Providers, providers...) }
}

// DefaultToolSet 构建 agent-service 的默认工具集（P2-45 + 内置工具集）。
//
// 注册内容：
//   - builtin 提供者：calculator（L0）/ web_search（L1）/ file_ops（L2）/ code_executor（L3）；
//   - 通用工具：echo（L0，联调自检）/ get_current_time（L0）。
//
// 装配方式：内置工具通过 tools.RegisterProviders（ToolProvider 接口）注入——
// 未来接入 MCP / Skill 时，只需新增实现 ToolProvider 的提供者并在此追加，
// 注册路径单一可控（internal/tools/provider.go）。
// 可选 opts 透传运行时配置（搜索后端 / 命令白名单），测试与默认调用可不传。
func DefaultToolSet(opts ...ToolSetOption) (*tool.Registry, error) {
	o := ToolSetOptions{}
	for _, f := range opts {
		f(&o)
	}
	reg := tool.NewRegistry()
	if err := tools.RegisterProviders(reg, builtin.Builtin{
		WebSearchBackend:  o.WebSearchBackend,
		CodeExecAllowlist: o.CodeExecAllowlist,
		SandboxURL:        o.SandboxURL,
		SkillsRoot:        o.SkillsRoot,
		DiskQuota:         o.DiskQuota,
	}); err != nil {
		return nil, fmt.Errorf("agentsvc: 注册内置工具集失败: %w", err)
	}
	if err := reg.Register(echoTool{}); err != nil {
		return nil, fmt.Errorf("agentsvc: 注册 echo 工具失败: %w", err)
	}
	if err := reg.Register(getCurrentTimeTool{}); err != nil {
		return nil, fmt.Errorf("agentsvc: 注册 get_current_time 工具失败: %w", err)
	}
	// 阶段3·本地工具：External=true，由桌面客户端确认后执行（浏览器环境前端会立即回填降级结果）。
	if err := reg.Register(builtin.LocalShellTool{}); err != nil {
		return nil, fmt.Errorf("agentsvc: 注册 local_shell 工具失败: %w", err)
	}
	// 外部能力源（Skill / MCP 等）：统一经 ToolProvider 注册，与内置工具同路径。
	for _, p := range o.Providers {
		if err := tools.RegisterProviders(reg, p); err != nil {
			return nil, fmt.Errorf("agentsvc: 注册提供者 %s 失败: %w", p.Name(), err)
		}
	}
	return reg, nil
}

// echoTool L0 回显工具：把入参原样返回。
//
// 用途：
//   - 教学示例：演示"工具注册 → LLM 发起调用 → 结果回填 → 继续推理"闭环；
//   - 联调自检：不依赖真实业务数据即可验证 agent 工具链路是否通。
//
// 说明：为让模型知道"回声工具"的语义，描述里明确提示只有测试/演示
// 时才使用，避免干扰正常问答。
type echoTool struct{}

// echoArgs 回显工具参数。
type echoArgs struct {
	Text string `json:"text"`
}

// Schema 实现 Tool 接口。
func (echoTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        "echo",
		Description: "回声工具（仅测试/演示用）：把输入的 text 原样返回。日常问答请勿调用。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"text":{"type":"string","description":"要回显的文本"}
			}
		}`),
		Required:   []string{"text"},
		Permission: schema.PermissionL0Pure,
	}
}

// Execute 实现 Tool 接口。
func (echoTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p echoArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("echo: 参数解析失败: %w", err)
	}
	return "echo: " + p.Text, nil
}

// 编译期断言：确保 echoTool 实现了 Tool 接口。
var _ tool.Tool = echoTool{}

// getCurrentTimeTool L0 通用时间工具：返回当前日期时间（含星期与时区）。
//
// 用途：一般智能体标配能力——模型需要"现在几点/今天几号"时可直接
// 调用，无需依赖外部服务；纯本地计算、无副作用、零网络。
// 参数 timezone 可选（IANA 时区名），缺省用服务器本地时区。
type getCurrentTimeTool struct{}

// getCurrentTimeArgs 时间工具参数。
type getCurrentTimeArgs struct {
	Timezone string `json:"timezone"`
}

// Schema 实现 Tool 接口。
func (getCurrentTimeTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        "get_current_time",
		Description: "获取当前日期与时间，返回格式：日期+时刻+星期+时区。参数 timezone 可选（IANA 时区名，如 Asia/Shanghai、UTC），缺省用服务器本地时区。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"timezone":{"type":"string","description":"IANA 时区名，如 Asia/Shanghai；缺省为服务器本地时区"}
			}
		}`),
		Permission: schema.PermissionL0Pure,
	}
}

// Execute 实现 Tool 接口。
func (getCurrentTimeTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p getCurrentTimeArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("get_current_time: 参数解析失败: %w", err)
		}
	}
	now := time.Now()
	loc := time.Local
	if p.Timezone != "" {
		l, err := time.LoadLocation(p.Timezone)
		if err != nil {
			return "", fmt.Errorf("get_current_time: 未知时区 %q: %w", p.Timezone, err)
		}
		loc = l
	}
	now = now.In(loc)
	return fmt.Sprintf("当前时间：%s（%s），时区 %s", now.Format("2006-01-02 15:04:05"), now.Weekday().String(), loc), nil
}

// 编译期断言：确保 getCurrentTimeTool 实现了 Tool 接口。
var _ tool.Tool = getCurrentTimeTool{}

// describeImageTool 图片视觉解析工具（需求 8·视觉作为智能体能力）。
//
// 背景：上传图片后，模型不一定能看到图片内容（纯文本链路依赖描述中转，
// 描述可能不够细，或用户追问新角度）。本工具让智能体在用户追问图片文字 /
// 图表 / 结构等细节时，随时以指定关注点重新解析图片：经 Service.vision 调用
// OpenAI 兼容多模态端点（VISION_* 环境变量装配），返回文字描述。
//
// path 约定：工作区全局相对路径（users/<uid>/chat-files/<sid>/<file>，
// 与 [图片] 注入消息、file_ops 展示路径同一约定）。
// 工具由 NewService / ReplaceRegistry 注册（绑定实例），DefaultToolSet 不注册。
type describeImageTool struct {
	svc *Service
}

// describeImageArgs 视觉工具参数。
type describeImageArgs struct {
	// Path 图片的工作区全局相对路径。
	Path string `json:"path"`
	// Focus 解析关注点（可选）：如「提取图中全部文字」「识别图表数据」「描述布局」。
	Focus string `json:"focus"`
}

// Schema 实现 Tool 接口。
func (t describeImageTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name: "describe_image",
		Description: "解析一张图片的内容并返回文字描述（视觉能力）。" +
			"当用户上传图片后追问图中文字/图表/结构等细节，或需要以不同角度重新查看图片时调用。" +
			"参数 path 为 [图片] 消息中的工作区路径（users/<uid>/chat-files/<sid>/<file>）。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","description":"图片在工作区的全局相对路径，如 users/1/chat-files/3/a.png（来自 [图片] 注入消息中的路径字段）"},
				"focus":{"type":"string","description":"可选：解析关注点，如「提取图中全部文字」「识别图表数据」「描述布局结构」；缺省为通用描述"}
			},
			"required":["path"]
		}`),
		Required:   []string{"path"},
		Permission: schema.PermissionL0Pure,
	}
}

// Execute 实现 Tool 接口：读工作区图片 → vision.Describe → 返回描述文本。
func (t describeImageTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p describeImageArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("describe_image: 参数解析失败: %w", err)
	}
	p.Path = strings.TrimSpace(p.Path)
	if p.Path == "" {
		return "", errors.New("describe_image: 缺少 path 参数")
	}
	full := filepath.Join(t.svc.effectiveWorkRoot(), filepath.FromSlash(p.Path))
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("describe_image: 读取图片失败（%s）: %w", p.Path, err)
	}
	dctx, cancel := context.WithTimeout(ctx, visionTimeout)
	defer cancel()
	desc, err := t.svc.vision.Describe(dctx, data, imageMimeFor(p.Path))
	if err != nil {
		return "", fmt.Errorf("describe_image: 视觉解析失败: %w", err)
	}
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return "", errors.New("describe_image: 视觉模型返回空描述")
	}
	return desc, nil
}

// 编译期断言：确保 describeImageTool 实现了 Tool 接口。
var _ tool.Tool = describeImageTool{}

// readDocumentTool 文档解析工具（需求 P2·文档解析作为可配置能力）。
//
// 背景：上传文档后，系统只注入提示词（[文档] 标记 + 工作区路径），不再自动
// 解析正文。智能体在会话配置了「文档解析」能力时拥有本工具，可自行决定是否
// 读取文档正文：读工作区文件 → 复用 rag ingest 解析管线（md/txt/html/xlsx
// 原生纯 Go；pdf/docx/pptx 委托 chatSandbox 沙盒）→ 返回正文文本。
//
// path 约定：工作区全局相对路径（users/<uid>/chat-files/<sid>/<file>，
// 与 [文档] 注入消息、file_ops 展示路径同一约定）。
// 工具由 NewService / ReplaceRegistry 注册（绑定实例），DefaultToolSet 不注册。
type readDocumentTool struct {
	svc *Service
}

// readDocumentArgs 文档解析工具参数。
type readDocumentArgs struct {
	// Path 文档的工作区全局相对路径。
	Path string `json:"path"`
	// MaxChars 返回正文的最大字符数（可选；缺省 8000，超长截断并提示）。
	MaxChars int `json:"max_chars"`
}

// Schema 实现 Tool 接口。
func (t readDocumentTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name: "read_document",
		Description: "解析一份上传的文档并返回正文文本（文档解析能力）。" +
			"当用户上传文档后询问内容/总结/提取要点，或需要读取文档中的表格数据时调用。" +
			"参数 path 为 [文档] 消息中的工作区路径（users/<uid>/chat-files/<sid>/<file>）。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string","description":"文档在工作区的全局相对路径，如 users/1/chat-files/3/简介.md（来自 [文档] 注入消息中的路径字段）"},
				"max_chars":{"type":"integer","description":"可选：返回正文的最大字符数（缺省 8000）；文档较长时建议显式限制"}
			},
			"required":["path"]
		}`),
		Required:   []string{"path"},
		Permission: schema.PermissionL0Pure,
	}
}

// Execute 实现 Tool 接口：读工作区文档 → ingest 解析 → 返回正文文本（截断）。
func (t readDocumentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p readDocumentArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("read_document: 参数解析失败: %w", err)
	}
	p.Path = strings.TrimSpace(p.Path)
	if p.Path == "" {
		return "", errors.New("read_document: 缺少 path 参数")
	}
	// 按扩展名分发：文档走 ingest 解析管线；图片明确指引用 describe_image。
	fileType, ok := chatDocTypes[strings.ToLower(filepath.Ext(p.Path))]
	if !ok || fileType == "image" {
		return "", fmt.Errorf("read_document: 不支持的文件类型（%s），仅支持 md/txt/html/xlsx/pdf/docx/pptx；图片请用 describe_image", filepath.Ext(p.Path))
	}
	full := filepath.Join(t.svc.effectiveWorkRoot(), filepath.FromSlash(p.Path))
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("read_document: 读取文档失败（%s）: %w", p.Path, err)
	}
	if len(data) == 0 {
		return "", errors.New("read_document: 文件内容为空")
	}
	parser := ingest.Parser{Sandbox: t.svc.chatSandbox}
	doc, err := parser.Parse(data, fileType, "")
	if err != nil {
		return "", fmt.Errorf("read_document: 文档解析失败: %w", err)
	}
	text := strings.TrimSpace(doc.Text())
	if text == "" {
		return "", errors.New("read_document: 文档解析后无有效正文内容")
	}
	maxChars := p.MaxChars
	if maxChars <= 0 {
		maxChars = t.svc.maxChatDocInjectRunes
	}
	return truncateRunes(text, maxChars), nil
}

// 编译期断言：确保 readDocumentTool 实现了 Tool 接口。
var _ tool.Tool = readDocumentTool{}
