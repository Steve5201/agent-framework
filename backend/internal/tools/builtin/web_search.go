package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// WebSearchTool L1 只读网络搜索工具。
//
// 实现：无密钥方案——请求搜索引擎的 HTML 端点，从结果页解析标题/链接/摘要，
// 格式化为 Markdown 列表返回给模型。无外部依赖、无需 API Key。
//
// 后端选择（Backend 字段）：
//   - "bing"（默认）：请求 cn.bing.com/search——国内可直连（DDG 在国内常被墙，
//     实测 html.duckduckgo.com 无法访问），无需翻墙即可搜索；
//   - "duckduckgo"：请求 html.duckduckgo.com/html/（海外网络环境可用）。
//
// 容器部署已带 ca-certificates，HTTPS 正常。
type WebSearchTool struct {
	// Backend 搜索后端：bing（默认）| duckduckgo；空 = bing。
	Backend string
	// BaseURL 搜索端点（测试可注入 httptest 地址）；空 = 按 Backend 取默认端点。
	BaseURL string
	// Client HTTP 客户端（测试可注入）；空 = 默认 15s 超时客户端。
	Client *http.Client
	// UserAgent 请求 UA（个别网络会拦截空 UA）；空 = 浏览器 UA。
	UserAgent string
}

// webSearchArgs 搜索参数。
type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

