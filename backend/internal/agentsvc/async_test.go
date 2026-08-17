// async_test.go —— 阶段3·外部工具挂起/回填单测。
//
// 覆盖：dispatchRunner 登记挂起 → Service.SubmitToolResult 唤醒会话 →
// 结果回填历史并落库；未挂起回填报 NotFound；跨用户回填被拒（防枚举）。
package agentsvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"github.com/Steve5201/agent-framework/tool"
	"go.uber.org/zap"
)

// externalTestTool 测试专用外部代理工具（External=true，Execute 不应被直接调用）。
type externalTestTool struct{}

func (externalTestTool) Schema() schema.ToolSchema {
	return schema.ToolSchema{
		Name:       "local_git",
		External:   true,
		Permission: schema.PermissionL2Write,
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (externalTestTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "不应被直接调用", nil
}

// newExternalService 构造带外部工具 local_git 注册表的 Service。
func newExternalService(t *testing.T, repo Repository, p llm.Provider) (*Service, error) {
	t.Helper()
	reg := tool.NewRegistry()
	if err := reg.Register(externalTestTool{}); err != nil {
		t.Fatalf("register external tool: %v", err)
	}
	return NewService(Config{
		Repo:         repo,
		Provider:     p,
		Registry:     reg,
		Log:          zap.NewNop(),
		Model:        "test-model",
		SystemPrompt: "你是测试助手。",
		MaxRounds:    5,
		MaxMessages:  20,
	})
}

// TestService_ExternalTool_BackfillLoop 验证完整闭环：
// Chat 挂起（等待外部执行）→ SubmitToolResult 回填 → 会话恢复 →
// 结果进入历史并落库。
func TestService_ExternalTool_BackfillLoop(t *testing.T) {
	repo := newFakeRepo()
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
	svc, err := newExternalService(t, repo, provider)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	s, err := svc.CreateSession(context.Background(), 1, "", "外部工具")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	done := make(chan *ChatResult, 1)
	errCh := make(chan error, 1)
	go func() {
		out, err := svc.Chat(context.Background(), 1, s.ID, "在本地执行 git status")
		if err != nil {
			errCh <- err
			return
		}
		done <- out
	}()

	// 等待框架把外部调用登记进挂起表。
	deadline := time.Now().Add(3 * time.Second)
	for {
		svc.pendingMu.Lock()
		_, ok := svc.pending[s.ID]["ext_1"]
		svc.pendingMu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("外部工具调用未登记挂起表（Dispatch 未触发）")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 模拟桌面客户端回填结果。
	if err := svc.SubmitToolResult(context.Background(), 1, s.ID, "ext_1", &schema.ToolResult{Content: "工作区干净，无改动"}); err != nil {
		t.Fatalf("SubmitToolResult: %v", err)
	}

	select {
	case out := <-done:
		if out.Message == nil || out.Message.Content != "已在桌面执行 git status" {
			t.Errorf("Content = %+v", out.Message)
		}
		if out.ToolCalls != 1 {
			t.Errorf("ToolCalls = %d, want 1", out.ToolCalls)
		}
	case err := <-errCh:
		t.Fatalf("Chat error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("回填后 Chat 未恢复（挂起未唤醒）")
	}

	// 挂起表应已清空。
	svc.pendingMu.Lock()
	pendingCount := len(svc.pending[s.ID])
	svc.pendingMu.Unlock()
	if pendingCount != 0 {
		t.Errorf("回填后挂起表应清空, got %d 项", pendingCount)
	}

	// 结果应落库为 role=tool 消息。
	msgs, _ := repo.ListMessages(context.Background(), s.ID)
	var toolContent string
	for _, m := range msgs {
		if m.Role == string(schema.RoleTool) && m.ToolCallID == "ext_1" {
			toolContent = m.Content
		}
	}
	if !strings.Contains(toolContent, "工作区干净") {
		t.Errorf("工具结果未落库, got %q", toolContent)
	}
}

// TestService_SubmitToolResult_NotPending 验证未挂起的调用回填报 NotFound。
func TestService_SubmitToolResult_NotPending(t *testing.T) {
	svc, _ := newTestService(newFakeRepo(), &llm.MockProvider{Content: "ok"})
	err := svc.SubmitToolResult(context.Background(), 1, 999, "nope", &schema.ToolResult{Content: "x"})
	if apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("未挂起回填应 NotFound, got %v", err)
	}
}

// TestService_SubmitToolResult_WrongOwner 验证跨用户回填被拒（防枚举）。
func TestService_SubmitToolResult_WrongOwner(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{Content: "ok"})
	s, _ := svc.CreateSession(context.Background(), 1, "", "x")

	ag, err := svc.newAgentWithConfig(context.Background(), s.ID, nil)
	if err != nil {
		t.Fatalf("newAgentWithConfig: %v", err)
	}
	// 用户 1 的会话挂起了一个外部调用。
	svc.registerPending(1, s.ID, ag, schema.ToolCall{ID: "ext_1", Name: "local_git"})

	// 用户 2 尝试回填 → 属主不符，报 NotFound。
	err = svc.SubmitToolResult(context.Background(), 2, s.ID, "ext_1", &schema.ToolResult{Content: "x"})
	if apperr.CodeOf(err) != apperr.CodeNotFound {
		t.Errorf("跨用户回填应 NotFound, got %v", err)
	}
}
