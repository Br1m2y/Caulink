//go:build windows

package main

// 系统代理"开关"的读写。
//
// 为什么单独拆出来：只还原 ProxyServer 是不够的。
// 如果用户原本没开代理（ProxyEnable=0），而我们把 ProxyEnable 留成 1、
// 只把 ProxyServer 写成别的地址，系统就会把全部流量丢给那个端口——
// 那里没人监听时浏览器直接 ERR_PROXY_CONNECTION_FAILED，整台机器断网。
// 实测踩过：把代理"还原"成 VPN 的 17891，但 VPN 没开，网页全打不开。

import (
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

// readSystemProxyEnable 读当前 ProxyEnable（1=启用，0=关闭）。
func readSystemProxyEnable() (bool, error) {
	path, err := syscall.UTF16PtrFromString(internetSettingsPath)
	if err != nil {
		return false, err
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
		out, e := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
			`(Get-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' -Name ProxyEnable -ErrorAction SilentlyContinue).ProxyEnable`).Output()
		if e != nil {
			return false, e
		}
		return strings.TrimSpace(string(out)) == "1", nil
	}
	defer regCloseKeyProc.Call(uintptr(key))

	namePtr, _ := syscall.UTF16PtrFromString("ProxyEnable")
	var typ uint32
	var val uint32
	size := uint32(4)
	regQueryValueExW := advapi.NewProc("RegQueryValueExW")
	ret, _, _ = regQueryValueExW.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(namePtr)),
		0,
		uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&val)),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 {
		return false, nil
	}
	return val == 1, nil
}

// setSystemProxyEnabled 切换系统代理开关，并刷新 WinINET 让已运行的程序感知。
func setSystemProxyEnabled(on bool) error {
	path, err := syscall.UTF16PtrFromString(internetSettingsPath)
	if err != nil {
		return err
	}
	var key syscall.Handle
	ret, _, _ := regOpenKeyExW.Call(
		uintptr(syscall.HKEY_CURRENT_USER),
		uintptr(unsafe.Pointer(path)),
		0,
		uintptr(syscall.KEY_SET_VALUE),
		uintptr(unsafe.Pointer(&key)),
	)
	if ret != 0 {
		return syscall.Errno(ret)
	}
	defer regCloseKeyProc.Call(uintptr(key))

	namePtr, _ := syscall.UTF16PtrFromString("ProxyEnable")
	var val uint32
	if on {
		val = 1
	}
	ret, _, _ = regSetValueExW.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(namePtr)),
		0,
		uintptr(syscall.REG_DWORD),
		uintptr(unsafe.Pointer(&val)),
		4,
	)
	if ret != 0 {
		return syscall.Errno(ret)
	}
	refreshInternetSettings()
	return nil
}

// refreshInternetSettings 让 WinINET 重新读取代理配置。
func refreshInternetSettings() {
	wininet := syscall.NewLazyDLL("wininet.dll")
	proc := wininet.NewProc("InternetSetOptionW")
	const (
		optionSettingsChanged = 39
		optionRefresh         = 37
	)
	_, _, _ = proc.Call(0, optionSettingsChanged, 0, 0)
	_, _, _ = proc.Call(0, optionRefresh, 0, 0)
}
