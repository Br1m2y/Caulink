//go:build !windows

package auth

import (
	"os/exec"
	"syscall"
)

// setProcAttr 让子进程自成一个进程组。
//
// 这样停止时用 kill(-pid) 能连同它可能派生的子进程一起清理；
// 否则只杀主进程，残留的子进程会继续占着认证会话。
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
