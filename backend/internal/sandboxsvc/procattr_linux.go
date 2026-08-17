//go:build linux

package sandboxsvc

import (
	"os/exec"
	"syscall"
)

// setProcAttr 让子进程成为独立进程组组长：超时/异常时可整体终止子进程树。
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 终止命令所在进程组（含其所有后代进程）。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// 负 PID = 进程组（命令执行时已 Setpgid 使其成为组长）。
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
