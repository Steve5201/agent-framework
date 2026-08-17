// defaults_test.go —— 智能体域默认会话配置解析缝单测。
//
// 覆盖：ParseDefaultsJSON（合法/空/空白/非法）、ApplyDefaults 合并语义
// （只填充显式未设置字段、空数组作为显式语义保留）、IsEmpty 判定。
package agentsvc

import (
	"strings"
	"testing"
)

func TestParseDefaultsJSON(t *testing.T) {
	t.Run("完整配置解析", func(t *testing.T) {
		d, err := ParseDefaultsJSON([]byte(`{
			"enabled_resources": ["search", "skill_my_skill"],
			"kb_ids": ["kb_1", "kb_2"],
			"mcp_servers": ["github"],
			"thinking": {"enabled": false}
		}`))
		if err != nil {
			t.Fatalf("parse err: %v", err)
		}
		if len(d.EnabledResources) != 2 || d.EnabledResources[0] != "search" {
			t.Fatalf("EnabledResources = %v", d.EnabledResources)
		}
		if len(d.KBIDs) != 2 || d.KBIDs[1] != "kb_2" {
			t.Fatalf("KBIDs = %v", d.KBIDs)
		}
		if len(d.MCPServers) != 1 || d.MCPServers[0] != "github" {
			t.Fatalf("MCPServers = %v", d.MCPServers)
		}
		if d.Thinking == nil || d.Thinking.Enabled {
			t.Fatalf("Thinking = %+v", d.Thinking)
		}
	})

	t.Run("空内容 = 零值非错误", func(t *testing.T) {
		d, err := ParseDefaultsJSON(nil)
		if err != nil {
			t.Fatalf("parse err: %v", err)
		}
		if !d.IsEmpty() {
			t.Fatalf("want empty, got %+v", d)
		}
		d, err = ParseDefaultsJSON([]byte("  \n\t "))
		if err != nil {
			t.Fatalf("parse err: %v", err)
		}
		if !d.IsEmpty() {
			t.Fatalf("want empty, got %+v", d)
		}
	})

	t.Run("非法 JSON 返回错误", func(t *testing.T) {
		if _, err := ParseDefaultsJSON([]byte("{not-json")); err == nil {
			t.Fatal("want error, got nil")
		}
	})

	t.Run("部分字段 = 其余零值", func(t *testing.T) {
		d, err := ParseDefaultsJSON([]byte(`{"kb_ids": []}`))
		if err != nil {
			t.Fatalf("parse err: %v", err)
		}
		if d.KBIDs == nil || len(d.KBIDs) != 0 {
			t.Fatalf("KBIDs want empty non-nil, got %v", d.KBIDs)
		}
		if d.EnabledResources != nil || d.MCPServers != nil || d.Thinking != nil {
			t.Fatalf("unexpected non-zero fields: %+v", d)
		}
	})
}

