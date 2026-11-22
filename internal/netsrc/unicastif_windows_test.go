//go:build windows

package netsrc

import (
	"context"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHtonl(t *testing.T) {
	cases := []struct {
		in   uint32
		want uint32
	}{
		{0, 0},
		{1, 0x01000000},
		{0x01020304, 0x04030201},
		{0xff, 0xff000000},
	}
	for _, c := range cases {
		if got := htonl(c.in); got != c.want {
			t.Errorf("htonl(%#x) = %#x，期望 %#x", c.in, got, c.want)
		}
	}
}

func TestNetworkFamily(t *testing.T) {
	cases := []struct {
		network string
		v6      bool
		known   bool
	}{
		{"tcp4", false, true},
		{"tcp6", true, true},
		{"udp4", false, true},
		{"udp6", true, true},
		{"tcp", false, false},
		{"udp", false, false},
	}
	for _, c := range cases {
		v6, known := networkFamily(c.network)
		if v6 != c.v6 || known != c.known {
			t.Errorf("networkFamily(%q) = (%v,%v)，期望 (%v,%v)", c.network, v6, known, c.v6, c.known)
		}
	}
}

// DialControl(nil) 必须什么都不做：源地址没绑的时候钉出口网卡毫无意义。
func TestDialControlNilSource(t *testing.T) {
	if DialControl(nil) != nil {
		t.Fatal("src 为 nil 时应返回 nil")
	}
}

// 端到端的回归测试：真的建一条连接。
//
// 选项编号或字节序写错时，setsockopt 会失败（编号错）或建连超时（字节序错），
// 两种情况这个测试都能抓到 —— 这正是双网卡场景里最难靠肉眼发现的部分。
func TestDialControlBindsLoopback(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close()
		}
	}()

	src := net.ParseIP("127.0.0.1")
	d := net.Dialer{
		Timeout:   2 * time.Second,
		LocalAddr: &net.TCPAddr{IP: src},
		Control:   DialControl(src),
	}
	conn, err := d.DialContext(context.Background(), "tcp4", ln.Addr().String())
	if err != nil {
		t.Fatalf("带 Control 的拨号失败: %v", err)
	}
	_ = conn.Close()
}

// 源地址不在本机时必须**报错**，不能悄悄按默认路由拨出去。
//
// 静默降级会让"第二个 IP 用不起来"表现成"第二条线路没速度"，排查成本极高。
func TestDialControlUnknownSourceFails(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	src := net.ParseIP("192.0.2.77") // TEST-NET-1，保证本机不持有
	d := net.Dialer{
		Timeout:   500 * time.Millisecond,
		LocalAddr: &net.TCPAddr{IP: src},
		Control:   DialControl(src),
	}
	_, err = d.DialContext(context.Background(), "tcp4", ln.Addr().String())
	if err == nil {
		t.Fatal("源地址不属于本机时应报错")
	}
	if !strings.Contains(err.Error(), "找不到持有源地址") {
		t.Fatalf("错误信息应指出找不到网卡，实际: %v", err)
	}
}

// v4 与 v6 走的是两个不同级别、不同选项号的 setsockopt，必须分别验一遍，
// 否则 v6 的常量写错要到真实 IPv6 目标上才暴露。
func TestBindToInterfaceOnUDP(t *testing.T) {
	idx, ok := lookupInterfaceIndex(net.ParseIP("127.0.0.1"))
	if !ok {
		t.Skip("本机没有报告 127.0.0.1 所属网卡，跳过")
	}

	cases := []struct {
		network string
		addr    string
		v6      bool
	}{
		{"udp4", "127.0.0.1:0", false},
		{"udp6", "[::1]:0", true},
	}
	for _, c := range cases {
		pc, err := net.ListenPacket(c.network, c.addr)
		if err != nil {
			t.Skipf("%s 不可用: %v", c.network, err)
		}
		raw, err := pc.(syscall.Conn).SyscallConn()
		if err != nil {
			pc.Close()
			t.Fatalf("%s: 取 SyscallConn 失败: %v", c.network, err)
		}
		err = bindToInterface(raw, idx, c.v6)
		pc.Close()
		if err != nil {
			t.Fatalf("%s: bindToInterface 失败: %v", c.network, err)
		}
	}
}

// 缓存要有内容，也不能把不同的源地址混成一个。
func TestInterfaceIndexCaches(t *testing.T) {
	ifIndexMu.Lock()
	ifIndexCache = make(map[string]ifIndexEntry)
	ifIndexMu.Unlock()

	idx1, ok1 := interfaceIndex(net.ParseIP("127.0.0.1"))
	if !ok1 {
		t.Skip("本机没有报告 127.0.0.1 所属网卡，跳过")
	}
	idx2, ok2 := interfaceIndex(net.ParseIP("127.0.0.1"))
	if !ok2 || idx1 != idx2 {
		t.Fatalf("两次查询结果不一致: (%d,%v) / (%d,%v)", idx1, ok1, idx2, ok2)
	}

	ifIndexMu.Lock()
	n := len(ifIndexCache)
	_, cached := ifIndexCache["127.0.0.1"]
	ifIndexMu.Unlock()
	if n == 0 || !cached {
		t.Fatalf("查询后应写入缓存，实际缓存项 %d", n)
	}
}
