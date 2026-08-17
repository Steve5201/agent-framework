// workspace.go —— 工具执行的用户上下文（阶段2 起：敏感操作按用户隔离）。
//
// agentsvc 在 agent.Run / Stream 前用 llm.WithHeader(ctx, "X-User-Id", ...)
// 注入调用者身份；工具执行时通过 llm.ContextHeader 取回，从而让 code_executor
// 把代码执行委托给 sandbox-service 时带上 user_id（沙盒按用户划分工作区）。
package builtin

import (
	"context"
	"strconv"

	"github.com/Steve5201/agent-framework/llm"
)

// userIDHeader 会话上下文里的用户标识请求头（与 agentsvc 注入处保持一致）。
const userIDHeader = "X-User-Id"

// userRoleHeader 会话上下文里的调用者角色请求头（与 agentsvc 注入处保持一致）。
const userRoleHeader = "X-User-Role"

// UserIDFromContext 从工具执行上下文读取当前调用者用户 ID。
// 未设置 / 非正整数时返回 false（调用方自行决定兜底策略）。
func UserIDFromContext(ctx context.Context) (int64, bool) {
	v := llm.ContextHeader(ctx, userIDHeader)
	if v == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// RoleFromContext 从工具执行上下文读取当前调用者角色（X-User-Role）。
// 未设置返回空串（调用方按普通用户处理，与 llm-gateway 的 isAdminRole 语义一致）。
func RoleFromContext(ctx context.Context) string {
	return llm.ContextHeader(ctx, userRoleHeader)
}
