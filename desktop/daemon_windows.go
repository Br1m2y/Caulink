//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// configureHidden 让子进程不弹控制台窗口。
//
// caulinkd 是控制台程序，桌面端是 windowsgui 程序；
// 不做这一步，双击桌面端会额外闪出一个黑窗口。
func configureHidden(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
