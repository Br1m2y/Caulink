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

// 本节负责"插上新的 USB 无线网卡后，自动连上校园网"。
//
// 背景：一台全新的电脑上，Windows 没有 CAU 的配置文件，新插的无线网卡不会
// 自己连，于是既没有地址、也进不了调度池，界面上看起来就是"扫不到网卡"。
//
// 实现上有意**不去解析 netsh 输出的字段名**：
// netsh 的文本编码跟随控制台代码页，在守护进程里是 GBK（实测原始字节是
// "ϵͳ\xc9\xcf..."，即"系统上有…"），按 UTF-8 匹配"名称"永远不中。
// 现在的做法：
//  1. 挑出"没有可用 IPv4"的网卡；
//  2. 与 netsh 报出的接口名取交集，只留无线网卡（名字通常是 ASCII，不受编码影响）；
//  3. 每块网卡最多尝试 3 轮连接，每轮等 3 秒看是否拿到地址。
//
// 三轮是有依据的：刚插上的网卡常常还没扫完信道，第一轮 netsh 会报
// "找不到网络"，隔两秒就正常了。

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
// 返回值是原始字节转成的字符串，中文在 GBK 代码页下会是乱码——只用于写日志，
// 不要拿它做逻辑判断。判断成败一律以退出码为准。
func netshHidden(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "netsh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// interfacesNeedingNetwork 返回"没有可路由 IPv4、且未被排除"的网卡名。
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
	names, err := netInterfaceNames()
	if err != nil {
		return nil
	}
	var out []string
	for _, name := range names {
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
// 往往不置 up 位，而这类网卡恰恰是我们要自动连的。
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
// 取每行冒号后面的值，再与本机真实网卡名取交集——不匹配字段名，因而不受
// 系统语言与代码页影响。
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

// ensureWifiProfile 把校园网配置写入本用户。
//
// 先试 user=all（对所有用户生效，多数机器需要管理员），失败再退回
// user=current（只需当前用户权限）。两条都不成也没关系：只要原来就有配置，
// 后面的 connect 仍然能成功。
func ensureWifiProfile(ctx context.Context, dataDir, ssid string) {
	path := filepath.Join(dataDir, "wifi-profile-"+sanitizeFileName(ssid)+".xml")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return
	}
	if err := os.WriteFile(path, []byte(buildWifiProfileXML(ssid)), 0o600); err != nil {
		return
	}
	_, _ = netshHidden(ctx, "wlan", "add", "profile", "filename="+path, "user=all")
	_, _ = netshHidden(ctx, "wlan", "add", "profile", "filename="+path, "user=current")
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

const (
	// wifiConnectAttempts 是每块网卡最多尝试几轮连接。
	wifiConnectAttempts = 3
	// wifiAttemptWait 是每轮连接后等 DHCP 的时长。
	wifiAttemptWait = 3 * time.Second
)

// autoConnectWifi 为"没有可用地址"的无线网卡注入并连接校园网。
//
// 返回（成功拿到地址的网卡，尝试过但没成功的网卡）。两个列表都会进日志与
// 扫描结果，用户报"扫不到网卡"时能直接看出是没认出、连不上、还是拿到地址慢。
func autoConnectWifi(ctx context.Context, logger *slog.Logger, dataDir, ssid string, exclude []string, wait time.Duration) (connected, tried []string) {
	if ssid == "" {
		return nil, nil
	}
	noIP := interfacesNeedingNetwork(exclude)
	if len(noIP) == 0 {
		return nil, nil
	}

	wifi := wifiNamesFromNetsh(ctx)
	var wireless []string
	for _, name := range noIP {
		if wifi[name] {
			wireless = append(wireless, name)
		}
	}
	logger.Info("检查无线网卡", "no_ip", len(noIP), "wireless", wireless, "wifi_names", len(wifi))
	if len(wireless) == 0 {
		// 有网卡没地址、却一块无线网卡都没认出来：把 netsh 的原始输出留一行，
		// 便于判断是"确实没有无线网卡"还是"netsh 输出格式变了"。
		raw, _ := netshHidden(ctx, "wlan", "show", "interfaces")
		head := raw
		if len(head) > 200 {
			head = head[:200]
		}
		logger.Info("本次没有需要连接的无线网卡", "netsh_head", head, "no_ip", noIP)
		return nil, nil
	}

	ensureWifiProfile(ctx, dataDir, ssid)

	for _, name := range wireless {
		got := ""
		for attempt := 1; attempt <= wifiConnectAttempts; attempt++ {
			logger.Info("尝试连接校园网", "interface", name, "ssid", ssid, "attempt", attempt)
			out, err := netshHidden(ctx, "wlan", "connect", "name="+ssid, "interface="+name)
			if err != nil {
				logger.Warn("netsh connect 失败", "interface", name, "attempt", attempt,
					"err", err, "output", out)
			}
			// 不靠退出码判断：netsh 对"这不是无线接口"也返回 0。
			// 只认"是否拿到可路由 IPv4"，轮询到本轮结束。
			deadline := time.Now().Add(wifiAttemptWait)
			for time.Now().Before(deadline) {
				if ip, ok := interfaceIPv4(name); ok {
					got = ip
					break
				}
				select {
				case <-ctx.Done():
					return connected, tried
				case <-time.After(300 * time.Millisecond):
				}
			}
			if got != "" {
				break
			}
			logger.Info("本轮未取得地址，稍后重试", "interface", name, "attempt", attempt)
		}
		if got != "" {
			logger.Info("网卡已连上校园网并取得地址", "interface", name, "ip", got)
			connected = append(connected, name)
		} else {
			tried = append(tried, name)
			logger.Warn("尝试连接校园网但未取得地址", "interface", name,
				"hint", "检查该网卡是否被禁用、无线电是否关闭，或信号是否覆盖")
		}
	}
	return connected, tried
}
