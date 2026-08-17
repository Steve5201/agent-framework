package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// TestRepro_Session103Round2 回归线上 bug：历史含 assistant(tool_calls)+tool
// 配对时，追加用户消息后发出的请求必须保持配对完整。
//
// 场景来源：会话 103（新会话）第二轮请求本应合法；此前线上 400 的根因是
// 记忆窗口裁剪切开配对（见 memory.ShortTermMemory.Trim 配对保护修复）。
// 此用例固定"历史注入 + 新消息"路径，防止该路径重新引入孤立 tool。
func TestRepro_Session103Round2(t *testing.T) {
	// 线上会话 103 的实际历史（seq 1-4，含 reasoning 与 tool_call_id）
	history := []schema.Message{
		{Role: schema.RoleUser, Content: "你好"},
		{
			Role:      schema.RoleAssistant,
			Content:   "",
			Reasoning: "用户只是打招呼，我用问候工具回应一下",
			ToolCalls: []schema.ToolCall{{
				ID:        "call_00_HLVjzCywcFIGHEZugLmB0898",
				Name:      "mcp_greeting_mcp_greet",
				Arguments: json.RawMessage(`{"name":"同学"}`),
			}},
		},
		{
			Role:       schema.RoleTool,
			Content:    "你好，同学！欢迎使用智能体。\n{\"result\":\"你好，同学！欢迎使用智能体。\"}",
			ToolCallID: "call_00_HLVjzCywcFIGHEZugLmB0898",
		},
		{Role: schema.RoleAssistant, Content: "你好，同学！👋 我是智能助手"},
	}

	var gotReq *llm.Request
	provider := &llm.MockProvider{Events: []llm.StreamEvent{{Content: "好的"}}}
	provider.ChatStreamFn = func(req *llm.Request) (llm.Stream, error) {
		gotReq = req
		return (&llm.MockProvider{Events: []llm.StreamEvent{{Content: "好的"}}}).ChatStream(nil, nil)
	}

	reg := tool.NewRegistry()
	if err := reg.Register(tool.CalculatorTool{}); err != nil {
		t.Fatalf("register calculator: %v", err)
	}
	cfg := schema.AgentConfig{
		Model:        "test-model",
		SystemPrompt: "你是测试助手",
		MaxRounds:    5,
		Memory:       schema.MemoryConfig{MaxMessages: 20},
	}
	s, err := NewSession(cfg, provider, reg, WithInitialHistory(history))
	if err != nil {
		t.Fatalf("NewSession error = %v", err)
	}

	if _, err := s.RunStream(context.Background(), "你好，请问第二个问题", nil); err != nil {
		t.Fatalf("RunStream error = %v", err)
	}
	if gotReq == nil {
		t.Fatal("未捕获请求")
	}

	t.Logf("请求消息数: %d", len(gotReq.Messages))
	for i, m := range gotReq.Messages {
		t.Logf("  [%d] role=%s tool_call_id=%q tool_calls=%d content=%q",
			i, m.Role, m.ToolCallID, len(m.ToolCalls), truncate(m.Content))
	}

	if err := validateNoOrphanTool(gotReq.Messages); err != nil {
		t.Errorf("请求包含孤立 tool: %v", err)
	}
}

// TestBuildRequest_DropsOrphanTool 回归线上 400 的直接诱因：记忆窗口若残留
// 孤立 tool（裁剪 bug 产物），buildRequest 必须兜底过滤，请求不得包含无主
// 工具结果。
func TestBuildRequest_DropsOrphanTool(t *testing.T) {
	// 模拟裁剪 bug 的产物：assistant(tool_calls) 被丢、tool 残留（孤立）
	history := []schema.Message{
		{Role: schema.RoleUser, Content: "查一下"},
		{Role: schema.RoleTool, Content: "孤立结果", ToolCallID: "call_orphan"}, // 无配对 assistant
		{Role: schema.RoleAssistant, Content: "之前的结果已给出"},
	}

	var gotReq *llm.Request
	provider := &llm.MockProvider{Events: []llm.StreamEvent{{Content: "好的"}}}
	provider.ChatStreamFn = func(req *llm.Request) (llm.Stream, error) {
		gotReq = req
		return (&llm.MockProvider{Events: []llm.StreamEvent{{Content: "好的"}}}).ChatStream(nil, nil)
	}

	reg := tool.NewRegistry()
	if err := reg.Register(tool.CalculatorTool{}); err != nil {
		t.Fatalf("register calculator: %v", err)
	}
	cfg := schema.AgentConfig{
		Model:        "test-model",
		SystemPrompt: "你是测试助手",
		MaxRounds:    5,
		Memory:       schema.MemoryConfig{MaxMessages: 20},
	}
	s, err := NewSession(cfg, provider, reg, WithInitialHistory(history))
	if err != nil {
		t.Fatalf("NewSession error = %v", err)
	}

	if _, err := s.RunStream(context.Background(), "继续", nil); err != nil {
		t.Fatalf("RunStream error = %v", err)
	}
	if gotReq == nil {
		t.Fatal("未捕获请求")
	}

	// 孤立 tool 必须被过滤掉
	for _, m := range gotReq.Messages {
		if m.Role == schema.RoleTool {
			t.Fatalf("请求仍包含 tool 消息（应为已过滤）：%+v", m)
		}
	}
	if err := validateNoOrphanTool(gotReq.Messages); err != nil {
		t.Errorf("请求仍包含孤立 tool: %v", err)
	}
}

// TestSanitizeMessages_KeepsValidPairings 正常配对不应被误过滤。
func TestSanitizeMessages_KeepsValidPairings(t *testing.T) {
	msgs := []schema.Message{
		{Role: schema.RoleUser, Content: "u"},
		{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "t"}}},
		{Role: schema.RoleTool, Content: "ok", ToolCallID: "c1"},
		{Role: schema.RoleAssistant, Content: "final"},
	}
	got := sanitizeMessages(msgs)
	if len(got) != len(msgs) {
		t.Errorf("合法配对被误过滤：len=%d want=%d", len(got), len(msgs))
	}
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// validateNoOrphanTool 校验消息序列没有"无主工具结果"。
// 语义：每条 role=tool 必须匹配其之前某条 assistant(tool_calls) 的声明。
func validateNoOrphanTool(msgs []schema.Message) error {
	declared := map[string]bool{}
	for _, m := range msgs {
		if m.Role == schema.RoleAssistant {
			for _, tc := range m.ToolCalls {
				declared[tc.ID] = true
			}
		}
		if m.Role == schema.RoleTool && !declared[m.ToolCallID] {
			return &orphanToolErr{toolCallID: m.ToolCallID}
		}
	}
	return nil
}

type orphanToolErr struct{ toolCallID string }

func (e *orphanToolErr) Error() string {
	return "tool 消息 " + e.toolCallID + " 之前没有配对的 assistant(tool_calls)"
}
