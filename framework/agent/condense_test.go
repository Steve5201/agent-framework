package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// TestRun_ContextCondensation 验证 WithMemoryCondenser 注入后，超窗历史会在
// Run 首轮被压成摘要并出现在请求中（context condensation 的集成路径）。
func TestRun_ContextCondensation(t *testing.T) {
	var condenseCalls int
	provider := &llm.MockProvider{Content: "好的"}
	var gotReq *llm.Request
	provider.ChatFn = func(req *llm.Request) (*llm.Response, error) {
		gotReq = req
		return &llm.Response{Content: "好的"}, nil
	}

	reg := tool.NewRegistry()
	if err := reg.Register(tool.CalculatorTool{}); err != nil {
		t.Fatalf("register calculator: %v", err)
	}
	cfg := schema.AgentConfig{
		Model:        "test-model",
		SystemPrompt: "你是测试助手",
		MaxRounds:    3,
		Memory:       schema.MemoryConfig{MaxMessages: 4},
	}
	// 6 条历史远超窗口（4 上限、1 保护），必然触发超窗压缩
	history := []schema.Message{
		{Role: schema.RoleUser, Content: "u1"},
		{Role: schema.RoleAssistant, Content: "a1"},
		{Role: schema.RoleUser, Content: "u2"},
		{Role: schema.RoleAssistant, Content: "a2"},
		{Role: schema.RoleUser, Content: "u3"},
		{Role: schema.RoleAssistant, Content: "a3"},
	}
	s, err := NewSession(cfg, provider, reg,
		WithInitialHistory(history),
		WithMemoryCondenser(func(_ context.Context, dropped []schema.Message) (string, error) {
			condenseCalls++
			return "早期的三问三答已完成", nil
		}),
	)
	if err != nil {
		t.Fatalf("NewSession error = %v", err)
	}
	if _, err := s.Run(context.Background(), "新问题"); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if condenseCalls == 0 {
		t.Fatal("超窗历史应触发摘要压缩")
	}
	if gotReq == nil {
		t.Fatal("未捕获请求")
	}
	hasSummary := false
	for _, m := range gotReq.Messages {
		if m.Role == schema.RoleSystem && strings.Contains(m.Content, "先前对话摘要") {
			hasSummary = true
		}
	}
	if !hasSummary {
		t.Errorf("请求应包含摘要 system 消息：%+v", gotReq.Messages)
	}
}

// TestRunStream_ContextCondensation 流式路径同样在超窗后触发压缩。
func TestRunStream_ContextCondensation(t *testing.T) {
	var condenseCalls int
	provider := &llm.MockProvider{}
	var gotReq *llm.Request
	provider.ChatStreamFn = func(req *llm.Request) (llm.Stream, error) {
		gotReq = req
		return llm.NewSliceStream([]llm.StreamEvent{{Content: "好的"}}), nil
	}

	reg := tool.NewRegistry()
	if err := reg.Register(tool.CalculatorTool{}); err != nil {
		t.Fatalf("register calculator: %v", err)
	}
	cfg := schema.AgentConfig{
		Model:        "test-model",
		SystemPrompt: "你是测试助手",
		MaxRounds:    3,
		Memory:       schema.MemoryConfig{MaxMessages: 4},
	}
	history := []schema.Message{
		{Role: schema.RoleUser, Content: "u1"},
		{Role: schema.RoleAssistant, Content: "a1"},
		{Role: schema.RoleUser, Content: "u2"},
		{Role: schema.RoleAssistant, Content: "a2"},
	}
	s, err := NewSession(cfg, provider, reg,
		WithInitialHistory(history),
		WithMemoryCondenser(func(_ context.Context, dropped []schema.Message) (string, error) {
			condenseCalls++
			return "早前已确认的事实", nil
		}),
	)
	if err != nil {
		t.Fatalf("NewSession error = %v", err)
	}
	if _, err := s.RunStream(context.Background(), "新问题", nil); err != nil {
		t.Fatalf("RunStream error = %v", err)
	}
	if condenseCalls == 0 {
		t.Fatal("超窗历史应触发摘要压缩")
	}
	if gotReq == nil {
		t.Fatal("未捕获请求")
	}
	hasSummary := false
	for _, m := range gotReq.Messages {
		if m.Role == schema.RoleSystem && strings.Contains(m.Content, "先前对话摘要") {
			hasSummary = true
		}
	}
	if !hasSummary {
		t.Errorf("请求应包含摘要 system 消息：%+v", gotReq.Messages)
	}
}

