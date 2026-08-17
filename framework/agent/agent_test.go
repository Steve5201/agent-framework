package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// newTestSession 构造测试会话：MockProvider + 已注册 calculator 的注册表。
func newTestSession(t *testing.T, provider llm.Provider, opts ...Option) *Session {
	t.Helper()
	reg := tool.NewRegistry()
	if err := reg.Register(tool.CalculatorTool{}); err != nil {
		t.Fatalf("register calculator: %v", err)
	}
	cfg := schema.AgentConfig{
		Model:        "test-model",
		SystemPrompt: "你是测试助手",
		MaxRounds:    5,
		Memory:       schema.MemoryConfig{MaxMessages: 10},
	}
	s, err := NewSession(cfg, provider, reg, opts...)
	if err != nil {
		t.Fatalf("NewSession error = %v", err)
	}
	return s
}

// TestRun_NoTool 验证单轮对话（无工具调用）。
func TestRun_NoTool(t *testing.T) {
	provider := &llm.MockProvider{Content: "你好，我是AI"}
	s := newTestSession(t, provider)

	res, err := s.Run(context.Background(), "在吗")
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if res.Content != "你好，我是AI" {
		t.Errorf("Content = %q", res.Content)
	}
	if res.Rounds != 1 {
		t.Errorf("Rounds = %d, want 1", res.Rounds)
	}
	if res.ToolCalls != 0 {
		t.Errorf("ToolCalls = %d, want 0", res.ToolCalls)
	}
}

// TestRun_WithTool 验证"先调工具、再给答案"的完整循环。
func TestRun_WithTool(t *testing.T) {
	provider := &llm.MockProvider{}
	calls := 0
	provider.ChatFn = func(_ *llm.Request) (*llm.Response, error) {
		calls++
		if calls == 1 {
			return &llm.Response{
				ToolCalls: []schema.ToolCall{{
					ID:        "call_1",
					Name:      "calculator",
					Arguments: json.RawMessage(`{"a":12,"b":13,"op":"*"}`),
				}},
			}, nil
		}
		return &llm.Response{Content: "12*13=156"}, nil
	}

	s := newTestSession(t, provider)
	res, err := s.Run(context.Background(), "12*13等于几")
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if res.Content != "12*13=156" {
		t.Errorf("Content = %q", res.Content)
	}
	if res.Rounds != 2 {
		t.Errorf("Rounds = %d, want 2", res.Rounds)
	}
	if res.ToolCalls != 1 {
		t.Errorf("ToolCalls = %d, want 1", res.ToolCalls)
	}

	// 历史中应包含 role=tool 的结果消息（协议配对要求）
	history := s.History()
	found := false
	for _, m := range history {
		if m.Role == schema.RoleTool && strings.Contains(m.Content, "156") {
			found = true
		}
	}
	if !found {
		t.Error("历史中应包含工具结果消息（含 156）")
	}
}

// TestRun_MaxRounds 验证无限工具调用被轮数保护拦截。
func TestRun_MaxRounds(t *testing.T) {
	provider := &llm.MockProvider{}
	// 每次都说要调工具（永不收敛）
	provider.ChatFn = func(_ *llm.Request) (*llm.Response, error) {
		return &llm.Response{
			ToolCalls: []schema.ToolCall{{
				ID: "loop", Name: "calculator",
				Arguments: json.RawMessage(`{"a":1,"b":1,"op":"+"}`),
			}},
		}, nil
	}

	s := newTestSession(t, provider) // MaxRounds=5
	if _, err := s.Run(context.Background(), "一直调工具"); err == nil {
		t.Fatal("达到最大轮数应报错")
	}
}

// TestRun_EmptyReply 验证模型返回空内容且未调工具时视为错误（防污染历史）。
func TestRun_EmptyReply(t *testing.T) {
	provider := &llm.MockProvider{}
	provider.ChatFn = func(_ *llm.Request) (*llm.Response, error) {
		return &llm.Response{Content: ""}, nil // 只有思考、无内容
	}
	s := newTestSession(t, provider)
	if _, err := s.Run(context.Background(), "你好"); err == nil {
		t.Fatal("空回复应报错，避免空 assistant 消息落库污染会话")
	}
}

