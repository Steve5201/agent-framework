// workdir.go —— 用户工作区中间层目录的统一创建与权限校正。
//
// 背景：agent（uid=100，app 组）与沙盒降权进程（派生 uid，同属 app 组）通过
// 组权限协作读写 users/<uid>/ 下的目录。sandbox ensureWorkspace 在用户目录
// 属主不匹配时执行 recursiveChown（chown -R uid:AgentGID），它只改属主不改
// mode——若把 agent 创建的 0755 中间层（chat-docs/、chat-files/ 等）改为
// 派生 uid:app 组，app 组将只剩 r-x，agent 后续 mkdir/write 被 OS 拒绝
// （报 permission denied）。根治：所有此类中间目录显式设为 2770
// （组 rwx + setgid），即使再被 chown -R 迁移，组写权限位仍然有效。
package agentsvc

import "os"

// ensureGroupWritableDir 创建目录（含父级）并强制 2770。
// MkdirAll 对已存在目录不改 mode，故创建后再 Chmod 兜底纠偏。
func ensureGroupWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o2770); err != nil {
		return err
	}
	return os.Chmod(dir, 0o2770)
}
