package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// 本文件解决一类"退出后断网"的问题：
//
// 桌面端在开启「Steam 多 IP 下载」时会把系统代理改成 127.0.0.1:8080。
// 如果用户**开着开关直接关掉程序**（或被强杀），这个代理就留在系统里，
// 而 8080 已经没人监听——浏览器从此全部 ERR_PROXY_CONNECTION_FAILED，
// 看起来像"关了软件就上不了网"。实机上已经踩到。
//
// 做法：写代理之前先拍一份完整快照存成"租约"文件，还原时按快照写回并删除
// 租约。租约文件同时被守护进程读取——下次启动时若发现残留租约，会先还原
// 再继续，这样即使上次是被强杀的，重启一次也能自愈。

const proxyLeaseFileName = "proxy-lease.json"

// proxySnap 是系统代理的完整快照。
//
// 只记 ProxyServer 是不够的：ProxyEnable 决定"用不用"，ProxyOverride 是
// 例外列表，AutoConfigURL 是 PAC。少还原任何一项都会留下错误状态。
type proxySnap struct {
	Valid      bool   `json:"valid"`
	Server     string `json:"server"`
	Enable     uint32 `json:"enable"`
	Override   string `json:"override"`
	AutoConfig string `json:"autoconfig"`
}

// proxyLeasePath 返回租约文件路径。
//
// 桌面端与守护进程必须用同一个路径，否则启动自愈读不到。
func proxyLeasePath() string {
	if p := os.Getenv("CAULINK_PROXY_LEASE"); p != "" {
		return p
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "caulink-desktop", proxyLeaseFileName)
}

// readProxySnap 读取当前用户的系统代理设置。
func readProxySnap() proxySnap {
	s := proxySnap{}
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return s
	}
	defer k.Close()

	if v, _, err := k.GetStringValue("ProxyServer"); err == nil {
		s.Server = v
	}
	if v, _, err := k.GetStringValue("ProxyOverride"); err == nil {
		s.Override = v
	}
	if v, _, err := k.GetStringValue("AutoConfigURL"); err == nil {
		s.AutoConfig = v
	}
	if v, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		s.Enable = uint32(v)
	}
	s.Valid = true
	return s
}

// restoreProxySnap 按快照写回，并刷新 WinINET 让浏览器立刻生效。
//
// 顺序有讲究：先写地址与例外，最后写开关，避免出现"开关已开、地址还是旧的"
// 的瞬时状态——那一瞬间正好发起的请求会打到错误的地方。
func restoreProxySnap(s proxySnap) error {
	if !s.Valid {
		return nil
	}
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if s.Server == "" {
		_ = k.DeleteValue("ProxyServer")
	} else if err := k.SetStringValue("ProxyServer", s.Server); err != nil {
		return err
	}
	if s.Override == "" {
		_ = k.DeleteValue("ProxyOverride")
	} else if err := k.SetStringValue("ProxyOverride", s.Override); err != nil {
		return err
	}
	if s.AutoConfig == "" {
		_ = k.DeleteValue("AutoConfigURL")
	} else if err := k.SetStringValue("AutoConfigURL", s.AutoConfig); err != nil {
		return err
	}
	// ProxyEnable 在注册表里是 REG_DWORD，必须用 SetDWordValue；
	// 写成 QWORD 的话 WinINET 读不到，等于没还原（会继续走坏代理）。
	if err := k.SetDWordValue("ProxyEnable", s.Enable); err != nil {
		return err
	}
	refreshWinINET()
	return nil
}

// refreshWinINET 通知系统代理设置已变更，让已开的浏览器/应用重新读取。
func refreshWinINET() {
	dll := syscall.NewLazyDLL("wininet.dll")
	proc := dll.NewProc("InternetSetOptionW")
	const (
		optionSettingsChanged = 39
		optionRefresh         = 37
	)
	_, _, _ = proc.Call(0, optionSettingsChanged, 0, 0)
	_, _, _ = proc.Call(0, optionRefresh, 0, 0)
}

// writeProxyLease 在改动系统代理**之前**调用：拍快照并落盘。
//
// 已有租约时不覆盖——那说明上一次的按住还没还完，覆盖会把"真正的原值"弄丢。
func writeProxyLease() error {
	path := proxyLeasePath()
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	snap := readProxySnap()
	if !snap.Valid {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	log.Printf("[proxy-lease] 已保存系统代理快照: enable=%d server=%q", snap.Enable, snap.Server)
	return nil
}

// restoreProxyLease 按租约还原系统代理并删除租约。
//
// 没有租约时什么也不做：那说明我们没有改过代理，不该动用户的设置。
func restoreProxyLease() {
	path := proxyLeasePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var snap proxySnap
	if err := json.Unmarshal(data, &snap); err != nil {
		_ = os.Remove(path)
		return
	}
	if err := restoreProxySnap(snap); err != nil {
		log.Printf("[proxy-lease] 还原系统代理失败: %v", err)
		return
	}
	_ = os.Remove(path)
	log.Printf("[proxy-lease] 已还原系统代理: enable=%d server=%q", snap.Enable, snap.Server)
}