// TestRunStream_EmptyReply 验证流式路径同样拦截空回复。
func TestRunStream_EmptyReply(t *testing.T) {
	provider := &llm.MockProvider{
		// 只出思考、无内容、无工具调用
		Events: []llm.StreamEvent{{Reasoning: "思考一下"}},
	}
	s := newTestSession(t, provider)
	var got string
	if _, err := s.RunStream(context.Background(), "你好", func(d string) { got += d }); err == nil {
		t.Fatal("流式空回复应报错")
	}
	if got != "" {
		t.Errorf("不应收到任何内容增量，got %q", got)
	}
}

// TestRun_ApprovalRejected 验证需确认工具在无确认回调时被拒绝。
func TestRun_ApprovalRejected(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(writeFileTool{})

	provider := &llm.MockProvider{}
	calls := 0
	provider.ChatFn = func(_ *llm.Request) (*llm.Response, error) {
		calls++
		if calls == 1 {
			return &llm.Response{
				ToolCalls: []schema.ToolCall{{ID: "c1", Name: "write_file", Arguments: json.RawMessage(`{}`)}},
			}, nil
		}
		return &llm.Response{Content: "文件未写入"}, nil
	}

	cfg := schema.AgentConfig{Model: "m", MaxRounds: 5}
	s, err := NewSession(cfg, provider, reg) // 无 WithApprovalFunc
	if err != nil {
		t.Fatalf("NewSession error = %v", err)
	}

	res, err := s.Run(context.Background(), "写文件")
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if res.Content != "文件未写入" {
		t.Errorf("Content = %q", res.Content)
	}

	// 历史中的 tool 消息应说明"被拒绝"
	history := s.History()
	for _, m := range history {
		if m.Role == schema.RoleTool {
			if !strings.Contains(m.Content, "需要用户确认") {
				t.Errorf("拒绝原因应回填给模型，实际: %q", m.Content)
			}
		}
	}
}

// TestRun_ApprovalAllowed 验证提供确认回调后可执行 L3 工具。
func TestRun_ApprovalAllowed(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(writeFileTool{})

	provider := &llm.MockProvider{}
	calls := 0
	provider.ChatFn = func(_ *llm.Request) (*llm.Response, error) {
		calls++
		if calls == 1 {
			return &llm.Response{
				ToolCalls: []schema.ToolCall{{ID: "c1", Name: "write_file", Arguments: json.RawMessage(`{}`)}},
			}, nil
		}
		return &llm.Response{Content: "已写入"}, nil
	}

	cfg := schema.AgentConfig{Model: "m", MaxRounds: 5}
	s, err := NewSession(cfg, provider, reg, WithApprovalFunc(func(schema.ToolCall) bool { return true }))
	if err != nil {
		t.Fatalf("NewSession error = %v", err)
	}

	res, err := s.Run(context.Background(), "写文件")
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if res.Content != "已写入" {
		t.Errorf("Content = %q", res.Content)
	}
}

// TestRunStream_NoTool 验证流式文本输出。
func TestRunStream_NoTool(t *testing.T) {
	provider := &llm.MockProvider{
		Events: []llm.StreamEvent{
			{Content: "你"},
			{Content: "好"},
		},
	}
	s := newTestSession(t, provider)

	var got string
	res, err := s.RunStream(context.Background(), "hi", func(part string) { got += part })
	if err != nil {
		t.Fatalf("RunStream error = %v", err)
	}
	if got != "你好" {
		t.Errorf("流式增量 = %q, want 你好", got)
	}
	if res.Content != "你好" {
		t.Errorf("Content = %q", res.Content)
	}
}

// errStream 测试专用流：事件序列出完后返回注入的错误（模拟流中途断连/超时）。
type errStream struct {
	events []llm.StreamEvent
	idx    int
	err    error
}

