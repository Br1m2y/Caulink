// Package dialer 把调度决策落到 socket 上：从指定的源 IP 发起连接。
//
// 调度器只负责"选哪个 IP"，本包负责"真的从那个 IP 出去"。两者分开是为了让
// 调度逻辑可以脱离网络单测 —— 否则测一次选路就要真的建连接。
package dialer

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Br1m2y/caulink/internal/netsrc"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// Dialer 按源 IP 建立连接。
type Dialer struct {
	// Timeout 是建连超时，默认 10s。
	Timeout time.Duration

	// KeepAlive 是 TCP 保活间隔，默认 30s。0 表示使用系统默认。
	KeepAlive time.Duration

	// OnFailure 在**从某个源 IP 建连失败**时被调用，用于事件驱动的重认证：
	// 代理层是唯一能实时看见"上游连不上"的地方，比轮询更早发现问题。
	//
	// 实现方需要自行去抖：高并发下载时失败会成串出现，直接触发登录会打爆门户。
	// 回调在拨号路径上同步执行，务必保持非阻塞（丢进缓冲 channel 即可）。
	OnFailure func(sourceIP string)
}

func (d Dialer) timeout() time.Duration {
	if d.Timeout <= 0 {
		return 10 * time.Second
	}
	return d.Timeout
}

func (d Dialer) keepAlive() time.Duration {
	if d.KeepAlive <= 0 {
		return 30 * time.Second
	}
	return d.KeepAlive
}

// DialContext 从 entry.SourceIP 发起连接到 address。
//
// 失败时**绝不**回退到不绑定源地址的默认拨号。
//
// 这条是硬规则：一旦回退，所有流量会静默回到默认路由，多 IP 调度就形同虚设，
// 而现象是"程序看起来在跑、速度就是上不去"，排查成本极高。
// 让调用方拿到错误、由调度层换一个 IP，比悄悄降级诚实得多。
func (d Dialer) DialContext(ctx context.Context, network, address string, entry contract.IPPoolEntry) (net.Conn, error) {
	// 源地址必须与目标地址族一致，否则内核直接拒绝（WSAEADDRNOTAVAIL）。
	// 校园网 IPv6 对等体很多，这一步不做的话它们会全部连不上。
	target, err := netsrc.Resolve(ctx, address, entry)
	if err != nil {
		return nil, fmt.Errorf("dialer: %s(%s) → %s: %w", entry.SourceIP, entry.Interface, address, err)
	}

	// 调用方给的 network 可能是 "tcp"，这里以 Resolve 的结果为准（tcp4/tcp6）。
	nd := &net.Dialer{
		Timeout:   d.timeout(),
		KeepAlive: d.keepAlive(),
		// LocalAddr 只管源 IP，管不了包从哪块网卡出去。Windows 双网卡同网段时
		// 内核按目的地址选路，会从 metric 更低的那块发出，源 IP 却属于另一块，
		// 交换机按源 IP/MAC 不匹配丢弃 —— 现象是第二条线路"连上了但没速度"。
		// Control 补上的就是这一半；非 Windows 平台它是 nil（内核按源地址反查路由）。
		Control: netsrc.DialControl(target.Source),
	}

	switch {
	case strings.HasPrefix(network, "tcp"):
		nd.LocalAddr = &net.TCPAddr{IP: target.Source}
	case strings.HasPrefix(network, "udp"):
		// UDP 也是一样的族约束；这里按目标族构造本地地址。
		if target.Source.To4() != nil {
			nd.LocalAddr = &net.UDPAddr{IP: target.Source}
		} else {
			nd.LocalAddr = &net.UDPAddr{IP: target.Source}
		}
		// UDP 不做域名解析后的重写：交给内核按原目标处理。
		target.Network = network
		target.Addr = address
	default:
		return nil, fmt.Errorf("dialer: 不支持的 network %q（只支持 tcp*/udp*）", network)
	}

	conn, err := nd.DialContext(ctx, target.Network, target.Addr)
	if err != nil {
		// 只在 TCP 建连失败时上报：UDP 的"失败"多为目标不响应，噪声太大。
		if d.OnFailure != nil && strings.HasPrefix(network, "tcp") {
			d.OnFailure(entry.SourceIP)
		}
		return nil, fmt.Errorf("dialer: 从 %s(%s) 连接 %s 失败: %w",
			target.Source, entry.Interface, address, err)
	}
	return conn, nil
}

// Dial 是 DialContext 的无超时控制版本，使用 Dialer 自身的 Timeout。
func (d Dialer) Dial(network, address string, entry contract.IPPoolEntry) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address, entry)
}
