// diskquota.go —— file_ops 写 protected/ 前的磁盘配额校验回调（模块三·保护区配额）。
//
// 设计：builtin 包不依赖 DB——配额查询/角色默认/目录统计由装配方（cmd/agent）
// 注入实现（agentsvc.DiskQuotaEnforcer），file_ops 仅按协议调用回调。
// 回调为 nil 时不校验（历史行为，本地/测试降级路径）。
package builtin

import "context"

// CheckDiskQuota 写入保护区前的配额校验回调。
//
// 参数：
//   - ctx：工具执行上下文（内含 X-User-Id）；
//   - userID：写入者用户 ID；
//   - protectedDir：该用户 protected/ 目录绝对路径（目录可能尚未创建）；
//   - writeBytes：本次写入字节数（用于"已用 + 本次 > 配额"的软上限拦截）；
//   - role：调用者角色（X-User-Role，空 = 普通用户）。
//
// 返回 error 表示超配额拒绝写入（错误信息会回填给模型）；返回 nil 放行。
type CheckDiskQuota func(ctx context.Context, userID int64, protectedDir string, writeBytes int64, role string) error
