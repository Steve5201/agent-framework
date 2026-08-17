// Package identity 提供已校验身份的上下文读写（gateway 与 adminsvc 共享）。
//
// 数据流：gateway 解析 JWT（或游客头）后将 user_id / role / agent_id 写入请求 context；
// adminsvc（同进程）通过本包读取身份，并在出站 gRPC 调用中经
// OutgoingMetadata 注入 x-user-id，下游服务只信该来源。
package identity

import (
	"context"
	"strconv"

	"google.golang.org/grpc/metadata"
)

// 私有类型作为 context key，避免与其他库碰撞。
type ctxKey int

const (
	ctxKeyUserID ctxKey = iota
	ctxKeyUserRole
	ctxKeyAgentID
)

// WithUserID 写入已校验的 user_id（真实用户 > 0；游客为负数）。
func WithUserID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, ctxKeyUserID, id)
}

// UserID 读取 user_id；不存在或为 0 返回 false（0 视为非法）。
func UserID(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(ctxKeyUserID).(int64)
	if !ok || v == 0 {
		return 0, false
	}
	return v, true
}

// WithRole 写入已校验的角色（JWT 声明；游客为空串）。
func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, ctxKeyUserRole, role)
}

// Role 读取角色（无则空串）。
func Role(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyUserRole).(string); ok {
		return v
	}
	return ""
}

// WithAgentID 写入资源域归属（JWT agent_id 声明；超管/游客为空）。
// 阶段3·多租户：agent_admin/admin 的资源域由此锁定，super_admin 为空可指定任意域。
func WithAgentID(ctx context.Context, agentID string) context.Context {
	return context.WithValue(ctx, ctxKeyAgentID, agentID)
}

// AgentID 读取资源域归属（无则空串）。
func AgentID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyAgentID).(string); ok {
		return v
	}
	return ""
}

// OutgoingMetadata 将已校验身份注入出站 gRPC metadata（键 x-user-id）。
// 供 adminsvc 等内部组件调用 authsvc 时透传调用者身份。
func OutgoingMetadata(ctx context.Context, userID int64) context.Context {
	return metadata.AppendToOutgoingContext(
		ctx,
		"x-user-id", strconv.FormatInt(userID, 10),
	)
}

// IsAdminRole 判断角色是否为管理员类（可访问 /v1/admin/*）。
// 阶段3·角色体系：super_admin（最高超管）/ agent_admin（智能体超管）/
// admin（普通管理员）均视为管理员；模块级细分由 adminsvc 按角色裁剪。
func IsAdminRole(role string) bool {
	switch role {
	case "super_admin", "agent_admin", "admin":
		return true
	}
	return false
}
