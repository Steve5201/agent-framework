// profiles.go —— 预置解析脚本注册表（P3-A3b：pdf/docx/pptx 委托 sandbox 解析）。
//
// profile 模式（ExecRequest.Profile 非空）执行的是镜像内预置脚本
// （/opt/rag-parsers，Dockerfile 构建期 COPY，与 Go 代码同仓版本管理），
// 而非用户提交的 code，因此 Exec 跳过 code 黑名单/白名单校验；
// 参数由 rag 侧构造（相对用户工作区的 input/out/media 路径）。
//
// 新增解析器：1) 在镜像预置脚本；2) 在此登记；3) rag 侧 sandboxclient 按
// fileType 选择 profile。
package sandboxsvc

import "time"

// profileSpec 预置解析脚本定义。
type profileSpec struct {
	// Cmd 脚本解释器/命令（python3 / sh）。
	Cmd string
	// Script 容器内脚本文件名（ParsersDir 下）。
	Script string
	// ArgCount 要求的参数个数（input 文件、输出 JSON、媒体目录），防参数错位。
	ArgCount int
	// MaxTimeout 该类 profile 的专属超时上限：大文档解析（PDF 图片提取等）
	// 可超过普通代码执行上限，故单独放宽；普通 code 执行仍受 MaxTimeout 约束。
	MaxTimeout time.Duration
}

// parserProfiles 解析器注册表。
var parserProfiles = map[string]profileSpec{
	"parse_pdf":  {Cmd: "python3", Script: "parse_pdf.py", ArgCount: 3, MaxTimeout: 120 * time.Second},
	"parse_docx": {Cmd: "python3", Script: "parse_docx.py", ArgCount: 3, MaxTimeout: 120 * time.Second},
	"parse_pptx": {Cmd: "python3", Script: "parse_pptx.py", ArgCount: 3, MaxTimeout: 120 * time.Second},
	// P4-I 文档生成：按 DocumentSpec（spec.json）渲染 Word/PPT，产物落用户工作区
	// 供 agent /files 下载。参数 = [spec.json 绝对路径, 输出文件绝对路径]。
	// 输出目录由调用方（agent）预创建，prepareProfileDirs 按 args[1] 父目录纠正
	// 属主为派生 uid（渲染进程降权后可写），脚本写出后 chmod 0644（app 组/other 可读）。
	"render_docx": {Cmd: "python3", Script: "render_docx.py", ArgCount: 2, MaxTimeout: 60 * time.Second},
	"render_pptx": {Cmd: "python3", Script: "render_pptx.py", ArgCount: 2, MaxTimeout: 60 * time.Second},
	// P5-HTML 中间层文档生成：render_html（format=pdf）委托 Chromium headless
	// `--print-to-pdf` 把自包含 HTML 渲染为 PDF。参数 = [输入 html 绝对路径,
	// 输出 pdf 绝对路径]。缺 chromium/渲染失败时脚本 emit_error + exit 1，
	// 由 agent 侧降级为 HTML 主产物，不阻断文档生成。
	// 注意：chromium 与 RLIMIT_AS 不兼容（Alpine/musl 下设置即崩），executor
	// 对 render_pdf 跳过 --as 限制（见 executor.go），其余限制（禁网/降权/nofile/
	// cpu/超时）不变。
	"render_pdf": {Cmd: "python3", Script: "render_pdf.py", ArgCount: 2, MaxTimeout: 60 * time.Second},
	// fetch_render：用 Chromium headless 渲染 JS 动态页面并提取正文（解决纯 JS
	// 渲染页——如 B 站等——HTML 骨架为空导致 fetch_url 抓不到内容的问题）。
	// 参数 = [URL 绝对地址, 输出正文 txt 绝对路径]。复用 render_pdf 的 chromium
	// 运行配置（跳过 RLIMIT_AS），且必须联网（由 agent 侧在请求里
	// network_enabled=true 放行，否则 unshare -n 禁网下 chromium 无法加载页面）。
	"fetch_render": {Cmd: "python3", Script: "fetch_render.py", ArgCount: 1, MaxTimeout: 60 * time.Second},
}
