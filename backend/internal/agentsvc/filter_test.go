// filter_test.go —— 会话级工具过滤单测（kb 语义反转 + MCP 会话级限定）。
//
// 覆盖：
//   - mcpServerOf 工具名解析；
//   - filterSessionTools：KBIDs 空 → 移除 kb_search；MCPServers 非空 → 按 server 过滤；
//   - validateConfig：mcp_servers 有效性校验（可用性检测）。
package agentsvc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
	"go.uber.org/zap"
)

// stubTool 极简工具桩：仅用于验证过滤逻辑，不关心执行行为。
type stubTool struct{ name string }

func (t stubTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{Name: t.name, Description: "stub " + t.name, Permission: schema.PermissionL1Read}
}

func (t stubTool) Execute(_ context.Context, _ json.RawMessage) (string, error) { return "stub", nil }

// stubRegistry 构造含能力工具（web_search/file_ops/...）、kb_search 与
// 多个 mcp_ 工具的测试注册表（能力工具齐全，保证资源校验可通过）。
func stubRegistry() *tool.Registry {
	reg := tool.NewRegistry()
	for _, name := range []string{
		"web_search",             // 搜索能力
		"file_ops",               // 文件读写能力
		"code_executor",          // 代码执行能力
		"calculator",             // 计算能力
		"get_current_time",       // 时间能力
		kbSearchToolName,         // 知识库检索
		"mcp_github_get_issue",   // github server
		"mcp_github_list_issues", // github server
		"mcp_slack_post_message", // slack server
		"skill_数据分析_abcd1234",    // 技能工具
	} {
		if err := reg.Register(stubTool{name: name}); err != nil {
			panic(err)
		}
	}
	return reg
}

func TestMcpServerOf(t *testing.T) {
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{name: "mcp_github_get_issue", want: "github", ok: true},
		{name: "mcp_slack_post_message", want: "slack", ok: true},
		{name: "web_search", ok: false},
		{name: "mcp_github", ok: false}, // 无工具名部分
	}
	for _, c := range cases {
		srv, ok := mcpServerOf(c.name)
		if ok != c.ok || srv != c.want {
			t.Fatalf("mcpServerOf(%q) = (%q, %v), want (%q, %v)", c.name, srv, ok, c.want, c.ok)
		}
	}
}

func TestFilterSessionTools_KBEmptyRemovesKBSearch(t *testing.T) {
	reg := stubRegistry()
	svc := &Service{log: zap.NewNop()}

	// 未勾选知识库 → kb_search 不装配，其余工具保留。
	out := svc.filterSessionTools(reg, SessionConfig{KBIDs: nil, MCPServers: nil})
	if _, err := out.Get(kbSearchToolName); err == nil {
		t.Fatal("KBIDs 为空时 kb_search 不应装配")
	}
	if _, err := out.Get("web_search"); err != nil {
		t.Fatalf("内置工具应保留: %v", err)
	}
	if _, err := out.Get("mcp_github_get_issue"); err != nil {
		t.Fatalf("MCP 工具应保留: %v", err)
	}
}

func TestFilterSessionTools_KBSetKeepsKBSearch(t *testing.T) {
	reg := stubRegistry()
	svc := &Service{log: zap.NewNop()}

	// 勾选了知识库 + 未限定 MCP → 无过滤需求，返回原注册表（零拷贝）。
	cfg := SessionConfig{KBIDs: []string{"kb1"}}
	if got := svc.filterSessionTools(reg, cfg); got != reg {
		t.Fatal("KBIDs 非空且 MCPServers 空时应返回原注册表（零拷贝）")
	}
	if _, err := reg.Get(kbSearchToolName); err != nil {
		t.Fatalf("kb_search 应保留: %v", err)
	}
}

func TestFilterSessionTools_MCPServerFilter(t *testing.T) {
	reg := stubRegistry()
	svc := &Service{log: zap.NewNop()}

	cfg := SessionConfig{KBIDs: []string{"kb1"}, MCPServers: []string{"github"}}
	out := svc.filterSessionTools(reg, cfg)

	if _, err := out.Get(kbSearchToolName); err != nil {
		t.Fatalf("kb_search 应保留: %v", err)
	}
	if _, err := out.Get("mcp_github_get_issue"); err != nil {
		t.Fatalf("选中 server 的工具应保留: %v", err)
	}
	if _, err := out.Get("mcp_slack_post_message"); err == nil {
		t.Fatal("未选中的 slack server 工具应被过滤")
	}
	if _, err := out.Get("web_search"); err != nil {
		t.Fatalf("非 MCP 工具不受 mcp_servers 影响: %v", err)
	}
}

