// fetch_url_render.go —— 渲染版网页解析工具（JS 动态页面）。
//
// 与 fetch_url 的分工：
//   - fetch_url：纯 HTTP 抓取 + HTML 文本提取，快、省资源，适合 SSR/静态页；
//   - fetch_url_render：委托沙盒用 Chromium headless 渲染 JS 后抓正文，慢但能
//     覆盖纯 JS 渲染（CSR）页面（如 B 站、React/Vue SPA），HTML 骨架为空的页面。
//
// 关键依赖：渲染需联网加载外网资源，而沙盒默认禁网（unshare -n）。本工具按
// 会话沙盒配置（agent_admin 设定 SandboxNetworkEnabled=true）在沙盒请求里
// network_enabled=true 放行联网。普通 code 执行不受影响（保持禁网）。
package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// fetchRenderTimeout 渲染版抓取的整体超时（含沙盒内 chromium 渲染，比纯 HTTP 宽松）。
const fetchRenderTimeout = 70 * time.Second

// fetchRenderTool L1 只读渲染版网页解析工具。
//
// 实现：委托 sandbox-service 的 fetch_render profile，用 chromium headless
// 渲染 URL 后提取正文文本。SSRF 防护与 fetch_url 一致（拒绝内网/保留 IP）。
// 网络开关取会话沙盒配置（agent_admin 可开网）；未开网时给出明确降级说明。
type fetchRenderTool struct {
	// SandboxURL 沙盒服务地址（与 CodeExecutorTool.SandboxURL 同源配置）。
	SandboxURL string
	// SkipSSRFCheck 跳过 SSRF 防护（仅测试用）。
	SkipSSRFCheck bool
}

// fetchRenderArgs 渲染版解析参数。
type fetchRenderArgs struct {
	URL string `json:"url"`
}

// Schema 实现 Tool 接口。
func (t *fetchRenderTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:        "fetch_url_render",
		Description: "渲染版网页解析（只读，较慢）：用无头浏览器渲染指定 URL 页面（执行页面 JS）后提取正文文本，返回标题与正文。当 fetch_url 抓到的内容为空或明显缺少动态加载的数据（如视频/动态列表页、前端 JS 渲染的应用），或已知目标页面是纯 JS 渲染时使用。参数 url 必填——完整网址（含 http/https 前缀）。注意：需要沙盒联网权限（由管理员在会话配置开启），且比 fetch_url 慢得多（数秒级）；能用 fetch_url 时优先用 fetch_url。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"url":{"type":"string","description":"要渲染解析的完整网页 URL（含 http:// 或 https:// 前缀）"}
			}
		}`),
		Required:   []string{"url"},
		Permission: schema.PermissionL1Read,
	}
}

// Execute 实现 Tool 接口：委托沙盒渲染抓取并返回正文。
func (t *fetchRenderTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.SandboxURL == "" {
		return "", fmt.Errorf("fetch_url_render: 沙盒服务未配置（部署需配置 AGENT_SANDBOX_URL）")
	}
	var p fetchRenderArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("fetch_url_render: 参数解析失败: %w", err)
	}
	p.URL = strings.TrimSpace(p.URL)
	if p.URL == "" {
		return "", fmt.Errorf("fetch_url_render: url 不能为空")
	}
	parsed, err := url.Parse(p.URL)
	if err != nil {
		return "", fmt.Errorf("fetch_url_render: URL 格式非法: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("fetch_url_render: 仅支持 http/https 链接，收到 %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("fetch_url_render: URL 缺少主机名")
	}
	if !t.SkipSSRFCheck {
		if err := checkURLAllowed(parsed); err != nil {
			return "", err
		}
	}

	scfg := SandboxConfigFromContext(ctx)
	if !scfg.NetworkEnabled {
		return "", fmt.Errorf("fetch_url_render: 沙盒联网未开启（本工具需联网渲染动态页）。请联系管理员在会话配置开启沙盒联网，或改用 fetch_url")
	}

	// 委托沙盒 fetch_render profile：渲染 URL 后正文写到 stdout。
	uid, _ := UserIDFromContext(ctx)
	body, err := json.Marshal(sandboxExecRequest{
		UserID:         uid,
		Profile:        "fetch_render",
		Args:           []string{p.URL},
		NetworkEnabled: true,
		TimeoutSecs:    60,
	})
	if err != nil {
		return "", fmt.Errorf("fetch_url_render: 构造沙盒请求失败: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, fetchRenderTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(execCtx, http.MethodPost,
		strings.TrimSuffix(t.SandboxURL, "/")+"/v1/exec", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("fetch_url_render: 构造沙盒请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch_url_render: 沙盒服务不可达: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("fetch_url_render: 沙盒执行失败（HTTP %d）: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var r sandboxExecResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxExecOutput+4096)).Decode(&r); err != nil {
		return "", fmt.Errorf("fetch_url_render: 解析沙盒响应失败: %w", err)
	}
	if r.Error != "" {
		return "", fmt.Errorf("fetch_url_render: 渲染失败: %s", r.Error)
	}
	if r.TimedOut {
		return "", fmt.Errorf("fetch_url_render: 渲染超时，页面加载过慢或需登录")
	}
	// 沙盒脚本把正文写到 stdout，直接回传。
	out := truncateRunes(r.Stdout, fetchURLReturnMax)
	return out, nil
}

// 编译期断言：fetchRenderTool 实现 Tool 接口。
var _ tool.Tool = (*fetchRenderTool)(nil)