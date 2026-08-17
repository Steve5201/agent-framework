package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/Steve5201/agent-framework/schema"
)

// defaultExternalTimeout 外部代理工具挂起等待的默认超时（可用
// AgentConfig.ExternalExecTimeout 覆盖）。桌面确认+执行一般几秒内完成，
// 120s 给足富余同时保证掉线场景不无限挂起。
const defaultExternalTimeout = 120 * time.Second

// AsyncRunner 外部异步执行器：宿主（agent-service / 桌面客户端）实现它，
// 接收"需外部执行"的工具调用（ToolSchema.External == true），在外部环境
// 执行后通过 Session.SubmitToolResult 回填结果，agent 循环随即恢复。
//
// 典型场景（阶段3·本地工具代理）：
//   - Tauri 桌面端的本地工具（shell/git/文件对话框）：框架发出调用指令，
//     宿主经 WebSocket 下发到桌面进程，用户确认后执行，结果回填；
//   - 浏览器端的受限工具：框架发出调用指令，前端展示确认 UI，
//     用户确认后把结果（或"已在外部执行"的说明）回填。
//
// 与同步工具的区别：同步工具在进程内直接执行并返回；外部工具的执行
// 发生在框架进程之外（用户桌面/浏览器），因此必须"挂起等待"。
type AsyncRunner interface {
	// Dispatch 派发一次外部工具调用。实现应尽快返回（不阻塞 agent 循环）：
	// 真正的执行与回填在外部异步进行，完成后调用 session.SubmitToolResult。
	// ctx 携带会话上下文（如 user_id），宿主据此路由到正确的会话。
	Dispatch(ctx context.Context, call schema.ToolCall) error
}

// pendingMu 与 pending 挂起表定义在 Session（agent.go）中，本文件实现其方法。

// SubmitToolResult 回填外部工具调用的执行结果（宿主在外部执行完成后调用）。
//
// callID 对应 ToolCall.ID；无匹配挂起项时返回错误（可能已回填或已超时清除）。
// 线程安全：可在任意 goroutine 调用（如 Tauri 回调、WS 收包线程）。
func (s *Session) SubmitToolResult(callID string, result *schema.ToolResult) error {
	if result == nil {
		return fmt.Errorf("agent: SubmitToolResult 结果不能为 nil")
	}
	s.pendingMu.Lock()
	ch, ok := s.pending[callID]
	if ok {
		delete(s.pending, callID)
	}
	s.pendingMu.Unlock()
	if !ok {
		return fmt.Errorf("agent: 无挂起的外部工具调用 %q", callID)
	}
	ch <- result
	return nil
}

// execExternal 执行需外部代理的工具调用：派发给 AsyncRunner 后挂起，
// 等待 SubmitToolResult 回填或 ctx 结束（超时/取消）。
//
// 先注册挂起项再派发，避免"外部先回填、后注册"的竞态丢结果——
// 即使宿主在 Dispatch 返回前就完成了执行，SubmitToolResult 也能送达。
func (s *Session) execExternal(ctx context.Context, call schema.ToolCall) (*schema.ToolResult, error) {
	if s.asyncRunner == nil {
		return nil, fmt.Errorf("agent: 工具 %q 需外部执行，但会话未配置 AsyncRunner", call.Name)
	}

	ch := make(chan *schema.ToolResult, 1)
	s.pendingMu.Lock()
	if _, dup := s.pending[call.ID]; dup {
		s.pendingMu.Unlock()
		return nil, fmt.Errorf("agent: 工具调用 %q 已挂起，重复执行", call.ID)
	}
	s.pending[call.ID] = ch
	s.pendingMu.Unlock()

	// 无论成功失败，退出时清理挂起项（防 map 泄漏）
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, call.ID)
		s.pendingMu.Unlock()
	}()

	if err := s.asyncRunner.Dispatch(ctx, call); err != nil {
		return nil, fmt.Errorf("agent: 外部执行派发失败: %w", err)
	}

	// 等待回填：受"外部执行超时"保护，宿主掉线/用户不响应时不会无限挂起。
	timeout := s.config.ExternalExecTimeout
	if timeout <= 0 {
		timeout = defaultExternalTimeout
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case result := <-ch:
		return result, nil
	case <-wctx.Done():
		return nil, fmt.Errorf("agent: 等待外部执行结果超时/中断（call_id=%s, %v）", call.ID, wctx.Err())
	}
}