// webSearchResult 单条搜索结果。
type webSearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// Schema 实现 Tool 接口。
func (t *WebSearchTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        "web_search",
		Description: "网络搜索（只读）：用搜索引擎检索网页，返回结果列表（标题+链接+摘要）。当用户询问实时信息、最新资讯、不在你训练数据中的内容时使用。参数 query 必填——尽量精简具体（如 \"2026 高考分数线 四川\"），不要用疑问句；max_results 可选（返回条数 1-10，默认 5）。注意：本工具不读取链接正文，如需详细内容请让用户提供或换 file_ops 读取。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"query":{"type":"string","description":"搜索关键词，精简具体，如 2026 高考分数线 四川"},
				"max_results":{"type":"integer","description":"返回结果条数 1-10，默认 5"}
			}
		}`),
		Required:   []string{"query"},
		Permission: schema.PermissionL1Read,
	}
}

// Execute 实现 Tool 接口：执行搜索并返回 Markdown 结果列表。
func (t *WebSearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p webSearchArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("web_search: 参数解析失败: %w", err)
	}
	p.Query = strings.TrimSpace(p.Query)
	if p.Query == "" {
		return "", fmt.Errorf("web_search: query 不能为空")
	}
	limit := p.MaxResults
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	ua := t.UserAgent
	if ua == "" {
		ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
	}

	base := t.BaseURL
	if base == "" {
		switch backend := strings.ToLower(t.Backend); backend {
		case "duckduckgo":
			base = "https://html.duckduckgo.com/html/"
		default: // "bing" / 空
			base = "https://cn.bing.com/search"
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return "", fmt.Errorf("web_search: 构造请求失败: %w", err)
	}
	q := req.URL.Query()
	q.Set("q", p.Query)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_search: 网络请求失败（请确认服务可访问外网）: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web_search: 搜索服务返回 HTTP %d，可能被限流，请稍后重试", resp.StatusCode)
	}
	// 上限 2MB，防止异常页面撑爆内存。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", fmt.Errorf("web_search: 读取响应失败: %w", err)
	}

	results := extractSearchResults(body, limit, strings.ToLower(t.Backend))
	if len(results) == 0 {
		return "未找到与查询相关的网页结果，可尝试更换关键词后重试。", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "搜索“%s”的结果：\n", p.Query)
	for i, r := range results {
		fmt.Fprintf(&b, "%d. [%s](%s)\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
	}
	return b.String(), nil
}

// ---------------------------------------------------------------------------
// 结果页解析（正则提取，布局稳定）：
//   - Bing：<li class="b_algo"> 块 + <h2><a> 标题/链接 + <p> 摘要；
//   - DuckDuckGo：result__a / result__snippet。
// ---------------------------------------------------------------------------

var (
	// resultLinkRe 匹配 DDG <a ... class="result__a" href="...">标题</a>。
	resultLinkRe = regexp.MustCompile(`(?s)<a[^>]*class="result__a"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	// snippetRe 匹配 DDG <a ... class="result__snippet" ...>摘要</a>。
	snippetRe = regexp.MustCompile(`(?s)<a[^>]*class="result__snippet"[^>]*>(.*?)</a>`)

	// bingAlgoRe 匹配 Bing <li class="b_algo"> 结果块。
	bingAlgoRe = regexp.MustCompile(`(?s)<li class="b_algo"[^>]*>(.*?)</li>`)
	// bingTitleRe 在结果块内匹配 <h2><a href="...">标题</a>。
	bingTitleRe = regexp.MustCompile(`(?s)<h2[^>]*>\s*<a[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	// bingSnipRe 在结果块内匹配第一个 <p> 摘要。
	bingSnipRe = regexp.MustCompile(`(?s)<p[^>]*>(.*?)</p>`)

	// htmlTagRe 剥离残余 HTML 标签。
	htmlTagRe = regexp.MustCompile(`(?s)<[^>]+>`)
	// spaceRe 折叠连续空白。
	spaceRe = regexp.MustCompile(`\s+`)
)

// cleanHTML 剥离标签 + 反转义实体 + 折叠空白。
func cleanHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// extractSearchResults 按后端选择解析器提取结果列表。
func extractSearchResults(body []byte, limit int, backend string) []webSearchResult {
	if backend == "duckduckgo" {
		return extractDDGResults(body, limit)
	}
	return extractBingResults(body, limit)
}

// extractBingResults 从 Bing 结果页提取（b_algo 块 → h2 a + p）。
func extractBingResults(body []byte, limit int) []webSearchResult {
	blocks := bingAlgoRe.FindAllSubmatch(body, -1)
	out := make([]webSearchResult, 0, len(blocks))
	for i, m := range blocks {
		if i >= limit {
			break
		}
		block := m[1]
		tm := bingTitleRe.FindSubmatch(block)
		if tm == nil {
			continue
		}
		title := cleanHTML(string(tm[2]))
		if title == "" {
			continue
		}
		r := webSearchResult{
			Title: title,
			// Bing 结果 href 即目标地址（含跟踪参数无碍），反转义 &amp;。
			URL: strings.ReplaceAll(string(tm[1]), "&amp;", "&"),
		}
		if sm := bingSnipRe.FindSubmatch(block); sm != nil {
			r.Snippet = cleanHTML(string(sm[1]))
		}
		out = append(out, r)
	}
	return out
}

// extractDDGResults 从 DDG 结果页提取（result__a / result__snippet）。
func extractDDGResults(body []byte, limit int) []webSearchResult {
	links := resultLinkRe.FindAllSubmatch(body, -1)
	snips := snippetRe.FindAllSubmatch(body, -1)

	out := make([]webSearchResult, 0, len(links))
	for i, m := range links {
		if i >= limit {
			break
		}
		raw := string(m[1])
		// DDG 链接是跳转地址，真实目标在 uddg 查询参数里。
		target := raw
		if u, err := url.Parse(strings.ReplaceAll(raw, "&amp;", "&")); err == nil {
			if dd := u.Query().Get("uddg"); dd != "" {
				target = dd
			}
		}
		r := webSearchResult{
			Title: cleanHTML(string(m[2])),
			URL:   target,
		}
		if i < len(snips) {
			r.Snippet = cleanHTML(string(snips[i][1]))
		}
		if r.Title == "" {
			continue
		}
		out = append(out, r)
	}
	return out
}

// 编译期断言：WebSearchTool 实现 Tool 接口。
var _ tool.Tool = (*WebSearchTool)(nil)
