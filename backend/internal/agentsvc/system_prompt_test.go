// system_prompt_test.go —— 按智能体基础提示词（auth agents.system_prompt）：
// gRPC 层固化进会话 config 快照；装配优先用按智能体提示词、且渲染协议与
// 保护区规范仍追加在其后；用户更新配置不能覆盖（管理员级字段，服务端继承）。
package agentsvc

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	agentv1 "github.com/Steve5201/agent-backend/internal/proto/agent/v1"
	"github.com/Steve5201/agent-framework/llm"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
)

// mustSessionID 把 gRPC 会话 ID（string）转成 service 层 int64。
func mustSessionID(t *testing.T, id string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatalf("会话 ID 解析失败 %q: %v", id, err)
	}
	return n
}

// newSysPromptTestSvc 构建捕获 LLM 请求 SystemPrompt 的测试服务。
func newSysPromptTestSvc(t *testing.T, repo Repository) (*Service, *string, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var gotSystem string
	svc, err := newTestService(repo, &llm.MockProvider{
		ChatFn: func(req *llm.Request) (*llm.Response, error) {
			// system 指令位于消息序列首条
			var sp string
			if len(req.Messages) > 0 {
				sp = req.Messages[0].Content
			}
			mu.Lock()
			gotSystem = sp
			mu.Unlock()
			return &llm.Response{Content: "你好", Usage: llm.Usage{TotalTokens: 5}}, nil
		},
	})
	if err != nil {
		t.Fatalf("newTestService: %v", err)
	}
	return svc, &gotSystem, &mu
}

// TestGrpc_CreateSession_SystemPrompt 固化 + 装配：按智能体提示词优先生效，
// 渲染协议与保护区规范仍作为常量追加在基础提示词之后（不被配置覆盖）。
func TestGrpc_CreateSession_SystemPrompt(t *testing.T) {
	repo := newFakeRepo()
	svc, gotSystem, mu := newSysPromptTestSvc(t, repo)
	gs := NewGrpcServer(svc, zap.NewNop())
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(metadataUserID, "1"))

	resp, err := gs.CreateSession(ctx, &agentv1.CreateSessionRequest{
		AgentId:      "tutor",
		Title:        "考研",
		SystemPrompt: "你是考研规划导师，专注考研数学。",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess, err := svc.GetSession(ctx, 1, mustSessionID(t, resp.Session.GetId()))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Config.SystemPrompt != "你是考研规划导师，专注考研数学。" {
		t.Fatalf("system_prompt 应固化进 config 快照, got %q", sess.Config.SystemPrompt)
	}

	if _, err := svc.Chat(context.Background(), 1, sess.ID, "你好"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasPrefix(*gotSystem, "你是考研规划导师，专注考研数学。") {
		t.Fatalf("装配应使用按智能体提示词, got prefix=%q", *gotSystem)
	}
	if !strings.Contains(*gotSystem, "内容渲染协议") {
		t.Fatalf("渲染协议应追加在基础提示词之后, got=%q", *gotSystem)
	}
	if !strings.Contains(*gotSystem, "工作区保护区") {
		t.Fatalf("保护区规范应追加在基础提示词之后, got=%q", *gotSystem)
	}
}

// TestGrpc_CreateSession_SystemPromptFallback 无按智能体提示词（空）时回退
// 实例全局提示词（newTestService 的 "你是测试助手。"），渲染协议等仍追加。
func TestGrpc_CreateSession_SystemPromptFallback(t *testing.T) {
	repo := newFakeRepo()
	svc, gotSystem, mu := newSysPromptTestSvc(t, repo)
	gs := NewGrpcServer(svc, zap.NewNop())
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(metadataUserID, "1"))

	resp, err := gs.CreateSession(ctx, &agentv1.CreateSessionRequest{AgentId: "tutor", Title: "普通"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess, _ := svc.GetSession(ctx, 1, mustSessionID(t, resp.Session.GetId()))
	if sess.Config.SystemPrompt != "" {
		t.Fatalf("未提供 system_prompt 时快照应为空, got %q", sess.Config.SystemPrompt)
	}

	if _, err := svc.Chat(context.Background(), 1, sess.ID, "你好"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasPrefix(*gotSystem, "你是测试助手。") {
		t.Fatalf("应回退实例全局提示词, got prefix=%q", *gotSystem)
	}
	if !strings.Contains(*gotSystem, "内容渲染协议") {
		t.Fatalf("渲染协议应追加, got=%q", *gotSystem)
	}
}

// TestGrpc_CreateSession_SystemPromptTooLong 超长按智能体提示词拒绝。
func TestGrpc_CreateSession_SystemPromptTooLong(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	gs := NewGrpcServer(svc, zap.NewNop())
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(metadataUserID, "1"))

	_, err := gs.CreateSession(ctx, &agentv1.CreateSessionRequest{
		AgentId:      "tutor",
		SystemPrompt: strings.Repeat("a", maxSystemPromptRunes+1),
	})
	if apperr.CodeOf(err) != apperr.CodeInvalidArgument {
		t.Fatalf("超长 system_prompt 应拒绝, got %v", err)
	}
}

// TestService_UpdateSessionConfig_SystemPromptInherited 用户更新配置不能覆盖
// 按智能体提示词（管理员级字段服务端继承快照原值），用户可配字段仍生效。
func TestService_UpdateSessionConfig_SystemPromptInherited(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := newTestService(repo, &llm.MockProvider{})
	gs := NewGrpcServer(svc, zap.NewNop())
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(metadataUserID, "1"))

	resp, err := gs.CreateSession(ctx, &agentv1.CreateSessionRequest{
		AgentId:      "tutor",
		Title:        "考研",
		SystemPrompt: "导师提示词",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id := mustSessionID(t, resp.Session.GetId())

	// 直接构造 SessionConfig 模拟"内部传值"（proto 不暴露该字段，用户请求实际
	// 无法携带）——验证服务端继承逻辑兜底：管理员级字段保留快照原值。
	_, err = svc.UpdateSessionConfig(context.Background(), 1, id, SessionConfig{Model: "m2", SystemPrompt: "用户伪造"})
	if err != nil {
		t.Fatalf("UpdateSessionConfig: %v", err)
	}
	sess, err := svc.GetSession(context.Background(), 1, id)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Config.SystemPrompt != "导师提示词" {
		t.Fatalf("用户不能覆盖 system_prompt, got %q", sess.Config.SystemPrompt)
	}
	if sess.Config.Model != "m2" {
		t.Fatalf("用户可配字段应生效, got %q", sess.Config.Model)
	}
}
