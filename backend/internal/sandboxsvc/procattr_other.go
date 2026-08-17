//go:build !linux

package sandboxsvc

import "os/exec"

// setProcAttr 非 Linux 平台（本地开发调试）无进程组语义：no-op。
// sandbox-service 的完整隔离（unshare/prlimit/setpriv）仅支持 Linux 容器。
func setProcAttr(_ *exec.Cmd) {}

// killProcessGroup 非 Linux 平台：退化为仅终止主进程（进程组 API 不可用）。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
