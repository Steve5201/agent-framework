package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/schema"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// newTestProvider 构建一个注册了 echo 工具的 InProcess MCP server 的 Provider。
func newTestProvider(t *testing.T, perm string) *Provider {
	t.Helper()
	srv := server.NewMCPServer("test-server", "1.0.0")
	echoTool := mcp.NewTool("echo",
		mcp.WithDescription("回显文本"),
		mcp.WithString("text", mcp.Required(), mcp.Description("要回显的文本")),
	)
	srv.AddTool(echoTool, func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, _ := req.Params.Arguments.(map[string]any)
		text, _ := args["text"].(string)
		return mcp.NewToolResultText("echo: " + text), nil
	})

	cli, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("NewInProcessClient 失败: %v", err)
	}

	conn := newServerConn(&ServerConfig{Name: "test-server", Transport: TransportStdio, Command: "inprocess", DefaultPermission: perm}, nil)
	conn.setClient(cli)
	return &Provider{servers: []*serverConn{conn}}
}

func TestProvider_DiscoverAndCall(t *testing.T) {
	p := newTestProvider(t, "L1")
	tools := p.Tools()
	if len(tools) != 1 {
		t.Fatalf("应发现 1 个工具，实际 %d", len(tools))
	}

	ts := tools[0].Schema()
	if ts.Name != "mcp_test_server_echo" {
		t.Fatalf("工具名应为 mcp_test_server_echo，实际 %q", ts.Name)
	}
	if !strings.Contains(ts.Description, "回显文本") {
		t.Fatalf("描述应透传 MCP 服务器描述: %s", ts.Description)
	}
	if ts.Permission != schema.PermissionLevel(1) {
		t.Fatalf("默认权限应映射 L1，实际 %d", ts.Permission)
	}
	// inputSchema 应透传（含 required 字段，供 ValidateArgs 合并校验）
	if !strings.Contains(string(ts.Parameters), "text") {
		t.Fatalf("Parameters 应透传 JSON Schema: %s", ts.Parameters)
	}

	out, err := tools[0].Execute(context.Background(), json.RawMessage(`{"text":"你好"}`))
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if out != "echo: 你好" {
		t.Fatalf("调用结果应为 echo: 你好，实际 %q", out)
	}
}

func TestProvider_DefaultPermission(t *testing.T) {
	cases := map[string]int{
		"":   2, // 缺省 L2
		"L0": 0,
		"L1": 1,
		"L3": 3,
		"2":  2,
	}
	for cfg, want := range cases {
		p := newTestProvider(t, cfg)
		ts := p.Tools()[0].Schema()
		if ts.Permission != schema.PermissionLevel(want) {
			t.Fatalf("default_permission=%q 应映射 %d，实际 %d", cfg, want, ts.Permission)
		}
	}
}

