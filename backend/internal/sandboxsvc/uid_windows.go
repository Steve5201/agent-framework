//go:build !unix

package sandboxsvc

import "os"

// fileOwnerID 非 Unix（如 Windows 本地调试）无法获取属主 uid，返回 -1。
// 此时 fixUserDir 判定"属主不符"而触发迁移流程，chown 不可用会
// 被记录 WARN 而不阻断（本地开发降级，生产容器内必然成功）。
func fileOwnerID(os.FileInfo) int { return -1 }

// fileGroupID 非 Unix 无法获取属组 gid，返回 -1（同上触发迁移流程）。
func fileGroupID(os.FileInfo) int { return -1 }
