// async.go —— 阶段3·外部工具挂起/唤醒（本地工具代理）。
//
// 场景：桌面客户端注册了"本地工具"（External=true，如 shell/git/文件对话框），
// 其执行发生在用户桌面进程（框架进程触达不到）。本文件实现：
//
//  1. dispatchRunner：agent.AsyncRunner 实现——框架检测到外部工具调用时
//     回调 Dispatch，把"会话 + 调用"登记进挂起表后立即返回；
//  2. Service.SubmitToolResult：桌面客户端完成本地执行后，经 gRPC
//     SubmitToolResult → gateway 上行 API → 本方法唤醒挂起会话；
//  3. 挂起表清理：StreamChat 结束时按 session 清空，防泄漏。
//
// 安全：挂起表条目记录属主 user_id，回填时做属主校验（防跨用户唤醒）。
package agentsvc

import (
	"context"
	"fmt"
	"sync/atomic"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-framework/agent"
	"github.com/Steve5201/agent-framework/schema"
	"go.uber.org/zap"
)

// pendingToolCall 一条挂起中的外部工具调用。
type pendingToolCall struct {
	ag     *agent.Session // 等待中的会话（框架 execExternal 挂起处）
	userID int64          // 属主（回填时校验）
	call   schema.ToolCall
}

// dispatchRunner agent.AsyncRunner 实现：把外部工具调用登记进 Service 挂起表。
// 真正的执行与回填在外部异步进行，本方法只做登记（不阻塞框架循环）。
type dispatchRunner struct {
	svc       *Service
	userID    int64
	sessionID int64
	ag        atomic.Pointer[agent.Session] // 会话创建完成后回填
}

// Dispatch 实现 agent.AsyncRunner。
func (r *dispatchRunner) Dispatch(_ context.Context, call schema.ToolCall) error {
	ag := r.ag.Load()
	if ag == nil {
		return fmt.Errorf("agentsvc: 会话未就绪，无法派发外部工具 %q", call.Name)
	}
	r.svc.registerPending(r.userID, r.sessionID, ag, call)
	return nil
}

// registerPending 登记一条外部工具调用到挂起表（Dispatch 内部调用）。
func (s *Service) registerPending(userID, sessionID int64, ag *agent.Session, call schema.ToolCall) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if s.pending[sessionID] == nil {
		s.pending[sessionID] = make(map[string]*pendingToolCall)
	}
	s.pending[sessionID][call.ID] = &pendingToolCall{ag: ag, userID: userID, call: call}
	s.log.Info("external tool dispatched, waiting for backfill",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sessionID),
		zap.String("tool", call.Name),
		zap.String("tool_call_id", call.ID),
	)
}

// clearSessionPending 清理某会话的全部挂起项（StreamChat 结束/超时时调用）。
func (s *Service) clearSessionPending(sessionID int64) {
	s.pendingMu.Lock()
	delete(s.pending, sessionID)
	s.pendingMu.Unlock()
}

// SubmitToolResult 回填外部工具执行结果（gRPC SubmitToolResult → 业务层）。
//
// 流程：挂起表定位 → 属主校验 → session.SubmitToolResult 唤醒框架挂起处 →
// 框架把结果回填记忆并继续推理循环。
//
// 错误语义：无挂起项（已超时/已回填/会话已结束）返回 CodeNotFound，
// 前端据此提示"该工具调用已过期"；属主不符返回 CodeNotFound（防枚举）。
func (s *Service) SubmitToolResult(ctx context.Context, userID, sessionID int64, callID string, result *schema.ToolResult) error {
	if callID == "" {
		return apperr.New(apperr.CodeInvalidArgument, "缺少工具调用标识")
	}
	if result == nil {
		return apperr.New(apperr.CodeInvalidArgument, "回填结果不能为空")
	}

	s.pendingMu.Lock()
	entry, ok := s.pending[sessionID][callID]
	s.pendingMu.Unlock()
	if !ok || entry == nil {
		return apperr.New(apperr.CodeNotFound, "该工具调用未挂起或已超时，请确认会话正在进行")
	}
	if entry.userID != userID {
		// 非本人：统一 NotFound，避免通过报错差异探测他人会话。
		return apperr.New(apperr.CodeNotFound, "会话不存在")
	}

	result.ToolCallID = callID
	result.Name = entry.call.Name
	if err := entry.ag.SubmitToolResult(callID, result); err != nil {
		s.log.Warn("external tool result backfill failed",
			zap.Int64("user_id", userID),
			zap.Int64("session_id", sessionID),
			zap.String("tool_call_id", callID),
			zap.Error(err),
		)
		return apperr.Wrap(apperr.CodeFailedPrecondition, "回填失败", err)
	}
	// 投递成功后移除挂起项：重复回填将走 NotFound 分支（幂等提示"已过期"）。
	s.pendingMu.Lock()
	if cur, ok := s.pending[sessionID][callID]; ok && cur == entry {
		delete(s.pending[sessionID], callID)
	}
	s.pendingMu.Unlock()
	s.log.Info("external tool result backfilled",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sessionID),
		zap.String("tool", result.Name),
		zap.String("tool_call_id", callID),
		zap.Bool("is_error", result.IsError),
	)
	return nil
}
