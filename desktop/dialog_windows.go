//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// showError 用系统弹窗显示致命错误，避免用户只看到"没反应"。
func showError(title, text string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("MessageBoxW")
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	textPtr, _ := syscall.UTF16PtrFromString(text)
	const mbIconError = 0x00000010
	proc.Call(0, uintptr(unsafe.Pointer(textPtr)), uintptr(unsafe.Pointer(titlePtr)), mbIconError)
}
