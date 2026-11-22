package netsrc

import (
	"context"
	"errors"
	"net"
	"testing"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

func entryWith(v4, v6 string) contract.IPPoolEntry {
	return contract.IPPoolEntry{ID: "test", Interface: "eth0", SourceIP: v4, SourceIP6: v6}
}

// IPv6 字面量必须走 v6 源：用 v4 源连 v6 目标会被内核以
// WSAEADDRNOTAVAIL 拒绝，这正是校园网里 BT 对等体全连不上的原因。
func TestResolveIPv6LiteralUsesIPv6Source(t *testing.T) {
	e := entryWith("10.6.18.3", "2001:250:209:2000::1")

	got, err := Resolve(context.Background(), "[2402:f000:1:400::2]:80", e)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Network != "tcp6" {
		t.Errorf("network = %q, want tcp6", got.Network)
	}
	if got.Source.String() != "2001:250:209:2000::1" {
		t.Errorf("source = %s, want 2001:250:209:2000::1", got.Source)
	}
	if got.Addr != "[2402:f000:1:400::2]:80" {
		t.Errorf("addr = %q", got.Addr)
	}
}

// 没有 v6 源的条目遇到 v6 目标时必须报错，而不是回退到默认路由。
func TestResolveIPv6LiteralWithoutSourceFails(t *testing.T) {
	e := entryWith("10.6.18.3", "")

	_, err := Resolve(context.Background(), "[2402:f000:1:400::2]:80", e)
	if !errors.Is(err, ErrNoSourceForFamily) {
		t.Fatalf("err = %v, want ErrNoSourceForFamily", err)
	}
}

// IPv4 字面量仍走 v4 源。
func TestResolveIPv4Literal(t *testing.T) {
	e := entryWith("10.6.18.3", "2001:250:209:2000::1")

	got, err := Resolve(context.Background(), "101.6.15.130:443", e)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Network != "tcp4" {
		t.Errorf("network = %q, want tcp4", got.Network)
	}
	if got.Source.String() != "10.6.18.3" {
		t.Errorf("source = %s", got.Source)
	}
}

// 域名两边都有地址时，优先 IPv4（更稳）。
func TestResolveDomainPrefersIPv4(t *testing.T) {
	e := entryWith("10.6.18.3", "2001:250:209:2000::1")

	got, err := Resolve(context.Background(), "localhost:80", e)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Network != "tcp4" {
		t.Errorf("network = %q, want tcp4（域名有条目可用 v4 时应优先 v4）", got.Network)
	}
}

// 只有 v6 源的条目遇到 IPv4 目标必须报错。
//
// 这里不做"域名解析后自动换族"的断言：localhost 之类的名字在不同机器上
// 解析结果不同（::1 是回环，不算全局 IPv6），写进单测会变成依赖环境。
// 域名换族的逻辑由 Resolve 的分支顺序保证，v4/v6 字面量用例已经覆盖。
func TestResolveIPv4LiteralWithoutSourceFails(t *testing.T) {
	e := entryWith("", "2001:250:209:2000::1")

	_, err := Resolve(context.Background(), "101.6.15.130:443", e)
	if !errors.Is(err, ErrNoSourceForFamily) {
		t.Fatalf("err = %v, want ErrNoSourceForFamily", err)
	}
}
func TestWantsIPv6(t *testing.T) {
	cases := []struct {
		addr  string
		want  bool
		known bool
	}{
		{"[2402:f000:1:400::2]:80", true, true},
		{"101.6.15.130:443", false, true},
		{"example.com:80", false, false},
		{"not-a-hostport", false, false},
	}
	for _, c := range cases {
		got, known := WantsIPv6(c.addr)
		if got != c.want || known != c.known {
			t.Errorf("WantsIPv6(%q) = (%v,%v), want (%v,%v)", c.addr, got, known, c.want, c.known)
		}
	}
}

func TestHasFamily(t *testing.T) {
	both := entryWith("10.6.18.3", "2001:250:209:2000::1")
	if !HasFamily(both, false) || !HasFamily(both, true) {
		t.Error("双栈条目应同时支持 v4 与 v6")
	}
	only4 := entryWith("10.6.18.3", "")
	if HasFamily(only4, true) {
		t.Error("只有 v4 的条目不应声称支持 v6")
	}
	var _ net.IP // 保持 net 导入用于类型断言场景
}
