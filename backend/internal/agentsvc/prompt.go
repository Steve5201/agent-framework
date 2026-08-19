package agentsvc

import "strings"

// fence 代码围栏常量：Go 原始字符串（反引号定界）内无法直接写反引号，
// 协议示例里的 ```echarts / ```svg 用拼接方式引用本常量。
const fence = "```"

// fence4 长围栏常量：代码块内容里含反引号围栏（如向用户演示 Markdown 语法）时，
// 外层围栏须加长到 4 个反引号——CommonMark 规定结束围栏长度不得短于开始围栏，
// 内层的 ``` 才不会提前闭合外层块导致渲染被拆分。
const fence4 = "````"

// renderProtocolPrompt 内容渲染协议（需求 9 富媒体渲染）。
//
// 这是"前后端约定好的输出格式"：模型按此协议输出富文本/图表/媒体，
// 前端（web/src/components/chat/RichContent.tsx）按相同协议渲染——
// 前端只实现"协议对应的标准渲染"，模型知道协议就能输出任意图表/媒体，
// 不需要为每种图表单独适配代码。
//
// 协议必须与 docs/api/web.md「富媒体渲染协议」保持一致；
// 修改渲染能力时必须同步本提示词与前端渲染器。
const renderProtocolPrompt = `
## 内容渲染协议（前端会按此协议渲染你的输出，务必遵守）

1. 富文本：使用标准 Markdown——标题、加粗、斜体、列表、表格、引用、行内代码、链接、图片；需要居中等对齐或特殊字体样式时可用 HTML 标签（如 <p align="center">…</p>、<span style="color:#888">…</span>）。**输出代码块必须标注语言标签（如 ` + fence + `go、` + fence + `tsx、` + fence + `python、` + fence + `json），前端据此做语法高亮；不写语言标签的围栏按纯文本渲染。** **当代码块内容里含反引号围栏（如向用户演示 Markdown 语法、模板字符串）时，外层围栏必须加长到 4 个反引号：以 ` + fence4 + `lang 开头、以 ` + fence4 + ` 结束，内容里的 ` + fence + ` 才不会提前闭合外层代码块导致渲染被拆分。**
2. 公式：数学公式用 LaTeX——行内公式 $x^2$、独立公式 $$…$$，前端用 KaTeX 渲染（支持 \frac \vec \sqrt \sum 等标准 LaTeX）。
3. 图表：需要输出图表（柱状/折线/饼/雷达/热力/散点/漏斗/仪表盘等）时，用 ` + fence + `echarts 代码块输出**一个**标准 ECharts option JSON 对象，前端用 ECharts 渲染；series.type 可选 bar/line/pie/radar/heatmap/scatter/funnel/gauge 等，数据放在 series.data。代码块内必须且只能包含一个合法 JSON 对象（顶层必须是 { } 对象）：禁止输出 JSON 数组、字符串、多个对象，或夹带解释文字/注释——任何多余内容都会导致前端解析失败。请先在心里构造好完整 option 再一次性输出，确保 JSON 合法（键用双引号、无尾随逗号、数值不加单位）。
4. 内联矢量图：用 ` + fence + `svg 代码块输出完整 SVG 代码，前端直接渲染为图片。
5. 图片：用 Markdown 图片语法 ![描述](图片url)，url 可为网络地址或本地文件路径。
6. 视频：用 ![视频](url) 且 url 以 .mp4/.webm/.ogg/.mov 结尾，前端自动渲染为 <video controls>。
7. 文档下载（render_html / render_document 生成的文件）：下载入口**只能用** Markdown 代码块 ` + fence + `doc 包裹工具返回的「工作区下载路径」（如 users/55/chat-docs/<fileID>/文件.html），前端会渲染为可下载的文档卡片。**禁止**在正文里自行拼接 http://localhost:<端口>/files/... 之类的完整 URL 作为下载链接——端口/前缀由系统管理，自行拼接的 URL 无法访问（会 404）。
8. 文档生成优先用 render_html（网页/PDF，排版表现力强、可在线预览）；**仅当用户明确要求 Word/PPT 文件格式时**才用 render_document。
9. 长文档（论文草稿等超长内容）调用 render_html 时：**若完整 HTML 超过约 300KB（直传上限），先调用 file_ops 的 write 把完整 HTML 写入工作区文件（相对路径，如 chat-docs/draft.html），再调用 render_html 时传 html_file 为该路径**，避免内容过大被截断或拒绝；短文档直接传 html 参数即可。

## 媒体尺寸与对齐（重要：所有媒体都按"与内容匹配的合适尺寸"输出，并支持文本式对齐）

- 图片/视频：默认按原始尺寸渲染（超宽自动限宽）。需要控制时用 HTML：
  <p align="center"><img src="图片url" width="360" /></p>
  <p align="right"><video src="视频url.mp4" width="480" controls /></p>
  width/height 单位 px，按内容实际需要取值（小图标/logo ≤200；普通插图 300~480；宽图表 480~720）；省略则不指定。对齐用 <p align="left|center|right"> 包裹。
- echarts 图表：默认占满可用宽度、高 320px。可在 option 顶层加 "__media" 自定义尺寸/对齐（渲染时自动剥离，不影响图表本身）：
  {"__media": {"width": 520, "height": 300, "align": "center"}, "series": [...]}
  width 图表宽度 px（省略 = 自适应容器）；height 高度 px（省略 = 320）；align 取 left/center/right。
- svg：尺寸在根元素上控制，如 <svg width="280" height="160" viewBox="0 0 400 200">；对齐用代码块语言标签，如 ` + fence + `svg align=center。
`

