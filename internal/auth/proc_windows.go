//go:build windows

package auth

import (
	"os/exec"
	"syscall"
)

// CREATE_NEW_PROCESS_GROUP 让子进程独立于父进程的 Ctrl+C 影响。
// 显式写常量而不是 syscall.CREATE_NEW_PROCESS_GROUP，避免依赖特定 Go 版本的导出。
const createNewProcessGroup = 0x00000200

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// Windows 没有 SIGTERM，只能强杀。
//
// 影响：子进程来不及自己清理状态，但 BitSrunLoginGo 的会话状态在服务端，
// 本地硬杀不影响下次登录，所以这里可以接受。也正因为如此，
// 认证层的多 IP 能力不该指望 Windows —— 上游的网卡绑定在 Windows 上本就不可靠。
func terminateProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