func (s *errStream) Next() (llm.StreamEvent, error) {
	if s.idx >= len(s.events) {
		return llm.StreamEvent{}, s.err
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *errStream) Close() error { return nil }

// TestRunStream_PartialContentOnError 流式中断（超时/断连/用户打断）时，
// 已拼装的部分内容必须先提交到短期记忆（P4-L 修复）：上层 persistPartialOnError
// 靠差分落库，否则"打断后已生成内容不入库、不计入后续上下文"。
func TestRunStream_PartialContentOnError(t *testing.T) {
	provider := &llm.MockProvider{}
	provider.ChatStreamFn = func(_ *llm.Request) (llm.Stream, error) {
		return &errStream{
			events: []llm.StreamEvent{{Content: "部分"}, {Content: "内容"}},
			err:    context.DeadlineExceeded,
		}, nil
	}
	s := newTestSession(t, provider)
	if _, err := s.RunStream(context.Background(), "hi", nil); err == nil {
		t.Fatal("流式中断应返回错误")
	}
	// 历史中应包含已拼装的部分内容（assistant 消息），供上层差分落库。
	found := false
	for _, m := range s.History() {
		if m.Role == schema.RoleAssistant && strings.Contains(m.Content, "部分内容") {
			found = true
		}
	}
	if !found {
		t.Fatalf("流式错误后部分内容应提交到历史: %+v", s.History())
	}
}

// TestRunStream_EmptyStreamError 流式在产生任何文本前即中断：不应提交空
// assistant 消息（空内容会被上层健康过滤剔除，提交无意义还污染差分）。
func TestRunStream_EmptyStreamError(t *testing.T) {
	provider := &llm.MockProvider{}
	provider.ChatStreamFn = func(_ *llm.Request) (llm.Stream, error) {
		return &errStream{err: context.Canceled}, nil
	}
	s := newTestSession(t, provider)
	if _, err := s.RunStream(context.Background(), "hi", nil); err == nil {
		t.Fatal("流式中断应返回错误")
	}
	for _, m := range s.History() {
		if m.Role == schema.RoleAssistant {
			t.Fatalf("未产生文本不应提交 assistant 消息: %+v", m)
		}
	}
}

// TestRunStream_WithTool 验证流式工具调用（参数分片拼装 + 执行）。
func TestRunStream_WithTool(t *testing.T) {
	provider := &llm.MockProvider{}
	calls := 0
	provider.ChatStreamFn = func(_ *llm.Request) (llm.Stream, error) {
		calls++
		if calls == 1 {
			// 参数分三片下发：id/name 首片，arguments 分片
			return (&llm.MockProvider{
				Events: []llm.StreamEvent{
					{ToolCalls: []llm.ToolCallDelta{{Index: 0, ID: "call_1", Name: "calculator", Arguments: `{"a":12,`}}},
					{ToolCalls: []llm.ToolCallDelta{{Index: 0, Arguments: `"b":13,`}}},
					{ToolCalls: []llm.ToolCallDelta{{Index: 0, Arguments: `"op":"*"}`}}},
				},
			}).ChatStream(nil, nil)
		}
		return (&llm.MockProvider{Events: []llm.StreamEvent{{Content: "结果=156"}}}).ChatStream(nil, nil)
	}

	s := newTestSession(t, provider)
	res, err := s.RunStream(context.Background(), "算一下", nil)
	if err != nil {
		t.Fatalf("RunStream error = %v", err)
	}
	if res.Content != "结果=156" {
		t.Errorf("Content = %q", res.Content)
	}
	if res.ToolCalls != 1 {
		t.Errorf("ToolCalls = %d, want 1", res.ToolCalls)
	}
}

// TestRun_Reasoning 验证非流式调用捕获思考内容（DeepSeek reasoning_content）。
func TestRun_Reasoning(t *testing.T) {
	provider := &llm.MockProvider{}
	provider.ChatFn = func(_ *llm.Request) (*llm.Response, error) {
		return &llm.Response{
			Content:   "最终回答",
			Reasoning: "先想一步，再答",
		}, nil
	}
	s := newTestSession(t, provider)

	if _, err := s.Run(context.Background(), "问题"); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	// 历史中 assistant 消息应带思考内容
	var assistant schema.Message
	for _, m := range s.History() {
		if m.Role == schema.RoleAssistant && m.Content == "最终回答" {
			assistant = m
		}
	}
	if assistant.Reasoning != "先想一步，再答" {
		t.Errorf("Reasoning = %q, want 先想一步，再答", assistant.Reasoning)
	}
}

// TestRunStream_ReasoningObserver 验证流式场景：
// 思考增量实时通知 + 累积进 assistant 消息 + 工具调用/返回事件顺序。
func TestRunStream_ReasoningObserver(t *testing.T) {
	provider := &llm.MockProvider{}
	calls := 0
	provider.ChatStreamFn = func(_ *llm.Request) (llm.Stream, error) {
		calls++
		if calls == 1 {
			// 第一轮：思考（准备调工具）→ 工具调用指令
			return (&llm.MockProvider{Events: []llm.StreamEvent{
				{Reasoning: "我需要计算"},
				{Reasoning: " 12*13"},
				{ToolCalls: []llm.ToolCallDelta{{Index: 0, ID: "call_1", Name: "calculator", Arguments: `{"a":12,"b":13,"op":"*"}`}}},
			}}).ChatStream(nil, nil)
		}
		// 第二轮：继续思考 → 最终回答
		return (&llm.MockProvider{Events: []llm.StreamEvent{
			{Reasoning: "结果是 156"},
			{Content: "答案=156"},
		}}).ChatStream(nil, nil)
	}

	s := newTestSession(t, provider)

	var gotContent, gotReasoning string
	var gotCalls []string
	gotResults := []string{}
	res, err := s.RunStreamWithObserver(context.Background(), "12*13等于几", &StreamObserver{
		OnContent:   func(d string) { gotContent += d },
		OnReasoning: func(d string) { gotReasoning += d },
		OnToolCall:  func(c schema.ToolCall) { gotCalls = append(gotCalls, c.Name) },
		OnToolResult: func(c schema.ToolCall, r *schema.ToolResult, e error) {
			gotResults = append(gotResults, r.Content)
		},
	})
	if err != nil {
		t.Fatalf("RunStreamWithObserver error = %v", err)
	}
	if res.Content != "答案=156" {
		t.Errorf("Content = %q", res.Content)
	}
	if gotContent != "答案=156" {
		t.Errorf("OnContent 增量 = %q", gotContent)
	}
	// 两轮思考内容按到达顺序拼接
	if gotReasoning != "我需要计算 12*13结果是 156" {
		t.Errorf("OnReasoning 增量 = %q", gotReasoning)
	}
	if len(gotCalls) != 1 || gotCalls[0] != "calculator" {
		t.Errorf("OnToolCall = %v, want [calculator]", gotCalls)
	}
	if len(gotResults) != 1 || gotResults[0] != "156" {
		t.Errorf("OnToolResult = %v, want [156]", gotResults)
	}

	// 历史中两轮 assistant 消息都应带思考内容（含工具轮——回传必需）
	var reasoning []string
	for _, m := range s.History() {
		if m.Role == schema.RoleAssistant && m.Reasoning != "" {
			reasoning = append(reasoning, m.Reasoning)
		}
	}
	if len(reasoning) != 2 {
		t.Fatalf("带思考内容的 assistant 消息数 = %d, want 2 (%v)", len(reasoning), reasoning)
	}
}

// TestRun_ThinkingConfig 思考配置透传：AgentConfig.Thinking →
// llm.Request.Thinking（关闭思考 + 指定推理强度）。
func TestRun_ThinkingConfig(t *testing.T) {
	var gotReq *llm.Request
	provider := &llm.MockProvider{}
	provider.ChatFn = func(req *llm.Request) (*llm.Response, error) {
		gotReq = req
		return &llm.Response{Content: "ok"}, nil
	}
	reg := tool.NewRegistry()
	if err := reg.Register(tool.CalculatorTool{}); err != nil {
		t.Fatalf("register calculator: %v", err)
	}
	s, err := NewSession(schema.AgentConfig{
		Model:     "m",
		MaxRounds: 5,
		Thinking:  &schema.ThinkingConfig{Enabled: false, ReasoningEffort: "high"},
	}, provider, reg)
	if err != nil {
		t.Fatalf("NewSession error = %v", err)
	}
	if _, err := s.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if gotReq.Thinking == nil || gotReq.Thinking.Enabled || gotReq.Thinking.ReasoningEffort != "high" {
		t.Errorf("Thinking 未按配置透传: %+v", gotReq.Thinking)
	}
}

// TestRun_ThinkingConfigZeroValue 零值 Thinking（未配置）不干预：Request.Thinking 为 nil。
func TestRun_ThinkingConfigZeroValue(t *testing.T) {
	var gotReq *llm.Request
	provider := &llm.MockProvider{}
	provider.ChatFn = func(req *llm.Request) (*llm.Response, error) {
		gotReq = req
		return &llm.Response{Content: "ok"}, nil
	}
	s := newTestSession(t, provider)
	if _, err := s.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if gotReq.Thinking != nil {
		t.Errorf("未配置思考模式应 Thinking=nil, got %+v", gotReq.Thinking)
	}
}

// writeFileTool 测试专用 L3 危险工具。
type writeFileTool struct{}

func (writeFileTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:       "write_file",
		Permission: schema.PermissionL3Dangerous,
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (writeFileTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "wrote", nil
}
