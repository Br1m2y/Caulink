//go:build windows

package main

// 系统代理的读写（Windows 注册表）。
//
// setSystemProxy 在 steam_windows.go 里已经实现（含 PowerShell 回退），
// 这里只补"读回原值"，好让按住结束后能还原成用户原本的设置。

import (
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

// readSystemProxy 读当前 HKCU 的 ProxyServer 值。
func readSystemProxy() (string, bool, error) {
	path, err := syscall.UTF16PtrFromString(internetSettingsPath)
	if err != nil {
		return "", false, err
	}
	var key syscall.Handle
	ret, _, _ := regOpenKeyExW.Call(
		uintptr(syscall.HKEY_CURRENT_USER),
		uintptr(unsafe.Pointer(path)),
		0,
		uintptr(syscall.KEY_QUERY_VALUE),
		uintptr(unsafe.Pointer(&key)),
	)
	if ret != 0 {
		// 读不了就走 PowerShell 兜底（与写入同一套思路）
		out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
			`(Get-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' -Name ProxyServer -ErrorAction SilentlyContinue).ProxyServer`).Output()
		if err != nil {
			return "", false, err
		}
		s := strings.TrimSpace(string(out))
		return s, s != "", nil
	}
	defer regCloseKeyProc.Call(uintptr(key))

	namePtr, _ := syscall.UTF16PtrFromString("ProxyServer")
	var typ uint32
	var buf [512]uint16
	size := uint32(len(buf) * 2)
	regQueryValueExW := advapi.NewProc("RegQueryValueExW")
	ret, _, _ = regQueryValueExW.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(namePtr)),
		0,
		uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 {
		return "", false, nil
	}
	s := syscall.UTF16ToString(buf[:])
	return s, s != "", nil
}
