package scheduler

import (
	"net"
	"strconv"
	"strings"
)

// Class 是流量类别。MVP 只分两类：
// 敏感（游戏、会议、实时语音）和批量（下载、网页）。
//
// 不做过细的分类：规则越多越难解释，也越容易在错误的方向上优化。
// 《要求.md》§3 明确"规则可解释优先，先规则引擎后极简分类器"。
type Class string

const (
	// ClassInteractive 是需要稳定低延迟的流量，固定走当前最优 IP。
	ClassInteractive Class = "interactive"
	// ClassBulk 是能从多 IP 分摊中获益的流量。
	ClassBulk Class = "bulk"
)

// Classifier 判定一条连接的类别。
type Classifier interface {
	Classify(network, addr string) Class
}

// ClassifyFunc 让普通函数满足 Classifier，便于测试与上层替换。
type ClassifyFunc func(network, addr string) Class

func (f ClassifyFunc) Classify(network, addr string) Class { return f(network, addr) }

// Rules 是基于端口与域名后缀的规则分类器。
//
// 选择端口而不是进程名：SOCKS5 层拿不到进程信息，而端口在握手时就有。
// 代价是端口推断会漏判（很多会议软件最终走 443），这一点在演示时要讲清楚，
// 而不是假装它很准。
type Rules struct {
	// InteractivePorts 是命中即视为敏感的端口集合。
	InteractivePorts map[int]bool

	// InteractiveSuffixes 是域名后缀，如 "zoom.us"。仅当 addr 是域名时生效。
	InteractiveSuffixes []string

	// TreatUDPAsInteractive 决定 UDP 是否一律视为敏感流量。
	//
	// 默认 true：UDP 无法重传，丢包直接体现为卡顿/掉帧，
	// 而且游戏与语音几乎都走 UDP。代价是 DNS/QUIC 也会走固定 IP，
	// 但这部分流量很小，不影响吞吐。
	TreatUDPAsInteractive bool
}

// DefaultRules 返回一套保守的默认规则。
func DefaultRules() Rules {
	ports := []int{
		3478, 3479, 3480, 3481, // STUN / TURN，多数会议与游戏用它打洞
		19302, 19303, // Google STUN
		3074,       // Xbox Live
		5060, 5061, // SIP
		5004, 5005, // RTP
		8801, 8802, 8803, // Zoom
		19132, 19133, // Minecraft Bedrock
	}
	// Source 引擎游戏（CS、Dota2、TF2 等）占用 27015-27030
	for p := 27015; p <= 27030; p++ {
		ports = append(ports, p)
	}

	m := make(map[int]bool, len(ports))
	for _, p := range ports {
		m[p] = true
	}

	return Rules{
		InteractivePorts:      m,
		InteractiveSuffixes:   []string{"zoom.us", "tencentmeeting.com", "meeting.qq.com", "teams.microsoft.com", "discord.com", "discord.gg"},
		TreatUDPAsInteractive: true,
	}
}

// Classify 实现 Classifier。
func (r Rules) Classify(network, addr string) Class {
	if r.TreatUDPAsInteractive && strings.HasPrefix(strings.ToLower(network), "udp") {
		return ClassInteractive
	}

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// 拿不到端口就不猜，按批量处理。分类错误只会影响调度效果，
		// 但在这里 panic 会直接断掉连接。
		return ClassBulk
	}

	if port, err := strconv.Atoi(portStr); err == nil && r.InteractivePorts[port] {
		return ClassInteractive
	}

	host = strings.ToLower(host)
	for _, suffix := range r.InteractiveSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return ClassInteractive
		}
	}

	return ClassBulk
}
