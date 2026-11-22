// Package v1 defines the frozen v1 data contracts shared by every Caulink
// module: auth layer (A), proxy & scheduling (B), strategy & UI (C).
//
// Rules for this package:
//  1. Types, constants and validation helpers only. No business logic.
//  2. Standard library only. No third-party imports.
//  3. Must never import any internal/* package.
//  4. Any change requires approval from all three module owners.
//
// Known v1 limitations (changes must go through v2):
//   - AuthStatus has three states only; a first-time sign-in in progress is
//     reported as offline, so the UI cannot tell "never connected" from
//     "connecting now".
//   - There is no AccountID: one campus account per source IP is assumed.
//     Entries are identified by ID alone, so multiple accounts sharing an
//     interface are out of scope.
//   - There is no generation/version field: the pool is written and read
//     inside one process (caulinkd), so an RWMutex snapshot is sufficient.
//     Cross-process sharing would require a new field and a v2 contract.
package v1

import (
	"fmt"
	"net"
	"time"
)

// AuthStatus is the authentication state of a single source IP.
// The empty value is deliberately invalid: every entry must declare its state.
type AuthStatus string

const (
	// AuthOnline means the source IP currently holds a valid campus session
	// and may be used by the scheduler.
	AuthOnline AuthStatus = "online"
	// AuthOffline means no valid session. Terminal until auth layer retries.
	AuthOffline AuthStatus = "offline"
	// AuthReauthenticating means a re-login attempt is in flight. The
	// scheduler must treat this IP as unusable during this state.
	AuthReauthenticating AuthStatus = "reauthenticating"
)

// Valid reports whether s is one of the three defined states.
func (s AuthStatus) Valid() bool {
	switch s {
	case AuthOnline, AuthOffline, AuthReauthenticating:
		return true
	default:
		return false
	}
}

// IPPoolEntry describes one schedulable source IP.
//
// Ownership: written only by the auth layer via internal/ippool; every other
// module consumes read-only snapshots. Entries are compared by ID.
//
// Unit conventions:
//   - RTTMs is milliseconds.
//   - LossRate and HealthScore are ratios in [0, 1], not percentages.
//   - LastSeen is UTC and serialized as RFC3339.
type IPPoolEntry struct {
	// ID is the stable identity of this pool entry, conventionally
	// "<interface>@<source_ip>" (e.g. "eth0@10.3.21.7"). Never change it for
	// the lifetime of the underlying binding; recreate the entry instead.
	ID string `json:"id"`

	// Interface is the OS interface name carrying SourceIP, e.g. "eth0",
	// "en0", "WLAN". The Go field cannot be lower-cased to "interface"
	// because that is a Go keyword; the JSON key stays "interface".
	Interface string `json:"interface"`

	// SourceIP is the bare IP address bound as the connection source.
	// No port, no zone suffix, no CIDR mask.
	SourceIP string `json:"source_ip"`
	// SourceIP6 是同一块网卡上的全局 IPv6 地址，用于连接 IPv6 对等体。
	//
	// 为什么必须单独存：一个 TCP socket 的源地址族必须与目标一致。
	// 校园网的 IPv6 资源很多（BT 对等体大量是 v6），只有 v4 源时
	// 那些连接会全部失败并报 WSAEADDRNOTAVAIL。
	// 为空表示该网卡没有可用的全局 IPv6。
	SourceIP6 string `json:"source_ip6,omitempty"`

	// AuthStatus is the campus network session state of SourceIP.
	AuthStatus AuthStatus `json:"auth_status"`

	// HealthScore is the scheduler weight in [0, 1]; 1.0 is best, 0 means
	// "not schedulable regardless of AuthStatus". The scoring formula lives
	// in internal/health, not here.
	HealthScore float64 `json:"health_score"`

	// RTTMs is the smoothed round-trip time in milliseconds. 0 means unknown.
	RTTMs float64 `json:"rtt_ms"`

	// LossRate is the observed packet loss ratio in [0, 1]. 0 means unknown.
	LossRate float64 `json:"loss_rate"`

	// LastSeen is the last moment the IP was confirmed alive. Must be UTC.
	LastSeen time.Time `json:"last_seen"`
}

// Schedulable reports whether this entry may receive new connections.
// It is the minimum bar; the scheduler may apply stricter checks.
func (e IPPoolEntry) Schedulable() bool {
	return e.AuthStatus == AuthOnline && e.HealthScore > 0
}

// Validate checks structural invariants. Call it at every producer boundary
// (auth layer before writing into the pool) and in tests.
func (e IPPoolEntry) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("contract: id is empty")
	}
	if len(e.ID) > 128 {
		return fmt.Errorf("contract: id too long (%d)", len(e.ID))
	}
	if e.Interface == "" {
		return fmt.Errorf("contract: interface is empty for id %q", e.ID)
	}
	ip := net.ParseIP(e.SourceIP)
	if ip == nil {
		return fmt.Errorf("contract: source_ip %q is not a valid IP (id %q)", e.SourceIP, e.ID)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("contract: source_ip %q is unspecified (id %q)", e.SourceIP, e.ID)
	}
	if !e.AuthStatus.Valid() {
		return fmt.Errorf("contract: invalid auth_status %q (id %q)", e.AuthStatus, e.ID)
	}
	if e.HealthScore < 0 || e.HealthScore > 1 {
		return fmt.Errorf("contract: health_score %v out of [0,1] (id %q)", e.HealthScore, e.ID)
	}
	if e.RTTMs < 0 {
		return fmt.Errorf("contract: rtt_ms %v is negative (id %q)", e.RTTMs, e.ID)
	}
	if e.LossRate < 0 || e.LossRate > 1 {
		return fmt.Errorf("contract: loss_rate %v out of [0,1] (id %q)", e.LossRate, e.ID)
	}
	if e.LastSeen.IsZero() {
		return fmt.Errorf("contract: last_seen is zero (id %q)", e.ID)
	}
	return nil
}
