//go:build windows

package netsrc

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Windows 上"让 socket 从某块网卡出去"需要两件事，缺一不可：
//
//	LocalAddr   （bind 源地址）—— 决定包的源 IP
//	IP_UNICAST_IF（本文件）    —— 决定包走哪块网卡
//
// 两块网卡同网段、默认网关也相同时（校园网双网卡的典型形态），内核只按目的地址
// 选路，会挑 metric 更低的那块出口，于是出现"源 IP 属于网卡 A、包却从网卡 B 发出"。
// 接入交换机按源 IP/MAC 不匹配丢弃，用户看到的是连接超时，而不是任何配置错误。
//
// 实测（见 双IP实测结果.md）：只绑源 IP 时 10.6.54.214 连续三次 8 秒超时，
// 补上 IP_UNICAST_IF 之后同一个地址立刻正常，双源并行也才真正分成两路。
//
// Linux/macOS 不需要这一步：内核会按源地址反查路由，LocalAddr 绑对了包就一定
// 从该地址所属的网卡出去。所以这个坑只在 Windows 暴露，也只需要在这里补。
const (
	// 编号取自 Windows SDK 的 ws2ipdef.h。
	ipUnicastIF   = 31 // IPPROTO_IP   → IPv4 出口网卡
	ipv6UnicastIF = 31 // IPPROTO_IPV6 → IPv6 出口网卡
	ipProtoIPv6   = 41 // IPPROTO_IPV6

	// 接口索引会随"网卡被禁用/启用、USB 网卡重新插拔"变化，缓存必须有有效期：
	// 永久记住会在插拔后把包发到错误的网卡上，而那种故障看起来和没修一样。
	ifIndexTTL = 30 * time.Second
)

// DialControl 返回 net.Dialer.Control，把 socket 钉在源地址所属的那块网卡上。
//
// 返回 nil 的含义要分清：**不是**"可以不绑源地址"。调用方仍然必须设置
// LocalAddr，本函数只补上 Windows 缺的另一半。src 为 nil 时无事可做，返回 nil。
func DialControl(src net.IP) func(network, address string, c syscall.RawConn) error {
	if src == nil {
		return nil
	}
	wantV6 := src.To4() == nil

	return func(network, address string, c syscall.RawConn) error {
		// network 是真正拨号用的网络（tcp4/tcp6/udp4/udp6）。族对不上说明调用方
		// 选路有错，不在这里纠正，交给 connect 自己报错。
		if v6, ok := networkFamily(network); ok && v6 != wantV6 {
			return nil
		}

		idx, ok := interfaceIndex(src)
		if !ok {
			// loopback 不属于任何真实网卡（单测、本地调试都是这种），放过。
			// 真实网卡上找不到源地址则明确报错：这种拨号必然超时，
			// 报"找不到网卡"比 8 秒后报 timeout 好排查得多。
			if src.IsLoopback() {
				return nil
			}
			return fmt.Errorf("netsrc: 找不到持有源地址 %s 的网卡，无法钉住出口网卡（网卡已拔出或地址已变更）", src)
		}
		return bindToInterface(c, idx, wantV6)
	}
}

// networkFamily 判断 network 限定的地址族；ok=false 表示未限定（"tcp"/"udp"）。
func networkFamily(network string) (v6 bool, ok bool) {
	switch {
	case strings.HasSuffix(network, "6"):
		return true, true
	case strings.HasSuffix(network, "4"):
		return false, true
	}
	return false, false
}

type ifIndexEntry struct {
	index int
	at    time.Time
}

var (
	ifIndexMu    sync.Mutex
	ifIndexCache = make(map[string]ifIndexEntry)
)

// interfaceIndex 返回源地址所属网卡的接口索引，带 TTL 缓存。
//
// 每次拨号都枚举一遍网卡（GetAdaptersAddresses）在高并发下载时是纯浪费，
// 但缓存又不能永久有效 —— 30 秒是这两者之间的折中。
func interfaceIndex(src net.IP) (int, bool) {
	key := src.String()
	now := time.Now()

	ifIndexMu.Lock()
	if e, ok := ifIndexCache[key]; ok && now.Sub(e.at) < ifIndexTTL {
		ifIndexMu.Unlock()
		return e.index, true
	}
	ifIndexMu.Unlock()

	idx, ok := lookupInterfaceIndex(src)
	if !ok {
		return 0, false
	}

	ifIndexMu.Lock()
	ifIndexCache[key] = ifIndexEntry{index: idx, at: now}
	ifIndexMu.Unlock()
	return idx, true
}

// lookupInterfaceIndex 遍历本机网卡，找出持有 src 的那一块。
func lookupInterfaceIndex(src net.IP) (int, bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return 0, false
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ip := addrIP(a); ip != nil && ip.Equal(src) {
				return iface.Index, true
			}
		}
	}
	return 0, false
}

func addrIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	}
	return nil
}

// bindToInterface 在 socket 建好、bind 之前设置出口网卡选项。
func bindToInterface(c syscall.RawConn, ifIndex int, v6 bool) error {
	level, opt := syscall.IPPROTO_IP, ipUnicastIF
	value := int(htonl(uint32(ifIndex)))
	if v6 {
		level, opt = ipProtoIPv6, ipv6UnicastIF
		// 两个选项的字节序**不一样**，这点没有任何文档讲清楚，是本机实测出来的：
		//   IP_UNICAST_IF   要求网络字节序，写成主机序 → WSAEADDRNOTAVAIL
		//   IPV6_UNICAST_IF 要求主机序，  写成网络序 → WSAEINVAL
		// 两种写法都不会静默成功，所以 test 里各钉了一个用例。
		value = ifIndex
	}

	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(syscall.Handle(fd), level, opt, value)
	}); err != nil {
		return fmt.Errorf("netsrc: 操作 socket 失败: %w", err)
	}
	if serr != nil {
		return fmt.Errorf("netsrc: 把网卡 %d 钉为出口失败: %w", ifIndex, serr)
	}
	return nil
}

// htonl 主机序 → 网络字节序（32 位）。
func htonl(v uint32) uint32 {
	return v<<24 | (v&0xff00)<<8 | (v>>8)&0xff00 | v>>24
}
