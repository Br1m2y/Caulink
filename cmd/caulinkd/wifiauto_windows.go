package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// 本节负责"插上新的 USB 无线网卡后，点重新扫描能自己连上校园网"。
//
// 背景：一台全新的电脑上，Windows 没有 CAU 的配置文件，新插的无线网卡不会
// 自己连，于是既没有地址、也进不了调度池，界面上看起来就是"扫不到网卡"。
//
// 实现上有意**不去解析 netsh 的输出**：
// netsh 的文本编码跟随控制台代码页，在本机守护进程里是 GBK（实测原始字节
// 是 "ϵͳ\xc9\xcf..."，即"系统上有…"），而 Go 里拿到的是原始字节，
// 按 UTF-8 匹配中文字段名永远对不上——曾经因此"一块无线网卡都没识别到"。
// 现在的做法：挑出"没有可用 IPv4"的网卡，挨个执行 netsh wlan connect；
// 有线/虚拟网卡会立刻返回错误，跳过即可，全程不依赖任何本地化文本。

// wifiProfileTemplate 是开放网络（无密码，认证走网页门户）的配置文件。
const wifiProfileTemplate = `<?xml version="1.0"?>
<WLANProfile xmlns="http://www.microsoft.com/networking/WLAN/profile/v1">
  <name>%s</name>
  <SSIDConfig>
    <SSID><name>%s</name></SSID>
  </SSIDConfig>
  <connectionType>ESS</connectionType>
  <connectionMode>auto</connectionMode>
  <MSM>
    <security>
      <authEncryption>
        <authentication>open</authentication>
        <encryption>none</encryption>
        <useOneX>false</useOneX>
      </authEncryption>
    </security>
  </MSM>
</WLANProfile>
`

// buildWifiProfileXML 生成开放网络的配置内容。
//
// SSID 里可能出现 & < > 这类字符，必须转义，否则 netsh 会拒绝整份文件。
func buildWifiProfileXML(ssid string) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	s := esc.Replace(ssid)
	return fmt.Sprintf(wifiProfileTemplate, s, s)
}

// netshHidden 执行 netsh，隐藏窗口并返回合并后的输出。
//
// netsh 成功时也会往 stderr 写提示，所以两者都收；判断成败以退出码为准。
// 注意返回值是原始字节转成的字符串，中文在 GBK 代码页下会是乱码——
// 只用于写日志，不要拿它做逻辑判断。
func netshHidden(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "netsh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// interfacesNeedingNetwork 返回"已启用、非回环、没有可路由 IPv4、且未被排除"的网卡名。
//
// 有地址的网卡绝不碰：它可能正连着手机热点或别的网络，强行切换会断掉
// 用户正在用的连接。
func interfacesNeedingNetwork(exclude []string) []string {
	excluded := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		if name != "" {
			excluded[name] = true
		}
	}

	ifaces, err := netInterfaceNames()
	if err != nil {
		return nil
	}
	var out []string
	for _, name := range ifaces {
		if excluded[name] {
			continue
		}
		if _, ok := interfaceIPv4(name); ok {
			continue
		}
		out = append(out, name)
	}
	return out
}

// netInterfaceNames 返回所有非回环的网卡名。
//
// 刻意**不**按 FlagUp 过滤：Windows 上"已启用但未关联任何网络"的无线网卡
// 往往不置 up 位，而这类网卡恰恰是我们要自动连的。对真正禁用/未插入的网卡，
// netsh 会立刻报错，代价只是几条无效尝试。
func netInterfaceNames() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		out = append(out, ifi.Name)
	}
	return out, nil
}

// wifiNamesFromNetsh 返回 netsh 列出的无线网卡名。
//
// 做法是"取每行冒号后面的值，再与本机真实网卡名取交集"，而不是匹配字段名——
// 中文系统下字段名是 GBK 字节，代码里按 UTF-8 匹配必定失败。
func wifiNamesFromNetsh(ctx context.Context) map[string]bool {
	out, _ := netshHidden(ctx, "wlan", "show", "interfaces")
	if out == "" {
		return nil
	}
	real := make(map[string]bool)
	if ifaces, err := net.Interfaces(); err == nil {
		for _, ifi := range ifaces {
			real[ifi.Name] = true
		}
	}
	return intersectNetshValues(out, real)
}

