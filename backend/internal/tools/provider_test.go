package tools_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Steve5201/agent-backend/internal/tools"
	"github.com/Steve5201/agent-backend/internal/tools/builtin"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// stubTool 模拟未来 MCP / Skill 提供者的工具（独立名称，避免与内置重名）。
type stubTool struct{}

func (stubTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{Name: "fake_tool", Description: "测试桩工具", Permission: schema.PermissionL0Pure}
}
func (stubTool) Execute(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil }

// fakeProvider 模拟未来接入的 MCP / Skill 提供者。
type fakeProvider struct{}

func (fakeProvider) Name() string { return "fake:skill" }
func (fakeProvider) Tools() []tool.Tool {
	return []tool.Tool{stubTool{}}
}

func TestRegisterProviders(t *testing.T) {
	reg := tool.NewRegistry()
	if err := tools.RegisterProviders(reg, builtin.Builtin{}, fakeProvider{}); err != nil {
		t.Fatalf("RegisterProviders 失败: %v", err)
	}

	for _, want := range []string{"calculator", "web_search", "file_ops", "code_executor"} {
		if !slices.Contains(reg.Names(), want) {
			t.Errorf("内置工具 %q 未注册", want)
		}
	}
	// 第三方提供者（MCP/Skill 预留）同样生效。
	if !slices.Contains(reg.Names(), "fake_tool") {
		t.Error("fakeProvider 的工具应注册成功")
	}
}

func TestRegisterProviders_DuplicateRejected(t *testing.T) {
	reg := tool.NewRegistry()
	if err := tools.RegisterProviders(reg, builtin.Builtin{}); err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}
	err := tools.RegisterProviders(reg, builtin.Builtin{})
	if err == nil || !strings.Contains(err.Error(), "已注册") {
		t.Fatalf("重复注册应报错并标明来源，实际 err=%v", err)
	}
}

func TestBuiltinProvider_ToolSchemasForLLM(t *testing.T) {
	// 工具描述清晰度：Schema 供 DeepSeek Function Calling 使用，
	// 每个工具的 Description 必须非空且含明确参数说明。
	for _, tl := range (builtin.Builtin{}).Tools() {
		s := tl.Schema()
		if s.Name == "" || s.Description == "" {
			t.Errorf("工具 %q 缺名称或描述", s.Name)
		}
		if len(s.Parameters) == 0 {
			t.Errorf("工具 %q 缺参数 Schema", s.Name)
		}
	}
}
