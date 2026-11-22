package control

import (
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// 响应结构就是 B → C 的接口契约，字段名一旦发出就不该轻易改。
// 增字段是兼容的（客户端忽略未知字段），改名和删字段不是。
// 详细约定见 docs/status-api.md。

// StatusResponse 是 GET /api/v1/status 的完整响应。
type StatusResponse struct {
	// Version 是构建版本，用来在 UI 上区分"跑的是哪一版"。
	Version string `json:"version"`

	// StartedAt 是进程内状态服务的启动时刻（UTC，RFC3339）。
	StartedAt time.Time `json:"started_at"`

	// UptimeSeconds 是运行时长，秒。用浮点是因为 UI 要显示到 0.1 秒。
	UptimeSeconds float64 `json:"uptime_seconds"`

	// Socks5 是代理监听地址，例如 "127.0.0.1:1080"。
	Socks5 string `json:"socks5"`

	// HTTPProxy 是可选 HTTP 代理的监听地址；未启用时为空。
	// Steam 这类只支持 HTTP 代理的客户端用它走多 IP 调度。
	HTTPProxy string `json:"http_proxy,omitempty"`

	// IPs 是按 ID 排序的源 IP 列表，顺序稳定，UI 可以直接按序渲染。
	IPs []IPStatus `json:"ips"`

	Scheduler SchedulerStatus `json:"scheduler"`
	Totals    Totals          `json:"totals"`
}

// IPStatus 是单个源 IP 的链路状态 + 流量统计。
//
// 内嵌 contract.IPPoolEntry 是为了让字段名与契约完全一致：
// id / interface / source_ip / auth_status / health_score / rtt_ms /
// loss_rate / last_seen 由契约决定，下面的字段是本接口的附加信息。
type IPStatus struct {
	contract.IPPoolEntry

	// InPool 表示当前是否可以接收新连接（认证在线且健康分 > 0）。
	// 与 entry.Schedulable() 同义，序列化出来是为了 UI 不必重复实现判断逻辑。
	InPool bool `json:"schedulable"`

	// Weight 是调度权重（0–100 的整数）。只在 InPool 为 true 时有意义。
	// UI 可以用它画"这份流量会怎么分"的饼图，不必自己按健康分重算。
	Weight int `json:"weight"`

	// ActiveConns 是当前活跃连接数；对 UDP 是活跃会话数。
	ActiveConns int64 `json:"active_conns"`

	// TotalConns 是进程启动以来该 IP 累计承载的连接数。
	TotalConns uint64 `json:"total_conns"`

	// BytesUp / BytesDown 是累计字节数。UDP 只统计载荷，不含 SOCKS5 头。
	BytesUp   uint64 `json:"bytes_up"`
	BytesDown uint64 `json:"bytes_down"`

	// RateUpBps / RateDownBps 是平滑后的瞬时速率，单位字节/秒。
	// UI 想显示 MB/s 自己除 1e6，不要在服务端做单位换算 ——
	// 一旦服务端固定了单位，客户端就没法换成人性化格式。
	RateUpBps   float64 `json:"rate_up_bps"`
	RateDownBps float64 `json:"rate_down_bps"`

	// Errors 是该 IP 上建连失败与转发异常的累计次数。
	// 用来区分"链路慢"和"链路在报错"，这两种情况在 UI 上要给出不同提示。
	Errors uint64 `json:"errors"`
}

// SchedulerStatus 描述当前调度策略的状态。
type SchedulerStatus struct {
	// Strategy 是策略名，目前固定 "smooth-weighted-round-robin"。
	Strategy string `json:"strategy"`

	// StickyID 是敏感流量（游戏、会议、语音）当前绑定的源 IP 条目 ID，
	// 为空表示还没有敏感流量走过。
	StickyID string `json:"sticky_id,omitempty"`

	// StickyIP 是 StickyID 对应的 IP，省得 UI 再去 IPs 里查。
	StickyIP string `json:"sticky_ip,omitempty"`

	// Candidates 是当前可调度的 IP 数量。为 0 时所有新连接都会失败，
	// UI 应该直接报警而不是显示"正常运行"。
	Candidates int `json:"candidates"`
}

// Totals 是全局汇总，UI 顶部的总速率、总连接数直接用它。
type Totals struct {
	IPCount          int     `json:"ip_count"`
	SchedulableCount int     `json:"schedulable_count"`
	ActiveConns      int64   `json:"active_conns"`
	TotalConns       uint64  `json:"total_conns"`
	BytesUp          uint64  `json:"bytes_up"`
	BytesDown        uint64  `json:"bytes_down"`
	RateUpBps        float64 `json:"rate_up_bps"`
	RateDownBps      float64 `json:"rate_down_bps"`
	Errors           uint64  `json:"errors"`
}
