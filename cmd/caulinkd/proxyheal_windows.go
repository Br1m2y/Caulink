package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// 本文件负责"上次异常退出留下的代理租约"的启动自愈。
//
// 桌面端在改系统代理之前会写一份租约（%LOCALAPPDATA%\caulink-desktop\proxy-lease.json）。
// 正常关闭时它自己被还原并删掉；但如果是被强杀（任务管理器结束进程、蓝屏、断电），
// 租约就会留在磁盘上，而系统代理还指着 127.0.0.1:8080——此时 8080 已经没人监听，
// 浏览器全部报 ERR_PROXY_CONNECTION_FAILED，用户看到的就是"关了软件就上不了网"。
//
// 重启一次就能自愈：守护进程启动时若发现租约，按快照还原再删掉它。

// proxyLeaseFileName 必须与桌面端保持一致。
const proxyLeaseFileName = "proxy-lease.json"

// proxyLease 是租约文件的 JSON 结构。
//
// 注意 Enable 用 uint32：注册表里是 DWORD，桌面端按数字写入；
// 这里转成 proxySnapshot 需要的 bool。
type proxyLease struct {
	Valid      bool   `json:"valid"`
	Server     string `json:"server"`
	Enable     uint32 `json:"enable"`
	Override   string `json:"override"`
	AutoConfig string `json:"autoconfig"`
}

// proxyLeasePath 返回租约路径，与桌面端一致。
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

// healProxyLease 在启动时检查残留租约：有就还原并删除。
//
// 只在"确实有租约"时动手——没有租约说明我们从没改过代理，不该碰用户设置。
func healProxyLease(logger *slog.Logger) {
	path := proxyLeasePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var lease proxyLease
	if err := json.Unmarshal(data, &lease); err != nil {
		logger.Warn("代理租约文件损坏，已删除", "err", err)
		_ = os.Remove(path)
		return
	}

	snap := proxySnapshot{
		Valid:      lease.Valid,
		Server:     lease.Server,
		Enable:     lease.Enable != 0,
		Override:   lease.Override,
		AutoConfig: lease.AutoConfig,
	}
	if err := restoreProxySnapshot(snap); err != nil {
		logger.Warn("按租约还原系统代理失败", "err", err)
		return
	}
	_ = os.Remove(path)
	logger.Info("已按上次遗留的租约还原系统代理",
		"enable", lease.Enable != 0, "server", lease.Server)
}

// writeProxyLease 把快照写成租约文件（守护进程按住系统代理时调用）。
//
// 桌面端关闭时会用 TerminateProcess 结束守护进程，defer 不会执行；
// 有了租约，下次启动就能按它还原，而不是把机器留在坏代理上。
func writeProxyLease(snap proxySnapshot) {
	path := proxyLeasePath()
	lease := proxyLease{
		Valid:      snap.Valid,
		Server:     snap.Server,
		Enable:     boolToUint32(snap.Enable),
		Override:   snap.Override,
		AutoConfig: snap.AutoConfig,
	}
	data, err := json.MarshalIndent(lease, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// removeProxyLease 在正常还原之后删掉租约。
func removeProxyLease() { _ = os.Remove(proxyLeasePath()) }

// boolToUint32 把 bool 转成注册表里的 0/1。
func boolToUint32(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}
