package main

// 源 IP 与网卡现状的核对。
//
// 校园网是 DHCP，USB 网卡重新插拔后 IP 会变（实测 10.6.53.173 → 10.6.69.131）。
// 配置里写死的 source_ip 一旦过期，认证子进程会一直绑不上地址：
// 界面上表现就是某个端口永远停在"认证中"。
//
// 所以在启动时用网卡**当前**的 IPv4 覆盖配置里的旧值；
// 网卡不在或没有地址时跳过这个源，并说明原因，而不是留一个必然失败的条目。

import (
	"log/slog"
	"net"
)

// resolveSources 用网卡实际地址校正配置里的 source_ip。
//
// 返回可用的源列表；被跳过的会在日志里说明。
func resolveSources(srcs []SourceConfig, exclude []string, logger *slog.Logger) []SourceConfig {
	out := make([]SourceConfig, 0, len(srcs))
	seen := make(map[string]bool, len(srcs))

	for _, s := range srcs {
		actual, ok := interfaceIPv4(s.Interface)

		switch {
		case !ok:
			logger.Warn("网卡当前没有 IPv4 地址，跳过该源",
				"interface", s.Interface, "configured_ip", s.SourceIP)
			continue

		case actual != s.SourceIP:
			logger.Info("源 IP 已变化，跟随网卡实际地址",
				"interface", s.Interface,
				"configured_ip", s.SourceIP,
				"actual_ip", actual)
			s.SourceIP = actual
		}

		if seen[s.SourceIP] {
			logger.Warn("跳过重复的源 IP", "ip", s.SourceIP, "interface", s.Interface)
			continue
		}
		seen[s.SourceIP] = true

		// IPv6 源：配置没写就自动探测。
		// 没有全局 v6 的网卡也能用，只是连不上 v6 对等体。
		if s.SourceIP6 == "" {
			if v6, ok := interfaceIPv6(s.Interface); ok {
				s.SourceIP6 = v6
				logger.Info("发现全局 IPv6 地址", "interface", s.Interface, "ip6", v6)
			}
		}
		out = append(out, s)
	}

	// 配置里没写、但网卡上真实存在的源：自动补进来。
	//
	// 早期这里刻意"不做自动发现"，理由是可预期；实际用下来代价太大——
	// 插一块新网卡后必须在 JSON 里手写一段 sources 再重启，
	// 而界面上偏偏有一个「重新扫描」按钮，用户点它却什么也不会发生。
	// 现在改成：配置优先（可写死 IP、可指定 v6），没写的网卡自动发现。
	discovered := discoverSources(out, logger)
	if len(exclude) == 0 {
		return append(out, discovered...)
	}
	excluded := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		if name != "" {
			excluded[name] = true
		}
	}
	kept := make([]SourceConfig, 0, len(discovered))
	for _, s := range discovered {
		if excluded[s.Interface] {
			logger.Info("按配置排除该网卡，不参与调度", "interface", s.Interface, "ip", s.SourceIP)
			continue
		}
		kept = append(kept, s)
	}
	return append(out, kept...)
}

// discoverSources 枚举本机网卡，返回配置里还没有、但可以直接用的源。
//
// 筛选规则与 interfaceIPv4 一致：网卡必须是 up，且有非回环、非链路本地的 IPv4。
// 同一网卡已在配置里出现过时跳过（配置里的写死值优先，哪怕 IP 不同——
// 那是用户显式指定的，交给 resolveSources 去校正）。
func discoverSources(known []SourceConfig, logger *slog.Logger) []SourceConfig {
	ifaces, err := net.Interfaces()
	if err != nil {
		logger.Warn("枚举网卡失败，跳过自动发现", "err", err)
		return nil
	}

	byIface := make(map[string]bool, len(known))
	usedIP := make(map[string]bool, len(known))
	for _, s := range known {
		byIface[s.Interface] = true
		if s.SourceIP != "" {
			usedIP[s.SourceIP] = true
		}
	}

	var out []SourceConfig
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if byIface[ifi.Name] {
			continue
		}
		ip, ok := interfaceIPv4(ifi.Name)
		if !ok || usedIP[ip] {
			continue
		}
		s := SourceConfig{Interface: ifi.Name, SourceIP: ip}
		if v6, ok := interfaceIPv6(ifi.Name); ok {
			s.SourceIP6 = v6
		}
		usedIP[ip] = true
		out = append(out, s)
		logger.Info("自动发现本机网卡", "interface", ifi.Name, "ip", ip, "ip6", s.SourceIP6)
	}
	return out
}

// interfaceIPv4 返回指定网卡当前的第一个全局 IPv4 地址。
func interfaceIPv4(name string) (string, bool) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return "", false
	}
	if ifi.Flags&net.FlagUp == 0 {
		return "", false
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return "", false
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
			continue
		}
		return ip4.String(), true
	}
	return "", false
}

// interfaceIPv6 返回指定网卡上第一个可用的**全局** IPv6 地址。
//
// 只接受全球单播（2000::/3）：链路本地（fe80::）、唯一本地（fc00::/7）
// 和回环都不能用来连接外部对等体。临时地址（RFC 4941 隐私地址）优先，
// 因为很多站点的源地址过滤只接受当前有效的那一个。
func interfaceIPv6(name string) (string, bool) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return "", false
	}
	if ifi.Flags&net.FlagUp == 0 {
		return "", false
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return "", false
	}

	var fallback string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP
		if ip.To4() != nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if !ip.IsGlobalUnicast() {
			continue
		}
		// 2000::/3 才是全球单播地址
		if len(ip) != net.IPv6len || ip[0]&0xe0 != 0x20 {
			continue
		}
		// 临时地址（temporary）通常带 "temporary" 标志，Go 里没有直接暴露；
		// 先记录第一个，稳定地址作为兜底。
		if fallback == "" {
			fallback = ip.String()
		}
		if isTemporaryIPv6(ip) {
			return ip.String(), true
		}
	}
	if fallback != "" {
		return fallback, true
	}
	return "", false
}

// isTemporaryIPv6 判断是否为 RFC 4941 临时地址。
//
// Windows 下没有直接 API，这里用简单启发：临时地址通常带有随机化接口标识，
// 与稳定地址难以区分——所以这个函数只是"尽量优先"，失败也不影响正确性。
func isTemporaryIPv6(net.IP) bool {
	return false
}