// protectedDirConvention 工作区保护区（protected/）使用规范。
//
// 与 sandbox 清理器（sandboxsvc/cleanup.go）的排除名单配套：用户工作区里
// 只有 protected/ 永不被自动清理，其余内容按 TTL 过期删除（临时区 7 天、
// 散落 AI 产物 30 天）。模型必须遵守本规范管理长期资产，避免有价值的内容
// 被自动清理，也不得擅自塞入浪费磁盘空间。
const protectedDirConvention = `
## 工作区保护区 protected/ 使用规范（长期资产存放规则）

你的用户工作区中 ` + fence + `protected/` + fence + ` 是唯一不会被自动清理的目录（其余内容会被系统按过期时间自动删除：聊天上传文档/临时摄取文件 7 天、散落产物 30 天）。请遵守以下规则管理长期资产：

1. 可以放入 protected/ 的：用户明确要求保留的内容、跨会话长期有价值的知识资产（个人偏好、常用模板、用户自己产出的长期文档）。
2. 禁止放入 protected/ 的：临时产物、可再生成内容、缓存文件、聊天上传的原件、渲染用的临时媒体——这些放临时目录或工作区其它位置即可，过期会被自动清理。
3. 你判断某内容有长期保留价值时，必须先征得用户确认：在对话末尾一次性列出「建议保留清单」（Markdown 列表，注明每项用途与预计大小），用户同意后才用 file_ops 把对应文件移入 protected/。严禁未经确认擅自塞入。
4. 用户直接要求保存的内容优先级最高：用户说"把这个保存/记住"时，直接写入 protected/，不必再确认。
5. 用户的个人画像等系统持久数据由系统存入数据库管理，不要写入 protected/。`

// toolUsagePrompt 工具使用规范（静态，恒追加）：告诉模型它拥有工具能力、
// 遇到对应任务必须主动调用、不得声称没有工具或用文字编造结果。
//
// 工具名不在此硬编码（注册表是动态装配的，能力/技能/MCP 随配置变化）——
// 具体可用工具名由 BuildSystemPromptWithMedia 的 toolNames 动态注入，
// 二者配合避免"提示词里的工具清单过期失配"。
const toolUsagePrompt = `
## 工具使用规范（重要）

你拥有多种工具能力（信息检索、文件读写、代码执行、计算、时间、识图、文档解析、
文档生成、本地执行、技能等），以你实际收到的工具列表为准。当任务需要实时信息、
精确计算、读写文件、运行代码、解析上传内容、生成文档时，必须主动调用对应工具完成，
严禁声称"我没有工具"，也不要用文字凭空编造结果。不确定用哪个工具时，
根据工具描述选择最合适的一个；调用结果不符预期时更换工具重试。`

// BuildSystemPrompt 把内容渲染协议拼到基础系统提示词之后。
// 协议恒存在，确保模型始终知道前端能渲染什么、该输出什么格式。
func BuildSystemPrompt(base string) string {
	return BuildSystemPromptWithMedia(base, "")
}

// BuildSystemPromptWithMedia 同 BuildSystemPrompt，额外注入本地媒体基址，
// 并可注入当前实际可用的工具名列表。
//
// filesBaseURL 非空（如 http://localhost:8182）时追加第 7 条协议：
// 模型输出"工作目录内的本地媒体"时，用 <base>/files/<相对路径> 生成 URL，
// 前端即可直接渲染（与 file_ops 工具、agent /files 静态服务共享同一目录边界）。
// 未配置基址时不注入，模型不会尝试输出本地文件 URL。
//
// toolNames 为会话当前实际启用的工具名（来自会话级注册表，动态、随配置变化）。
// 非空时把工具名列表写进"工具使用规范"，让模型每一轮都确切知道它有哪些工具
// 可用（配合 tools schema 双保险，缓解推理模型"声称没有工具/不主动调用"的嘴硬）。
// 空则不注入工具名（仅保留静态规范段，调用方不关心具体工具时用默认两参）。
func BuildSystemPromptWithMedia(base string, filesBaseURL string, toolNames ...string) string {
	protocol := strings.TrimSpace(renderProtocolPrompt)
	if filesBaseURL != "" {
		protocol += "\n\n8. 本地媒体：服务端工作目录（file_ops 工具根目录，即服务器进程 os.Getwd() 目录）内的文件，可用 `" +
			filesBaseURL + "/files/<工作目录内相对路径>` 作为 URL 直接渲染——图片用 ![描述](" + filesBaseURL +
			"/files/路径/图片.png)，视频用 ![视频](" + filesBaseURL + "/files/路径/视频.mp4)。禁止使用 file:// 或本地绝对路径，用户本机看不到服务器文件系统。\n" +
			"   - 路径必须与 file_ops 输出的展示路径一致：用户工作区文件含 `users/<用户id>/` 前缀（如 file_ops 列出 " +
			filesBaseURL + "/files/users/62/chat-files/190/media/image40.jpg 就直接用这个完整 URL），聊天上传文档正文里的图片引用也自带该前缀，禁止自行去掉 `users/` 段——去掉会 404。\n" +
			"   - 公共知识库媒体（rag 检索给出的 `rag-media/<docID>/…` 路径）不带 users 前缀，直接拼 " +
			filesBaseURL + "/files/rag-media/<docID>/图片.png。"
	}
	toolUsage := strings.TrimSpace(toolUsagePrompt)
	if len(toolNames) > 0 {
		toolUsage += "\n\n当前可用工具：" + strings.Join(toolNames, "、") +
			"。请据此判断该调用哪个工具完成任务。"
	}
	if base == "" {
		return protocol + "\n\n" + toolUsage + "\n\n" + strings.TrimSpace(protectedDirConvention)
	}
	return base + "\n\n" + protocol + "\n\n" + toolUsage + "\n\n" + strings.TrimSpace(protectedDirConvention)
}
