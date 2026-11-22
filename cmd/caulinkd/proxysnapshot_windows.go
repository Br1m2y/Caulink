//go:build windows

package main

// 系统代理配置的完整快照与还原。
//
// 教训：还原只记 ProxyServer 会出事——用户原本 ProxyEnable=0（不使用代理），
// 我们把它改成 1 并指向自己的 8080；停止时只把地址写回去，开关留成 1，
// 而那个地址可能根本没人监听，整台机器立刻断网（浏览器
// ERR_PROXY_CONNECTION_FAILED，实测踩过）。
//
// 所以这里做**全量快照**：地址、开关、例外列表、PAC 地址，
// 有多少记多少，还原时按原样写回。

import (
	"os/exec"
	"syscall"
	"unsafe"
)

// proxySnapshot 是系统代理配置的一份完整副本。
type proxySnapshot struct {
	// Valid 为 false 表示读取失败，此时不做任何还原，避免把配置写坏。
	Valid bool

	Server     string // ProxyServer
	Enable     bool   // ProxyEnable
	Override   string // ProxyOverride（代理例外）
	AutoConfig string // AutoConfigURL（PAC 脚本）
}

// readProxySnapshot 读取当前完整的系统代理配置。
func readProxySnapshot() proxySnapshot {
	server, serverOK := readRegistryString("ProxyServer")
	override, _ := readRegistryString("ProxyOverride")
	autoConfig, _ := readRegistryString("AutoConfigURL")
	enable, _ := readSystemProxyEnable()

	return proxySnapshot{
		Valid:      serverOK,
		Server:     server,
		Enable:     enable,
		Override:   override,
		AutoConfig: autoConfig,
	}
}

// restoreProxySnapshot 把系统代理配置按快照写回。
//
// 顺序有讲究：先写地址与例外，最后写开关并刷新，
// 这样中间状态不会出现"开关已开、地址还是旧的"这种瞬间。
func restoreProxySnapshot(s proxySnapshot) error {
	if !s.Valid {
		return nil
	}
	if s.Server != "" {
		if err := writeRegistryString("ProxyServer", s.Server); err != nil {
			return err
		}
	}
	if s.Override != "" {
		if err := writeRegistryString("ProxyOverride", s.Override); err != nil {
			return err
		}
	}
	if s.AutoConfig != "" {
		if err := writeRegistryString("AutoConfigURL", s.AutoConfig); err != nil {
			return err
		}
	}
	if err := setSystemProxyEnabled(s.Enable); err != nil {
		return err
	}
	refreshInternetSettings()
	return nil
}

// ---- 注册表读写小工具（与 proxyreg_windows.go 共用 advapi 句柄） ----

// readRegistryString 读一个 REG_SZ 值。
func readRegistryString(name string) (string, bool) {
	path, err := syscall.UTF16PtrFromString(internetSettingsPath)
	if err != nil {
		return "", false
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
		return "", false
	}
	defer regCloseKeyProc.Call(uintptr(key))

	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return "", false
	}
	var typ uint32
	var buf [1024]uint16
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
		return "", false
	}
	return syscall.UTF16ToString(buf[:]), true
}

// writeRegistryString 写一个 REG_SZ 值。
func writeRegistryString(name, value string) error {
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

	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	val, err := syscall.UTF16FromString(value)
	if err != nil {
		return err
	}
	ret, _, _ = regSetValueExW.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(namePtr)),
		0,
		uintptr(syscall.REG_SZ),
		uintptr(unsafe.Pointer(&val[0])),
		uintptr(len(val)*2),
	)
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}

// ensureWindowsImportUsed 防止 goimports 误删（exec 仅用于兜底路径）。
var _ = exec.Command
