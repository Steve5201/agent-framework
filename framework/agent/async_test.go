package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
)

// externalTool 测试专用"外部代理"工具：声明 External=true，
// 实际执行必须由 AsyncRunner 派发到宿主外部，Execute 不应被直接调用。
type externalTool struct{}

func (externalTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:       "local_git",
		External:   true, // 声明需外部执行
		Permission: schema.PermissionL2Write,
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (externalTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "不应被直接调用", nil // 若被调用说明框架分流失败
}

// funcAsyncRunner 用闭包实现 AsyncRunner，方便测试注入行为。
type funcAsyncRunner struct {
	dispatch func(ctx context.Context, call schema.ToolCall) error
}

func (f *funcAsyncRunner) Dispatch(ctx context.Context, call schema.ToolCall) error {
	if f.dispatch == nil {
		return nil
	}
	return f.dispatch(ctx, call)
}

// newExternalSession 构造带外部工具 local_git 的会话。
func newExternalSession(t *testing.T, provider llm.Provider, opts ...Option) *Session {
	t.Helper()
	reg := tool.NewRegistry()
	if err := reg.Register(externalTool{}); err != nil {
		t.Fatalf("register external tool: %v", err)
	}
	cfg := schema.AgentConfig{Model: "test-model", MaxRounds: 5}
	s, err := NewSession(cfg, provider, reg, opts...)
	if err != nil {
		t.Fatalf("NewSession error = %v", err)
	}
	return s
}

// TestRun_ExternalTool 验证外部代理工具的完整闭环：
// 派发 → 挂起 → 宿主异步回填 → 结果回填历史 → 模型继续。
func TestRun_ExternalTool(t *testing.T) {
	provider := &llm.MockProvider{}
	calls := 0
	provider.ChatFn = func(_ *llm.Request) (*llm.Response, error) {
		calls++
		if calls == 1 {
			return &llm.Response{ToolCalls: []schema.ToolCall{{
				ID: "ext_1", Name: "local_git", Arguments: json.RawMessage(`{}`),
			}}}, nil
		}
		return &llm.Response{Content: "已在桌面执行 git status"}, nil
	}

	var sess *Session
	dispatchCalls := 0
	runner := &funcAsyncRunner{dispatch: func(_ context.Context, call schema.ToolCall) error {
		dispatchCalls++
		// 模拟外部异步执行：延迟后由宿主回填
		go func() {
			time.Sleep(20 * time.Millisecond)
			_ = sess.SubmitToolResult(call.ID, &schema.ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Content:    "外部执行完成: 工作区干净",
			})
		}()
		return nil
	}}

	sess = newExternalSession(t, provider, WithAsyncRunner(runner))

	res, err := sess.Run(context.Background(), "在本地执行 git status")
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if res.Content != "已在桌面执行 git status" {
		t.Errorf("Content = %q", res.Content)
	}
	if res.ToolCalls != 1 {
		t.Errorf("ToolCalls = %d, want 1", res.ToolCalls)
	}
	if dispatchCalls != 1 {
		t.Errorf("Dispatch 调用次数 = %d, want 1", dispatchCalls)
	}

	// 外部结果应回填进历史（role=tool），供模型下一轮看到
	history := sess.History()
	found := false
	for _, m := range history {
		if m.Role == schema.RoleTool && strings.Contains(m.Content, "工作区干净") {
			found = true
		}
	}
	if !found {
		t.Error("外部执行结果应回填到历史（role=tool）")
	}

	// 挂起表应已清空（无泄漏）
	sess.pendingMu.Lock()
	pendingCount := len(sess.pending)
	sess.pendingMu.Unlock()
	if pendingCount != 0 {
		t.Errorf("挂起表应清空, got %d 项", pendingCount)
	}
}

// TestRun_ExternalTool_NoRunner 验证未配置 AsyncRunner 时，
// 外部工具调用被拒绝，拒绝原因回填给模型。
func TestRun_ExternalTool_NoRunner(t *testing.T) {
	provider := &llm.MockProvider{}
	calls := 0
	provider.ChatFn = func(_ *llm.Request) (*llm.Response, error) {
		calls++
		if calls == 1 {
			return &llm.Response{ToolCalls: []schema.ToolCall{{
				ID: "ext_1", Name: "local_git", Arguments: json.RawMessage(`{}`),
			}}}, nil
		}
		return &llm.Response{Content: "无法执行，请安装桌面客户端"}, nil
	}

	s := newExternalSession(t, provider) // 未配置 AsyncRunner

	res, err := s.Run(context.Background(), "在本地执行 git status")
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if res.Content != "无法执行，请安装桌面客户端" {
		t.Errorf("Content = %q", res.Content)
	}

	// 拒绝原因应回填为 role=tool 消息
	history := s.History()
	for _, m := range history {
		if m.Role == schema.RoleTool {
			if !strings.Contains(m.Content, "需外部执行") {
				t.Errorf("拒绝原因应回填给模型, got %q", m.Content)
			}
		}
	}
}

// TestRun_ExternalTool_Timeout 验证外部执行超时保护：
// 宿主迟迟不回填，ctx 到期后挂起被解除，超时原因回填给模型。
func TestRun_ExternalTool_Timeout(t *testing.T) {
	provider := &llm.MockProvider{}
	calls := 0
	provider.ChatFn = func(_ *llm.Request) (*llm.Response, error) {
		calls++
		if calls == 1 {
			return &llm.Response{ToolCalls: []schema.ToolCall{{
				ID: "ext_1", Name: "local_git", Arguments: json.RawMessage(`{}`),
			}}}, nil
		}
		return &llm.Response{Content: "抱歉，外部执行超时了"}, nil
	}

	// 派发后永不回填（模拟桌面客户端掉线）
	runner := &funcAsyncRunner{dispatch: func(context.Context, schema.ToolCall) error { return nil }}
	s := newExternalSession(t, provider, WithAsyncRunner(runner))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	res, err := s.Run(ctx, "在本地执行 git status")
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if res.Content != "抱歉，外部执行超时了" {
		t.Errorf("Content = %q", res.Content)
	}

	// 超时原因应回填为 role=tool 消息
	history := s.History()
	for _, m := range history {
		if m.Role == schema.RoleTool {
			if !strings.Contains(m.Content, "超时/中断") {
				t.Errorf("超时原因应回填给模型, got %q", m.Content)
			}
		}
	}
}

// TestSubmitToolResult_UnknownCallID 验证对未挂起的 call_id 回填返回错误。
func TestSubmitToolResult_UnknownCallID(t *testing.T) {
	s := newExternalSession(t, &llm.MockProvider{Content: "ok"})
	if err := s.SubmitToolResult("nonexistent", &schema.ToolResult{Content: "x"}); err == nil {
		t.Fatal("对未知 call_id 回填应报错")
	}
}

// TestSubmitToolResult_NilResult 验证 nil 结果被拒绝（防御误用）。
func TestSubmitToolResult_NilResult(t *testing.T) {
	s := newExternalSession(t, &llm.MockProvider{Content: "ok"})
	if err := s.SubmitToolResult("ext_1", nil); err == nil {
		t.Fatal("nil 结果应报错")
	}
}