func TestApplyDefaults(t *testing.T) {
	t.Run("填充显式未设置字段", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{}, AgentDefaults{
			EnabledResources: []string{"search"},
			KBIDs:            []string{"kb_1"},
		})
		if len(cfg.EnabledResources) != 1 || cfg.EnabledResources[0] != "search" {
			t.Fatalf("EnabledResources = %v", cfg.EnabledResources)
		}
		if len(cfg.KBIDs) != 1 || cfg.KBIDs[0] != "kb_1" {
			t.Fatalf("KBIDs = %v", cfg.KBIDs)
		}
	})

	t.Run("显式配置优先（默认不覆盖）", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{
			EnabledResources: []string{"search"},
			KBIDs:            []string{"kb_explicit"},
		}, AgentDefaults{
			EnabledResources: []string{"web_search"},
			KBIDs:            []string{"kb_default"},
		})
		if len(cfg.EnabledResources) != 1 || cfg.EnabledResources[0] != "search" {
			t.Fatalf("explicit should win, got %v", cfg.EnabledResources)
		}
		if len(cfg.KBIDs) != 1 || cfg.KBIDs[0] != "kb_explicit" {
			t.Fatalf("explicit should win, got %v", cfg.KBIDs)
		}
	})

	t.Run("默认空数组 = 显式语义保留", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{}, AgentDefaults{KBIDs: []string{}})
		if cfg.KBIDs == nil || len(cfg.KBIDs) != 0 {
			t.Fatalf("want empty non-nil KBIDs, got %v", cfg.KBIDs)
		}
	})

	t.Run("KBIDsSet=true 且 KBIDs=nil = 显式清空，不回填默认", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{KBIDsSet: true}, AgentDefaults{KBIDs: []string{"kb_1"}})
		if cfg.KBIDs != nil {
			t.Fatalf("显式清空不应回填默认知识库: %+v", cfg.KBIDs)
		}
	})

	t.Run("KBIDsSet=true 且 KBIDs 与默认一致 = 显式锁定保留", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{KBIDs: []string{"kb_1"}, KBIDsSet: true}, AgentDefaults{KBIDs: []string{"kb_1"}})
		if len(cfg.KBIDs) != 1 || cfg.KBIDs[0] != "kb_1" {
			t.Fatalf("显式锁定应保留: %+v", cfg.KBIDs)
		}
	})

	t.Run("Thinking 已设但 effort 空串 → 回填默认强度", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{Thinking: &ThinkingConfig{Enabled: true}},
			AgentDefaults{Thinking: &ThinkingConfig{Enabled: true, ReasoningEffort: "high"}})
		if cfg.Thinking == nil || cfg.Thinking.ReasoningEffort != "high" {
			t.Fatalf("应回填默认强度 high: %+v", cfg.Thinking)
		}
	})

	t.Run("Thinking 已设且 effort 非空 → 保留显式强度", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{Thinking: &ThinkingConfig{Enabled: true, ReasoningEffort: "low"}},
			AgentDefaults{Thinking: &ThinkingConfig{Enabled: true, ReasoningEffort: "high"}})
		if cfg.Thinking == nil || cfg.Thinking.ReasoningEffort != "low" {
			t.Fatalf("显式强度应保留 low: %+v", cfg.Thinking)
		}
	})

	t.Run("不修改入参对象（值拷贝）", func(t *testing.T) {
		src := SessionConfig{EnabledResources: []string{"search"}}
		_ = ApplyDefaults(src, AgentDefaults{MCPServers: []string{"github"}})
		if len(src.EnabledResources) != 1 || src.MCPServers != nil {
			t.Fatalf("source mutated: %+v", src)
		}
	})

	t.Run("默认显式全不选（set=true，空数组被 omitempty 丢弃成 nil）→ 仍生效", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{}, AgentDefaults{EnabledResourcesSet: true})
		if !cfg.EnabledResourcesSet {
			t.Fatalf("默认显式清空应透传 set 标记: %+v", cfg)
		}
	})

	t.Run("默认 set=true 且带列表 → 列表与标记都透传", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{}, AgentDefaults{
			EnabledResources:    []string{"search"},
			EnabledResourcesSet: true,
		})
		if !cfg.EnabledResourcesSet || len(cfg.EnabledResources) != 1 || cfg.EnabledResources[0] != "search" {
			t.Fatalf("set=true 应同时透传列表与标记: %+v", cfg)
		}
	})

	t.Run("会话显式 set=true 时默认不覆盖", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{EnabledResourcesSet: true},
			AgentDefaults{EnabledResources: []string{"search"}})
		if cfg.EnabledResources != nil {
			t.Fatalf("会话显式设置不应被默认覆盖: %+v", cfg)
		}
	})

	t.Run("MCP 默认列表填充", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{}, AgentDefaults{MCPServers: []string{"github"}})
		if len(cfg.MCPServers) != 1 || cfg.MCPServers[0] != "github" {
			t.Fatalf("MCPServers = %v", cfg.MCPServers)
		}
	})

	t.Run("MCP 默认全不选（set=true 空数组）→ 显式透传", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{}, AgentDefaults{MCPServersSet: true})
		if !cfg.MCPServersSet || cfg.MCPServers != nil {
			t.Fatalf("MCP 默认全不选应透传 set 标记: %+v", cfg)
		}
	})

	t.Run("会话显式 MCP set=true 时默认不覆盖", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{MCPServersSet: true},
			AgentDefaults{MCPServers: []string{"github"}})
		if cfg.MCPServers != nil {
			t.Fatalf("会话显式 MCP 选择不应被默认覆盖: %+v", cfg)
		}
	})

	t.Run("能力/技能类别 set 标记继承（默认未设时不注入）", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{}, AgentDefaults{EnabledCapabilitiesSet: true, EnabledSkillsSet: true})
		if !cfg.EnabledCapabilitiesSet || !cfg.EnabledSkillsSet {
			t.Fatalf("默认类别 set 标记应透传: %+v", cfg)
		}
	})
	t.Run("能力/技能类别 set 标记不注入（默认未设时保持 false）", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{}, AgentDefaults{})
		if cfg.EnabledCapabilitiesSet || cfg.EnabledSkillsSet {
			t.Fatalf("默认无类别标记时不应注入: %+v", cfg)
		}
	})
	t.Run("会话显式类别 set=true 时默认不覆盖", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{EnabledCapabilitiesSet: true},
			AgentDefaults{EnabledCapabilitiesSet: false})
		if !cfg.EnabledCapabilitiesSet {
			t.Fatalf("会话显式能力类别标记应保留: %+v", cfg)
		}
	})
	t.Run("类别 set 标记随资源列表透传（不依赖旧联合标记）", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{}, AgentDefaults{
			EnabledResources:       []string{"search"},
			EnabledCapabilitiesSet: true,
		})
		if !cfg.EnabledCapabilitiesSet || len(cfg.EnabledResources) != 1 {
			t.Fatalf("能力白名单 + 标记应同时透传: %+v", cfg)
		}
	})

	t.Run("管理员级字段：0 取默认值，非 0 保留显式", func(t *testing.T) {
		cfg := ApplyDefaults(SessionConfig{}, AgentDefaults{
			MaxRounds:         16,
			MaxMessages:       40,
			MaxThinkingRounds: 12,
		})
		if cfg.MaxRounds != 16 || cfg.MaxMessages != 40 || cfg.MaxThinkingRounds != 12 {
			t.Fatalf("管理员级字段应透传: %+v", cfg)
		}
		cfg = ApplyDefaults(SessionConfig{MaxRounds: 20}, AgentDefaults{MaxRounds: 16})
		if cfg.MaxRounds != 20 {
			t.Fatalf("会话显式值应优先: %d", cfg.MaxRounds)
		}
		cfg = ApplyDefaults(SessionConfig{}, AgentDefaults{})
		if cfg.MaxRounds != 0 || cfg.MaxMessages != 0 || cfg.MaxThinkingRounds != 0 {
			t.Fatalf("默认 0 应保持 0（回退实例默认）: %+v", cfg)
		}
	})
}

