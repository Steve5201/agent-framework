package agentsvc

import "time"

// 阶段1·资源层：把"普通用户可见的资源/能力"翻译成"工具白名单"。
//
// 设计动机（权限分层）：工具配置不展示给用户——用户只知道模型"有搜索功能"
// 等能力，像思考开关一样按能力勾选；技能按名称展示（不含任何代码）。后端
// 负责 resource → tool 白名单的翻译，用户全程接触不到工具名。
//
// 约定：
//   - 内置能力（capability）由本文件定义，映射到一组工具名；
//   - 技能（skill）由管理端上传，工具名 = "skill_<技能名>"，资源 id = 技能名；
//   - enabled_resources 为空 = 全部启用（与旧 enabled_tools 空语义一致）。

// defaultAgentName 当前智能体标识（预留多智能体扩展，审计表 agent_name 用）。
const defaultAgentName = "default"

// skillToolPrefix 技能工具名前缀（管理端上传技能注册为 skill_<技能名>）。
const skillToolPrefix = "skill_"

// auditWriteTimeout 工具审计落库的独立超时：审计是旁路，写库不应阻塞对话主循环。
const auditWriteTimeout = 3 * time.Second

// capability 一个内置能力：id 即 enabled_resources 里使用的资源标识。
type capability struct {
	id          string   // 资源标识（search/file/code/...）
	name        string   // 展示名
	description string   // 一句话说明
	tools       []string // 该能力映射的工具名白名单
}

// defaultCapabilities 内置能力全集（顺序即前端展示顺序）。
// 注意：工具必须是 DefaultToolSet 注册过的；未注册的工具翻译时会忽略。
var defaultCapabilities = []capability{
	{id: "search", name: "搜索", description: "在互联网搜索最新信息、并读取网页正文内容（含 JS 动态渲染页面），回答时效性问题", tools: []string{"web_search", "fetch_url", "fetch_url_render"}},
	{id: "file", name: "文件读写", description: "在个人工作区读取/写入/搜索文件，梳理目录结构", tools: []string{"file_ops"}},
	{id: "code", name: "代码执行", description: "运行 shell / Python 代码（沙盒内执行，用于计算与脚本任务）", tools: []string{"code_executor"}},
	{id: "calculate", name: "计算", description: "精确四则运算", tools: []string{"calculator"}},
	{id: "time", name: "时间", description: "获取当前日期时间", tools: []string{"get_current_time"}},
	{id: "vision", name: "识图", description: "解析用户上传图片的内容（文字/图表/结构等），回答与图片相关的问题", tools: []string{"describe_image"}},
	{id: "doc", name: "文档解析", description: "解析用户上传的文档内容（文本/表格/PDF/Office 等），回答与文档相关的问题", tools: []string{"read_document"}},
	// 文档生成拆分为两个独立能力（P5-HTML）：网页/PDF 文档（render_html，主力，
	// 排版表现力强、可在线预览、可导出 PDF）与 Office 文档（render_document，
	// 用户明确要求 Word/PPT 格式时才用）。两者独立勾选、互不绑定。
	{id: "webdoc", name: "网页文档", description: "生成可在线预览、排版精美的网页文档（.html），可同时导出 PDF（文档生成主力工具）", tools: []string{"render_html"}},
	{id: "officedoc", name: "Office 文档", description: "生成 Word（.docx）/ PPT（.pptx）文件（用户明确要求 Office 格式时使用）", tools: []string{"render_document"}},
	// 本地执行（local）：External=true 工具（local_shell），由桌面客户端在本机
	// 执行（弹窗确认）。仅桌面端显示该能力（前端按 isTauri 过滤）；浏览器环境
	// 勾选无意义——前端收到 local_shell 调用会立即回填"请使用桌面客户端"。
	{id: "local", name: "本地执行", description: "在你的电脑上执行命令/脚本（仅桌面客户端支持，执行前需你确认）", tools: []string{"local_shell"}},
}

// capabilityByID 索引（id → capability），避免每次遍历。
func capabilityByID(id string) (capability, bool) {
	for _, c := range defaultCapabilities {
		if c.id == id {
			return c, true
		}
	}
	return capability{}, false
}

// defaultCapabilityIDs 返回全部能力 id（enabled_resources 校验用）。
func defaultCapabilityIDs() []string {
	ids := make([]string, 0, len(defaultCapabilities))
	for _, c := range defaultCapabilities {
		ids = append(ids, c.id)
	}
	return ids
}

// resourceToTools 把一组资源标识翻译成工具名白名单。
//
// 规则：
//   - 能力 id → 该能力映射的工具列表；
//   - 其它标识按技能名处理 → "skill_<技能名>"（skill_ 前缀工具）。
//
// 返回的去重工具名即会话的工具白名单；空入参返回空（调用方按"全部启用"处理）。
// 注意：本函数不做"工具是否已注册"校验——注册校验由 validateConfig 与
// registryForConfig 负责（未注册的工具在过滤时记日志忽略）。
func resourceToTools(resources []string) []string {
	seen := make(map[string]bool)
	var tools []string
	for _, id := range resources {
		if c, ok := capabilityByID(id); ok {
			for _, t := range c.tools {
				if !seen[t] {
					seen[t] = true
					tools = append(tools, t)
				}
			}
			continue
		}
		// 技能：工具名 = skill_<技能名>。技能名必须满足管理端命名规则
		//（字母/数字/下划线/连字符，1~50），防止异常标识注入。
		toolName := skillToolPrefix + id
		if !seen[toolName] {
			seen[toolName] = true
			tools = append(tools, toolName)
		}
	}
	return tools
}

// splitResourceTools 把资源标识按类别拆分为能力 id 与技能名两组
// （能力/技能独立 presence 语义的前置拆分；未知标识一律按技能处理，
// 与 resourceToTools 的翻译规则保持一致）。
func splitResourceTools(resources []string) (caps, skills []string) {
	for _, id := range resources {
		if _, ok := capabilityByID(id); ok {
			caps = append(caps, id)
		} else {
			skills = append(skills, id)
		}
	}
	return caps, skills
}

// allCapabilityTools 全部内置能力的工具并集（能力类别"未设置"时的全量白名单，
// 与"未勾选任何能力"等价——跟随实例全量能力）。
func allCapabilityTools() []string {
	seen := make(map[string]bool)
	var tools []string
	for _, c := range defaultCapabilities {
		for _, t := range c.tools {
			if !seen[t] {
				seen[t] = true
				tools = append(tools, t)
			}
		}
	}
	return tools
}

// docGenEnabled Office 文档生成能力（officedoc）是否启用（P4-I 可配置流程管线）。
//
// 语义：
//   - 会话未显式设置能力（EnabledCapabilitiesSet=false，老配置/默认）→ 默认启用
//     （render_document 全量装配 + 编排自动产出照常），向后兼容；
//   - 会话显式勾选了能力 → enabled_resources 白名单须含 officedoc。
//
// 编排自动产出（autoRenderDocument）产出 Word/PPT，绑定 officedoc；网页/PDF
// 文档（webdoc → render_html）由模型显式调用，不参与编排自动产出。
// 供编排完成自动产出与工具装配共用判断。
func (s *Service) docGenEnabled(sess *Session) bool {
	cfg := sess.Config
	if !cfg.EnabledCapabilitiesSet {
		return true
	}
	caps, _ := splitResourceTools(cfg.EnabledResources)
	for _, c := range caps {
		if c == "officedoc" {
			return true
		}
	}
	return false
}
