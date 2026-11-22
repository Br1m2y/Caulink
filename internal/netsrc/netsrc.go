// Package netsrc 处理"源地址与目标地址族匹配"这件事。
//
// 一个 TCP socket 的源地址必须与目标地址同族：用 IPv4 源去连 IPv6 目标，
// 内核直接报 WSAEADDRNOTAVAIL（"在其上下文中，该请求的地址无效"）。
//
// 这在纯 IPv4 环境里从来不是问题，但在校园网（IPv6 资源丰富、BT 对等体
// 大量是 v6）里是致命的：所有 v6 连接都会失败，客户端表现为"一个对等体都连不上"。
package netsrc

import (
	"context"
	"fmt"
	"net"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// ErrNoSourceForFamily 表示该条目没有对应地址族的源地址。
var ErrNoSourceForFamily = fmt.Errorf("netsrc: 该网卡没有可用于该地址族的源地址")

// ParseSource 返回条目中指定地址族的源地址。
//
// wantV6=true 取 SourceIP6，否则取 SourceIP。
func ParseSource(entry contract.IPPoolEntry, wantV6 bool) (net.IP, error) {
	if wantV6 {
		if entry.SourceIP6 == "" {
			return nil, ErrNoSourceForFamily
		}
		ip := net.ParseIP(entry.SourceIP6)
		if ip == nil {
			return nil, fmt.Errorf("netsrc: source_ip6 %q 无法解析", entry.SourceIP6)
		}
		return ip, nil
	}
	if entry.SourceIP == "" {
		return nil, ErrNoSourceForFamily
	}
	ip := net.ParseIP(entry.SourceIP)
	if ip == nil {
		return nil, fmt.Errorf("netsrc: source_ip %q 无法解析", entry.SourceIP)
	}
	return ip, nil
}

// HasFamily 判断条目是否支持指定地址族。
func HasFamily(entry contract.IPPoolEntry, wantV6 bool) bool {
	_, err := ParseSource(entry, wantV6)
	return err == nil
}

// Target 是一次解析后的拨号目标。
type Target struct {
	// Network 是 "tcp4" 或 "tcp6"，显式限定地址族。
	Network string
	// Addr 是 "ip:port" 形式的最终目标。
	Addr string
	// Source 是要绑定的源地址。
	Source net.IP
}

// Resolve 把「用户给的目标 + 条目可用的源」解析成一个可拨号的组合。
//
// 规则：
//   - 目标是 IPv6 字面量 → 必须用 v6 源；没有就报错，绝不退回默认路由
//   - 目标是 IPv4 字面量 → 必须用 v4 源
//   - 目标是域名 → 解析后优先选与条目有源地址的族；两边都有时优先 v4
//     （实测 v4 路径的中转和成功率更稳，v6 只在没有 v4 时才用）
func Resolve(ctx context.Context, hostPort string, entry contract.IPPoolEntry) (Target, error) {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return Target{}, fmt.Errorf("netsrc: 目标 %q 格式不对: %w", hostPort, err)
	}

	// 字面量地址：直接按族选源。
	if ip := net.ParseIP(host); ip != nil {
		wantV6 := ip.To4() == nil
		src, err := ParseSource(entry, wantV6)
		if err != nil {
			return Target{}, err
		}
		network := "tcp4"
		if wantV6 {
			network = "tcp6"
		}
		return Target{Network: network, Addr: net.JoinHostPort(ip.String(), port), Source: src}, nil
	}

	// 域名：解析后按可用性挑。
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return Target{}, fmt.Errorf("netsrc: 解析 %s 失败: %w", host, err)
	}

	var v4, v6 []net.IP
	for _, a := range addrs {
		ip := a.IP
		if ip.To4() != nil {
			v4 = append(v4, ip)
		} else if ip.IsGlobalUnicast() {
			v6 = append(v6, ip)
		}
	}

	if len(v4) > 0 && HasFamily(entry, false) {
		src, _ := ParseSource(entry, false)
		return Target{Network: "tcp4", Addr: net.JoinHostPort(v4[0].String(), port), Source: src}, nil
	}
	if len(v6) > 0 && HasFamily(entry, true) {
		src, _ := ParseSource(entry, true)
		return Target{Network: "tcp6", Addr: net.JoinHostPort(v6[0].String(), port), Source: src}, nil
	}

	// 目标解析出了地址，但条目没有对应族的源。
	if len(v4) > 0 {
		src, err := ParseSource(entry, false)
		if err != nil {
			return Target{}, err
		}
		return Target{Network: "tcp4", Addr: net.JoinHostPort(v4[0].String(), port), Source: src}, nil
	}
	return Target{}, ErrNoSourceForFamily
}

// WantsIPv6 判断目标是否是 IPv6 字面量（供调度层过滤候选）。
func WantsIPv6(hostPort string) (bool, bool) {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return false, false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, false // 域名，未知
	}
	return ip.To4() == nil, true
}