// intersectNetshValues 从 netsh 输出里挑出属于 real 的接口名。
func intersectNetshValues(out string, real map[string]bool) map[string]bool {
	names := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		_, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v := strings.TrimSpace(val)
		if v != "" && real[v] {
			names[v] = true
		}
	}
	return names
}

// ensureWifiProfile 把校园网配置写入本用户；已存在时也无妨（netsh 会提示已存在）。
func ensureWifiProfile(ctx context.Context, dataDir, ssid string) error {
	path := filepath.Join(dataDir, "wifi-profile-"+sanitizeFileName(ssid)+".xml")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(buildWifiProfileXML(ssid)), 0o600); err != nil {
		return err
	}
	// user=current：不需要管理员权限。
	_, _ = netshHidden(ctx, "wlan", "add", "profile", "filename="+path, "user=current")
	return nil
}

// sanitizeFileName 把 SSID 变成安全的文件名。
func sanitizeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "ssid"
	}
	return b.String()
}

// autoConnectWifi 为"没有可用地址"的网卡注入并连接校园网，返回成功的接口名。
//
// wait 是每块网卡等 DHCP 的上限。不能以 netsh 的退出码判断成功：有些情况
// netsh 返回 0 但并没有真的连上（例如根本没扫到该 SSID），所以这里只以
// "是否拿到 IPv4"为成功判据。
func autoConnectWifi(ctx context.Context, logger *slog.Logger, dataDir, ssid string, exclude []string, wait time.Duration) []string {
	if ssid == "" {
		return nil
	}
	cands := interfacesNeedingNetwork(exclude)
	if len(cands) == 0 {
		return nil
	}
	// 只保留 netsh 认得的无线网卡。
	//
	// 不能按"名称/Name"这类字段名去解析 netsh 的文本：它的编码跟随控制台代码页，
	// 守护进程里拿到的中文是 GBK 字节，按 UTF-8 匹配"名称"永远不中（踩过）。
	// 但**值**不受影响：无线网卡名通常是 "WLAN"/"WLAN 2"/"Wi-Fi" 这类 ASCII，
	// 把 netsh 输出的每个值与本机真实网卡名取交集，既能识别又不依赖语言。
	wifi := wifiNamesFromNetsh(ctx)
	var wireless []string
	for _, name := range cands {
		if wifi[name] {
			wireless = append(wireless, name)
		}
	}
	logger.Info("重新扫描：检查无线网卡", "no_ip", len(cands), "wireless", wireless, "wifi_names", len(wifi))
	cands = wireless
	if len(cands) == 0 {
		return nil
	}

	if err := ensureWifiProfile(ctx, dataDir, ssid); err != nil {
		logger.Warn("写入校园网 WiFi 配置失败", "ssid", ssid, "err", err)
		return nil
	}

	// 并发尝试，而不是一块一块来。
	//
	// netsh 对"这张网卡不是无线"也返回 0（实测：以太网、蓝牙网卡都返回 0，
	// 只是输出一句 "There is no such wireless interface on the system."），
	// 所以没法靠退出码快速筛掉，只能每块都给一个短暂的观察窗。
	// 串行会给一次扫描增加 N×等待时间的延迟，并发后总耗时≈一个窗口。
	type result struct {
		iface string
		ip    string
	}
	done := make(chan result, len(cands))
	for _, name := range cands {
		name := name
		go func() {
			logger.Info("重新扫描：尝试为网卡连接校园网", "interface", name, "ssid", ssid)
			out, err := netshHidden(ctx, "wlan", "connect", "name="+ssid, "interface="+name)
			if err != nil {
				logger.Info("连接命令失败，跳过该网卡", "interface", name, "err", err, "output", out)
				done <- result{iface: name}
				return
			}
			deadline := time.Now().Add(wait)
			for time.Now().Before(deadline) {
				if ip, ok := interfaceIPv4(name); ok {
					logger.Info("网卡已连上校园网并取得地址", "interface", name, "ip", ip)
					done <- result{iface: name, ip: ip}
					return
				}
				select {
				case <-ctx.Done():
					done <- result{iface: name}
					return
				case <-time.After(400 * time.Millisecond):
				}
			}
			// 没拿到地址：多半不是无线网卡，或该网卡没扫到 CAU。
			logger.Info("未取得地址（非无线网卡或未扫到校园网）", "interface", name)
			done <- result{iface: name}
		}()
	}

	var connected []string
	for i := 0; i < len(cands); i++ {
		if r := <-done; r.ip != "" {
			connected = append(connected, r.iface)
		}
	}
	return connected
}