func TestParseServersJSON(t *testing.T) {
	cfgs, err := ParseServersJSON([]byte(`[
		{"name":"github","transport":"stdio","command":"npx","args":["-y","@modelcontextprotocol/server-github"],"env":{"TOKEN":"x"}},
		{"name":"weather","transport":"http","url":"https://mcp.example.com/weather","headers":{"Authorization":"Bearer x"},"default_permission":"L1"}
	]`))
	if err != nil {
		t.Fatalf("解析合法配置失败: %v", err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("应解析出 2 个 server，实际 %d", len(cfgs))
	}
	if cfgs[0].Transport != "stdio" || cfgs[0].Command != "npx" || len(cfgs[0].Env) != 1 {
		t.Fatalf("stdio 配置解析错误: %+v", cfgs[0])
	}
	if cfgs[1].Transport != "http" || cfgs[1].URL == "" {
		t.Fatalf("http 配置解析错误: %+v", cfgs[1])
	}

	// 空配置 = 零 server（合法）
	empty, err := ParseServersJSON([]byte(``))
	if err != nil || len(empty) != 0 {
		t.Fatalf("空配置应返回空列表: %v", err)
	}

	// 非法用例
	badCases := []string{
		`[{"transport":"stdio","command":"npx"}]`,                                      // 缺 name
		`[{"name":"x","transport":"udp","command":"npx"}]`,                             // 非法 transport
		`[{"name":"x","transport":"stdio"}]`,                                           // stdio 缺 command
		`[{"name":"x","transport":"http","url":"ftp://bad"}]`,                          // http 非法 url
		`[{"name":"x","transport":"stdio","command":"npx","default_permission":"L9"}]`, // 非法权限
		`{"name":"x"}`, // 非数组
	}
	for _, in := range badCases {
		if _, err := ParseServersJSON([]byte(in)); err == nil {
			t.Fatalf("应拒绝非法配置: %s", in)
		}
	}
}

// TestParseServersJSON_StandardFormat 兼容 Claude Desktop / trae / workbuddy 的
// 标准 mcpServers 对象格式（本地 MCP 配置的行业惯例）。
func TestParseServersJSON_StandardFormat(t *testing.T) {
	cfgs, err := ParseServersJSON([]byte(`{
	  "mcpServers": {
	    "journal-crawler": {
	      "command": "d:\\PyCharm\\projects\\Soup\\.venv\\Scripts\\python.exe",
	      "args": ["d:\\PyCharm\\projects\\Soup\\mcp_server.py"],
	      "cwd": "d:\\PyCharm\\projects\\Soup"
	    },
	    "weather": {
	      "url": "https://mcp.example.com/weather",
	      "transport": "http",
	      "headers": {"Authorization": "Bearer x"}
	    }
	  }
	}`))
	if err != nil {
		t.Fatalf("标准格式解析失败: %v", err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("应解析出 2 个 server，实际 %d", len(cfgs))
	}
	// key → name，command/args/cwd 透传
	if cfgs[0].Name != "journal-crawler" || cfgs[0].Command == "" ||
		len(cfgs[0].Args) != 1 || cfgs[0].Cwd != "d:\\PyCharm\\projects\\Soup" {
		t.Fatalf("stdio server 配置错误: %+v", cfgs[0])
	}
	if cfgs[1].Name != "weather" || cfgs[1].Transport != "http" || cfgs[1].URL == "" {
		t.Fatalf("http server 配置错误: %+v", cfgs[1])
	}

	// 裸对象（无 mcpServers 包装）同样兼容
	bare, err := ParseServersJSON([]byte(`{"github": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"]}}`))
	if err != nil {
		t.Fatalf("裸对象解析失败: %v", err)
	}
	if len(bare) != 1 || bare[0].Name != "github" || bare[0].Command != "npx" {
		t.Fatalf("裸对象配置错误: %+v", bare)
	}
}


func TestFormatCallResult(t *testing.T) {
	r := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "第一行"},
			&mcp.ImageContent{MIMEType: "image/png", Data: "ZmFrZQ=="},
			&mcp.TextContent{Text: "第二行"},
		},
	}
	out := formatCallResult(r)
	if !strings.Contains(out, "第一行") || !strings.Contains(out, "第二行") {
		t.Fatalf("应拼接全部文本内容: %q", out)
	}
	if !strings.Contains(out, "图片结果") {
		t.Fatalf("图片内容应保留元信息: %q", out)
	}

	errR := &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "boom"}}}
	if out := formatCallResult(errR); !strings.Contains(out, "返回错误") || !strings.Contains(out, "boom") {
		t.Fatalf("错误结果应带前缀并保留文本: %q", out)
	}

	if out := formatCallResult(&mcp.CallToolResult{}); out != "（MCP 工具返回空结果）" {
		t.Fatalf("空结果应有兜底文案: %q", out)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"GitHub":     "github",
		"my-server":  "my_server",
		"中文服务":       "mcp",
		"   ":        "mcp",
		"already_ok": "already_ok",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Fatalf("SanitizeName(%q) = %q，期望 %q", in, got, want)
		}
	}
}