func TestFilterSessionTools_MCPRawNameSanitized(t *testing.T) {
	reg := stubRegistry()
	svc := &Service{log: zap.NewNop()}

	// 配置传原始名（非净化名）也应匹配（SanitizeName("GitHub")="github"）。
	cfg := SessionConfig{MCPServers: []string{"GitHub"}}
	out := svc.filterSessionTools(reg, cfg)
	if _, err := out.Get("mcp_github_get_issue"); err != nil {
		t.Fatalf("原始名应被净化后匹配: %v", err)
	}
	if _, err := out.Get("mcp_slack_post_message"); err == nil {
		t.Fatal("slack 工具不应装配")
	}
}

func TestValidateConfig_MCPServers(t *testing.T) {
	svc, err := NewService(Config{
		Repo:         newFakeRepo(),
		Provider:     &llm.MockProvider{},
		Registry:     stubRegistry(),
		Log:          zap.NewNop(),
		Model:        "test-model",
		SystemPrompt: "你是测试助手。",
		MaxRounds:    8,
		MaxMessages:  20,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	t.Run("已启用的 server 合法", func(t *testing.T) {
		if err := svc.validateConfig(SessionConfig{MCPServers: []string{"github"}}); err != nil {
			t.Fatalf("合法 mcp_servers 应通过: %v", err)
		}
	})
	t.Run("未启用的 server 拒绝", func(t *testing.T) {
		err := svc.validateConfig(SessionConfig{MCPServers: []string{"github", "nonexist"}})
		if err == nil {
			t.Fatal("未启用的 server 应被拒绝")
		}
	})
	t.Run("空列表合法（全部生效）", func(t *testing.T) {
		if err := svc.validateConfig(SessionConfig{MCPServers: nil}); err != nil {
			t.Fatalf("空 mcp_servers 应通过: %v", err)
		}
	})
}

func TestCleanConfig_MCPServersDedup(t *testing.T) {
	got := cleanConfig(SessionConfig{MCPServers: []string{"github", "", "github", " slack "}})
	if len(got.MCPServers) != 2 || got.MCPServers[0] != "github" || got.MCPServers[1] != "slack" {
		t.Fatalf("mcp_servers 应 trim+去重: %v", got.MCPServers)
	}
}

func TestSessionToolRegistry_KBOverrideCapabilityWhitelist(t *testing.T) {
	// P4-K 回归：kb_search 不属于任何 capability，能力白名单生效时若不被
	// buildResourceTools 并入，用户勾选知识库（KBIDs 非空）也看不到 kb_search
	//（实测复现：会话 kb_ids 非空但模型只见 web_search/calculator）。
	svc, err := NewService(Config{
		Repo:         newFakeRepo(),
		Provider:     &llm.MockProvider{},
		Registry:     stubRegistry(),
		Log:          zap.NewNop(),
		Model:        "test-model",
		SystemPrompt: "你是测试助手。",
		MaxRounds:    8,
		MaxMessages:  20,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	t.Run("能力白名单生效且勾选知识库 → kb_search 保留", func(t *testing.T) {
		cfg := SessionConfig{
			KBIDs:                  []string{"kb1"},
			EnabledResources:       []string{"search"}, // 只勾了"搜索"能力
			EnabledCapabilitiesSet: true,               // 能力白名单生效
			EnabledSkillsSet:       true,
		}
		out := svc.sessionToolRegistry(cfg)
		if _, err := out.Get(kbSearchToolName); err != nil {
			t.Fatalf("勾选知识库后 kb_search 应装配（即使能力白名单不含 kb）: %v", err)
		}
		if _, err := out.Get("web_search"); err != nil {
			t.Fatalf("白名单内能力工具应保留: %v", err)
		}
		if _, err := out.Get("file_ops"); err == nil {
			t.Fatal("能力白名单外工具（file_ops）应被裁剪")
		}
	})

	t.Run("能力白名单生效但未勾选知识库 → kb_search 不装配", func(t *testing.T) {
		cfg := SessionConfig{
			EnabledResources:       []string{"search"},
			EnabledCapabilitiesSet: true,
			EnabledSkillsSet:       true,
		}
		out := svc.sessionToolRegistry(cfg)
		if _, err := out.Get(kbSearchToolName); err == nil {
			t.Fatal("未勾选知识库时 kb_search 不应装配")
		}
	})

	t.Run("能力类别未设置（全量）且勾选知识库 → kb_search 保留", func(t *testing.T) {
		cfg := SessionConfig{KBIDs: []string{"kb1"}}
		out := svc.sessionToolRegistry(cfg)
		if _, err := out.Get(kbSearchToolName); err != nil {
			t.Fatalf("勾选知识库后 kb_search 应装配: %v", err)
		}
	})
}
