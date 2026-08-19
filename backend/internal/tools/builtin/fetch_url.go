// Package builtin 智能体内置工具集。
//
// 本文件实现 fetch_url（网页解析）：输入具体 URL，抓取并提取正文文本返回给模型。
// 与 web_search 分工：
//   - web_search：发现——关键词 → 返回标题/链接/摘要列表（不读正文）；
//   - fetch_url：深读——具体 URL → 返回该页正文，通常配合 web_search 使用
//     （先搜索定位链接，再对本工具读取详情）。
package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/Steve5201/agent-framework/schema"
)

// fetchURLMaxBody 抓取响应体上限（流式读取，超过即截断，保证大页面可用）。
// 宽松设置：现代网页可能很大，仅防异常撑爆内存。
const fetchURLMaxBody = 2 << 20 // 2MB

// fetchURLReturnMax 返回给模型的最大文本长度（rune，保护上下文窗口）。
const fetchURLReturnMax = 20000

// fetchURLTimeout 抓取超时。
const fetchURLTimeout = 15 * time.Second

// FetchURLTool L1 只读网页解析工具。
//
// 实现：对给定 URL 发 GET 请求（浏览器 UA），用 HTML5 解析器提取标题与正文
// 文本，去脚本/样式/导航噪音，压缩空白后返回 Markdown。SSRF 防护：拒绝内网/
// 保留 IP 与 localhost，避免让模型探测内网资源。无外部依赖、无需 API Key。
type FetchURLTool struct {
	// Client HTTP 客户端（测试可注入 httptest 地址）；空 = 默认 15s 超时客户端。
	Client *http.Client
	// UserAgent 请求 UA（个别网站拦截空 UA）；空 = 浏览器 UA。
	UserAgent string
	// SkipSSRFCheck 跳过 SSRF 防护（仅测试用：httptest 跑在 127.0.0.1，
	// 会被公网校验拦截）。生产保持 false，始终校验。
	SkipSSRFCheck bool
}

// fetchURLArgs 网页解析参数。
type fetchURLArgs struct {
	URL string `json:"url"`
}