// TestAgentDefaultsIsEmpty 零值/各字段占位对 IsEmpty 判定的影响。
func TestAgentDefaultsIsEmpty(t *testing.T) {
	if !(AgentDefaults{}).IsEmpty() {
		t.Fatal("zero value should be empty")
	}
	if (AgentDefaults{Thinking: &ThinkingConfig{Enabled: true}}).IsEmpty() {
		t.Fatal("thinking default should not be empty")
	}
	if (AgentDefaults{MCPServers: []string{}}).IsEmpty() {
		t.Fatal("explicit empty slice is a default and should not be empty")
	}
	if (AgentDefaults{MCPServersSet: true}).IsEmpty() {
		t.Fatal("explicit MCP empty (set=true) is a default and should not be empty")
	}
	if (AgentDefaults{KBIDsSet: true}).IsEmpty() {
		t.Fatal("explicit KB empty (set=true) is a default and should not be empty")
	}
	if (AgentDefaults{MaxRounds: 16}).IsEmpty() {
		t.Fatal("admin max_rounds default should not be empty")
	}
	if (AgentDefaults{MaxMessages: 40}).IsEmpty() {
		t.Fatal("admin max_messages default should not be empty")
	}
	if (AgentDefaults{MaxThinkingRounds: 12}).IsEmpty() {
		t.Fatal("admin max_thinking_rounds default should not be empty")
	}
}

// TestDefaultsFileName 落盘文件名常量与 domainview 引用一致（防改名脱钩）。
func TestDefaultsFileName(t *testing.T) {
	if strings.TrimSpace(DefaultsFileName) != "agent_defaults.json" {
		t.Fatalf("DefaultsFileName = %q", DefaultsFileName)
	}
}