// TestRun_NoCondenser_PureWindow 未注入压缩器时行为与旧滑动窗口一致：
// 超窗直接丢弃最旧消息，请求中不出现摘要。
func TestRun_NoCondenser_PureWindow(t *testing.T) {
	provider := &llm.MockProvider{Content: "好的"}
	var gotReq *llm.Request
	provider.ChatFn = func(req *llm.Request) (*llm.Response, error) {
		gotReq = req
		return &llm.Response{Content: "好的"}, nil
	}
	reg := tool.NewRegistry()
	_ = reg.Register(tool.CalculatorTool{})
	cfg := schema.AgentConfig{
		Model:        "test-model",
		SystemPrompt: "你是测试助手",
		MaxRounds:    3,
		Memory:       schema.MemoryConfig{MaxMessages: 4},
	}
	history := []schema.Message{
		{Role: schema.RoleUser, Content: "u1"},
		{Role: schema.RoleAssistant, Content: "a1"},
		{Role: schema.RoleUser, Content: "u2"},
		{Role: schema.RoleAssistant, Content: "a2"},
		{Role: schema.RoleUser, Content: "u3"},
		{Role: schema.RoleAssistant, Content: "a3"},
	}
	s, err := NewSession(cfg, provider, reg, WithInitialHistory(history))
	if err != nil {
		t.Fatalf("NewSession error = %v", err)
	}
	if _, err := s.Run(context.Background(), "新问题"); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	for _, m := range gotReq.Messages {
		if m.Role == schema.RoleSystem && strings.Contains(m.Content, "先前对话摘要") {
			t.Fatalf("未注入压缩器不应出现摘要消息：%+v", m)
		}
	}
}

// TestRun_DrainCondenseNotices 压缩发生后，DrainCondenseNotices 返回记录
// （供宿主落库"哪个节点压缩过"），且消费式取空。
func TestRun_DrainCondenseNotices(t *testing.T) {
	provider := &llm.MockProvider{Content: "好的"}
	provider.ChatFn = func(req *llm.Request) (*llm.Response, error) {
		return &llm.Response{Content: "好的"}, nil
	}
	reg := tool.NewRegistry()
	_ = reg.Register(tool.CalculatorTool{})
	cfg := schema.AgentConfig{
		Model:        "test-model",
		SystemPrompt: "你是测试助手",
		MaxRounds:    3,
		Memory:       schema.MemoryConfig{MaxMessages: 4},
	}
	history := []schema.Message{
		{Role: schema.RoleUser, Content: "u1"},
		{Role: schema.RoleAssistant, Content: "a1"},
		{Role: schema.RoleUser, Content: "u2"},
		{Role: schema.RoleAssistant, Content: "a2"},
		{Role: schema.RoleUser, Content: "u3"},
		{Role: schema.RoleAssistant, Content: "a3"},
	}
	s, err := NewSession(cfg, provider, reg,
		WithInitialHistory(history),
		WithMemoryCondenser(func(_ context.Context, dropped []schema.Message) (string, error) {
			return "早期对话摘要", nil
		}),
	)
	if err != nil {
		t.Fatalf("NewSession error = %v", err)
	}
	if _, err := s.Run(context.Background(), "新问题"); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	notices := s.DrainCondenseNotices()
	if len(notices) == 0 {
		t.Fatal("压缩后应产生压缩记录")
	}
	if notices[0].Dropped <= 0 {
		t.Errorf("记录 Dropped 应 > 0，实际 = %d", notices[0].Dropped)
	}
	if notices[0].Count < 1 {
		t.Errorf("记录 Count 应从 1 起，实际 = %d", notices[0].Count)
	}
	// 消费式：再次读取应为空
	if again := s.DrainCondenseNotices(); len(again) != 0 {
		t.Errorf("消费后应清空，实际 = %+v", again)
	}
}