// Schema 实现 Tool 接口。
func (t *FetchURLTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        "fetch_url",
		Description: "网页解析（只读）：抓取并读取指定 URL 网页的正文内容，返回标题与正文文本。当用户给了一个链接、或你在 web_search 结果里看到了想深入了解的链接时使用。参数 url 必填——填完整的网址（含 http/https 前缀）。与 web_search 配合：先 web_search 定位链接，再用本工具读取详情。注意：只读取该单一网页，不支持列表页批量抓取。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"url":{"type":"string","description":"要解析的完整网页 URL（含 http:// 或 https:// 前缀）"}
			}
		}`),
		Required:   []string{"url"},
		Permission: schema.PermissionL1Read,
	}
}

// Execute 实现 Tool 接口：抓取 URL 并提取正文返回 Markdown。
func (t *FetchURLTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p fetchURLArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("fetch_url: 参数解析失败: %w", err)
	}
	p.URL = strings.TrimSpace(p.URL)
	if p.URL == "" {
		return "", fmt.Errorf("fetch_url: url 不能为空")
	}

	parsed, err := url.Parse(p.URL)
	if err != nil {
		return "", fmt.Errorf("fetch_url: URL 格式非法: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("fetch_url: 仅支持 http/https 链接，收到 %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("fetch_url: URL 缺少主机名")
	}
	if !t.SkipSSRFCheck {
		if err := checkURLAllowed(parsed); err != nil {
			return "", err
		}
	}

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: fetchURLTimeout}
	}
	ua := t.UserAgent
	if ua == "" {
		ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", fmt.Errorf("fetch_url: 构造请求失败: %w", err)
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch_url: 网络请求失败（请确认链接可访问）: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch_url: 链接返回 HTTP %d，可能已失效、需登录或拒绝访问", resp.StatusCode)
	}

	// 流式读取 + 上限截断（大页面只取开头，保证可用性）。
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchURLMaxBody))
	if err != nil {
		return "", fmt.Errorf("fetch_url: 读取响应失败: %w", err)
	}

	title, text := extractPageText(body)
	if strings.TrimSpace(text) == "" {
		return fmt.Sprintf("已抓取 %s，但未能提取到正文（页面可能依赖脚本渲染或为空）。", p.URL), nil
	}

	var b strings.Builder
	if title != "" {
		fmt.Fprintf(&b, "## %s\n\n", title)
	}
	fmt.Fprintf(&b, "来源：%s\n\n%s", p.URL, text)
	out := truncateRunes(b.String(), fetchURLReturnMax)
	if len([]rune(b.String())) > fetchURLReturnMax {
		out += "\n\n（内容过长，已截断至前 20000 字。如需更多请结合 web_search 补充查询。）"
	}
	return out, nil
}

// checkURLAllowed SSRF 防护：拒绝解析到内网/保留 IP 或 localhost 的 URL。
// 仅当域名解析为公网地址时才放行。返回 nil 表示允许。
func checkURLAllowed(u *url.URL) error {
	host := u.Hostname()
	// 文字形式的内网域名直接拦截（无需 DNS）。
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("fetch_url: 拒绝访问本地主机（%s）", host)
	}
	// 解析所有 A 记录；只要有一个落在内网/保留段即拒绝。
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("fetch_url: 无法解析主机 %s", host)
	}
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		if !isPublicIP(ip) {
			return fmt.Errorf("fetch_url: 拒绝访问非公网地址 %s（SSRF 防护）", ip)
		}
	}
	return nil
}

// isPublicIP 判断 IP 是否属于公网（非内网/保留/回环/链路本地等）。
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// IPv4 私有与保留段
		switch {
		case v4.IsLoopback(), v4.IsPrivate(), v4.IsLinkLocalUnicast(),
			v4.IsUnspecified(), v4.IsMulticast():
			return false
		}
		return true
	}
	// IPv6：拦截回环/链路本地/唯一本地/未指定/组播。
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return true
}

// extractPageText 用 HTML5 解析器提取标题与正文文本，去掉脚本/样式/导航噪音，
// 并压缩连续空白。同时尝试从 <script> 内嵌的全局状态对象（__INITIAL_STATE__ /
// __NEXT_DATA__ 等）提取结构化数据并入正文——纯 JS 渲染页（SSR/CSR 混合）的
// HTML 骨架为空，正文在脚本变量里，需捞出来避免"抓不到内容"。
func extractPageText(body []byte) (title, text string) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", ""
	}
	// 先单独收集内嵌 JSON 状态（SSR 数据源），供后续并入正文。
	embedded := collectEmbeddedState(doc)
	var sb strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			switch tag {
			case "script", "style", "noscript", "template", "svg", "iframe", "form", "nav", "footer", "header":
				return // 跳过噪音节点
			}
		}
		if n.Type == html.TextNode && strings.TrimSpace(n.Data) != "" {
			sb.WriteString(strings.TrimSpace(n.Data))
			sb.WriteString(" ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	// 先遍历拿标题：单独走一遍找 <title>。
	walkTitle(doc, &title)
	walk(doc)
	text = collapseWhitespace(sb.String())
	// 内嵌 JSON 状态并入正文（页面正文为空或很短时尤其有用）。
	if embedded != "" {
		if text != "" {
			text += " "
		}
		text += embedded
	}
	return title, text
}

// embeddedStateVars 常见前端框架的全局状态变量名（值通常是 JSON 对象）。
// 命中任一即尝试按 JSON 提取。
var embeddedStateVars = []string{
	"__INITIAL_STATE__",
	"__NEXT_DATA__",
	"__PRELOADED_STATE__",
	"__NUXT__",
	"window.__APOLLO_STATE__",
}

// collectEmbeddedState 遍历 <script> 节点，从 "window.XXX=..." 形式的内嵌
// JSON 中提取可读文本。返回空串表示无可用内嵌数据。只捞安全上限内的文本，
// 避免把巨大脚本刷爆上下文。
func collectEmbeddedState(root *html.Node) string {
	if root == nil {
		return ""
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if b.Len() >= fetchURLReturnMax {
			return
		}
		if n.Type == html.ElementNode && strings.ToLower(n.Data) == "script" {
			code := n.FirstChild
			if code != nil && code.Type == html.TextNode {
				extractStateFromScript(code.Data, &b)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return b.String()
}

// extractStateFromScript 从一段脚本源码提取 "window.XXX=<json>" 内嵌数据。
func extractStateFromScript(code string, b *strings.Builder) {
	for _, varName := range embeddedStateVars {
		idx := strings.Index(code, varName+"=")
		if idx < 0 {
			continue
		}
		rest := code[idx+len(varName)+1:]
		// 取到行尾（内嵌 JSON 通常一行内结束）。
		if eol := strings.IndexByte(rest, ';'); eol >= 0 {
			rest = rest[:eol]
		}
		if eol := strings.IndexByte(rest, '\n'); eol >= 0 {
			rest = rest[:eol]
		}
		rest = strings.TrimSpace(rest)
		if rest == "" || rest[0] != '{' {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(rest), &obj); err != nil {
			continue
		}
		// 递归扁平化 JSON 为可读 key: value 文本。
		flattenJSON("", obj, b)
		b.WriteString("\n")
	}
}

// flattenJSON 递归把 JSON 对象扁平化为 "key: value" 文本（value 为标量时
// 才输出，数组/对象递归展开，避免输出纯结构噪音）。
func flattenJSON(prefix string, v any, b *strings.Builder) {
	if b.Len() >= fetchURLReturnMax {
		return
	}
	writeVal := func(key string, val any) {
		s, ok := val.(string)
		if ok && strings.TrimSpace(s) != "" {
			if prefix != "" {
				fmt.Fprintf(b, "%s.%s: %s\n", prefix, key, strings.TrimSpace(s))
			} else {
				fmt.Fprintf(b, "%s: %s\n", key, strings.TrimSpace(s))
			}
		}
	}
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			switch val.(type) {
			case map[string]any, []any:
				child := k
				if prefix != "" {
					child = prefix + "." + k
				}
				flattenJSON(child, val, b)
			default:
				writeVal(k, val)
			}
		}
	case []any:
		for i, val := range t {
			switch val.(type) {
			case map[string]any, []any:
				child := fmt.Sprintf("%s[%d]", prefix, i)
				flattenJSON(child, val, b)
			default:
				writeVal(fmt.Sprintf("%s[%d]", prefix, i), val)
			}
		}
	}
}

// walkTitle 查找页面 <title> 元素文本。
func walkTitle(n *html.Node, title *string) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode && strings.ToLower(n.Data) == "title" {
		if n.FirstChild != nil {
			*title = strings.TrimSpace(n.FirstChild.Data)
		}
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkTitle(c, title)
	}
}

// collapseWhitespace 把连续空白（含换行）压缩为单个空格。
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateRunes 按 rune 数截断字符串（避免切断多字节字符）。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
