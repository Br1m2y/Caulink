package socks5

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// startUDPEcho 起一个回显式 UDP 服务端。
func startUDPEcho(t *testing.T) (string, func()) {
	t.Helper()

	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("启动 UDP 回显服务: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteToUDP(buf[:n], from)
		}
	}()

	return pc.LocalAddr().String(), func() { pc.Close(); <-done }
}

// doUDPAssociate 发送 UDP ASSOCIATE 请求，返回服务端给出的 BND 地址与响应码。
func doUDPAssociate(t *testing.T, ctrl net.Conn) (*net.UDPAddr, byte) {
	t.Helper()

	// DST 全零表示"客户端还没决定用哪个 UDP 端口"，这是绝大多数客户端的做法。
	req := []byte{version5, cmdUDPAssociate, 0, atypIPv4, 0, 0, 0, 0, 0, 0}
	if _, err := ctrl.Write(req); err != nil {
		t.Fatalf("发送 UDP ASSOCIATE: %v", err)
	}

	var head [4]byte
	if _, err := io.ReadFull(ctrl, head[:]); err != nil {
		t.Fatalf("读响应头: %v", err)
	}
	if head[0] != version5 {
		t.Fatalf("响应版本应为 5，实际 %d", head[0])
	}

	host, port, err := readHostPort(ctrl, head[3])
	if err != nil {
		t.Fatalf("读 BND 地址: %v", err)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		t.Fatalf("BND 地址无法解析: %q", host)
	}
	return &net.UDPAddr{IP: ip, Port: int(port)}, head[1]
}

func buildUDPRequest(dst *net.UDPAddr, payload []byte) []byte {
	buf := []byte{0, 0, 0} // RSV + FRAG
	if ip4 := dst.IP.To4(); ip4 != nil {
		buf = append(buf, atypIPv4)
		buf = append(buf, ip4...)
	} else {
		buf = append(buf, atypIPv6)
		buf = append(buf, dst.IP.To16()...)
	}
	buf = binary.BigEndian.AppendUint16(buf, uint16(dst.Port))
	return append(buf, payload...)
}

func newUDPClient(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("创建 UDP 客户端: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestUDPAssociateRoundTrip(t *testing.T) {
	echoAddr, closeEcho := startUDPEcho(t)
	defer closeEcho()

	picker := &fakePicker{entry: contract.IPPoolEntry{
		ID: "lo@127.0.0.1", Interface: "lo", SourceIP: "127.0.0.1",
	}}
	proxyAddr, stop := startProxy(t, picker)
	defer stop()

	ctrl, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	if m := doHandshake(t, ctrl); m != methodNone {
		t.Fatalf("应选择无认证，实际 0x%02x", m)
	}

	bnd, rep := doUDPAssociate(t, ctrl)
	if rep != repSuccess {
		t.Fatalf("UDP ASSOCIATE 应成功，响应码 0x%02x", rep)
	}

	// BND 必须回真实地址端口。回 0.0.0.0:0 是这一层最典型的实现错误：
	// 客户端会照这个地址发包，发到 0.0.0.0 上面什么也出不去。
	if !bnd.IP.IsLoopback() {
		t.Errorf("BND.ADDR 应为回环地址，实际 %s", bnd.IP)
	}
	if bnd.Port == 0 {
		t.Fatal("BND.PORT 为 0，客户端无法知道往哪发")
	}

	uc := newUDPClient(t)

	target, err := net.ResolveUDPAddr("udp", echoAddr)
	if err != nil {
		t.Fatal(err)
	}

	const msg = "udp-hello-caulink"
	if _, err := uc.WriteToUDP(buildUDPRequest(target, []byte(msg)), bnd); err != nil {
		t.Fatalf("发送 UDP 数据: %v", err)
	}

	buf := make([]byte, 4096)
	_ = uc.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := uc.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("等待 UDP 回包: %v", err)
	}

	got, err := parseUDPDatagram(buf[:n])
	if err != nil {
		t.Fatalf("解析回包: %v", err)
	}
	if string(got.payload) != msg {
		t.Fatalf("回显不一致: 期望 %q，实际 %q", msg, got.payload)
	}

	// 回包头里的 DST 应是上游真实地址，客户端靠它匹配会话。
	if got.addr() != echoAddr {
		t.Errorf("回包头 DST 应为 %s，实际 %s", echoAddr, got.addr())
	}

	// UDP 也必须走调度器，且 network 参数要是 udp ——
	// 分类器靠它把 UDP 判成敏感流量。
	calls := picker.calls()
	if len(calls) != 1 || calls[0] != "udp "+echoAddr {
		t.Fatalf("调度器收到的请求不对: %v", calls)
	}
}

// 一个关联里对不同目标要各自建会话，回包不能串线。
func TestUDPAssociateMultipleTargets(t *testing.T) {
	echo1, close1 := startUDPEcho(t)
	defer close1()
	echo2, close2 := startUDPEcho(t)
	defer close2()

	picker := &fakePicker{entry: contract.IPPoolEntry{
		ID: "lo@127.0.0.1", Interface: "lo", SourceIP: "127.0.0.1",
	}}
	proxyAddr, stop := startProxy(t, picker)
	defer stop()

	ctrl, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	doHandshake(t, ctrl)
	bnd, rep := doUDPAssociate(t, ctrl)
	if rep != repSuccess {
		t.Fatalf("响应码 0x%02x", rep)
	}

	uc := newUDPClient(t)

	t1, _ := net.ResolveUDPAddr("udp", echo1)
	t2, _ := net.ResolveUDPAddr("udp", echo2)

	if _, err := uc.WriteToUDP(buildUDPRequest(t1, []byte("to-one")), bnd); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.WriteToUDP(buildUDPRequest(t2, []byte("to-two")), bnd); err != nil {
		t.Fatal(err)
	}

	// 两个回包都要到，且各自对应的目标地址正确。
	seen := map[string]string{}
	buf := make([]byte, 4096)
	for i := 0; i < 2; i++ {
		_ = uc.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _, err := uc.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("第 %d 个回包读取失败: %v", i+1, err)
		}
		dg, err := parseUDPDatagram(buf[:n])
		if err != nil {
			t.Fatalf("解析回包: %v", err)
		}
		seen[dg.addr()] = string(dg.payload)
	}

	if seen[echo1] != "to-one" {
		t.Errorf("目标 %s 的回包应为 to-one，实际 %q", echo1, seen[echo1])
	}
	if seen[echo2] != "to-two" {
		t.Errorf("目标 %s 的回包应为 to-two，实际 %q", echo2, seen[echo2])
	}

	// 两个目标 = 两次选路。同一目标复用会话时不应再次选路。
	if calls := picker.calls(); len(calls) != 2 {
		t.Errorf("两个目标应选路两次，实际 %d 次: %v", len(calls), calls)
	}
}

// 分片报文必须被丢弃，且不建会话。
func TestUDPAssociateDropsFragment(t *testing.T) {
	echoAddr, closeEcho := startUDPEcho(t)
	defer closeEcho()

	picker := &fakePicker{entry: contract.IPPoolEntry{
		ID: "lo@127.0.0.1", Interface: "lo", SourceIP: "127.0.0.1",
	}}
	proxyAddr, stop := startProxy(t, picker)
	defer stop()

	ctrl, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	doHandshake(t, ctrl)
	bnd, _ := doUDPAssociate(t, ctrl)

	uc := newUDPClient(t)
	target, _ := net.ResolveUDPAddr("udp", echoAddr)

	// 把 FRAG 字节改成 1
	pkt := buildUDPRequest(target, []byte("fragmented"))
	pkt[2] = 1
	if _, err := uc.WriteToUDP(pkt, bnd); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 4096)
	_ = uc.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	if _, _, err := uc.ReadFromUDP(buf); err == nil {
		t.Fatal("分片报文不应产生回包")
	}

	if calls := picker.calls(); len(calls) != 0 {
		t.Errorf("分片报文不应触发选路，实际 %v", calls)
	}

	// 不支持的报文不能把关联搞坏：随后正常的包仍要能通。
	if _, err := uc.WriteToUDP(buildUDPRequest(target, []byte("ok")), bnd); err != nil {
		t.Fatal(err)
	}
	_ = uc.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := uc.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("分片丢弃后关联应仍可用: %v", err)
	}
	dg, err := parseUDPDatagram(buf[:n])
	if err != nil || string(dg.payload) != "ok" {
		t.Fatalf("后续正常报文未正确回显: %v %q", err, dg.payload)
	}
}

// 控制连接关闭后，关联必须终止：否则客户端崩溃会留下一个永久免费的 UDP 出口。
func TestUDPAssociateEndsWithControlConn(t *testing.T) {
	echoAddr, closeEcho := startUDPEcho(t)
	defer closeEcho()

	picker := &fakePicker{entry: contract.IPPoolEntry{
		ID: "lo@127.0.0.1", Interface: "lo", SourceIP: "127.0.0.1",
	}}
	proxyAddr, stop := startProxy(t, picker)
	defer stop()

	ctrl, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}

	doHandshake(t, ctrl)
	bnd, _ := doUDPAssociate(t, ctrl)

	uc := newUDPClient(t)
	target, _ := net.ResolveUDPAddr("udp", echoAddr)

	// 先确认关联是可用的
	if _, err := uc.WriteToUDP(buildUDPRequest(target, []byte("before")), bnd); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	_ = uc.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := uc.ReadFromUDP(buf); err != nil {
		t.Fatalf("关联建立后应能收到回包: %v", err)
	}

	ctrl.Close()
	time.Sleep(200 * time.Millisecond)

	if _, err := uc.WriteToUDP(buildUDPRequest(target, []byte("after")), bnd); err != nil {
		t.Fatal(err)
	}

	// 端口已关闭，要么超时要么收到 ICMP 拒绝，两种都算关联已终止。
	_ = uc.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
	if n, _, err := uc.ReadFromUDP(buf); err == nil {
		t.Fatalf("控制连接关闭后不应再有回包，却收到了 %d 字节", n)
	}
}

// 不是本关联的客户端发来的包必须被丢弃。
func TestUDPAssociateIgnoresForeignSender(t *testing.T) {
	echoAddr, closeEcho := startUDPEcho(t)
	defer closeEcho()

	picker := &fakePicker{entry: contract.IPPoolEntry{
		ID: "lo@127.0.0.1", Interface: "lo", SourceIP: "127.0.0.1",
	}}
	proxyAddr, stop := startProxy(t, picker)
	defer stop()

	ctrl, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	doHandshake(t, ctrl)
	bnd, _ := doUDPAssociate(t, ctrl)

	target, _ := net.ResolveUDPAddr("udp", echoAddr)

	owner := newUDPClient(t)
	if _, err := owner.WriteToUDP(buildUDPRequest(target, []byte("owner")), bnd); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	_ = owner.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := owner.ReadFromUDP(buf); err != nil {
		t.Fatalf("首个客户端应能收到回包: %v", err)
	}

	// 另一个 socket 试图搭便车
	foreign := newUDPClient(t)
	if _, err := foreign.WriteToUDP(buildUDPRequest(target, []byte("foreign")), bnd); err != nil {
		t.Fatal(err)
	}
	_ = foreign.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if n, _, err := foreign.ReadFromUDP(buf); err == nil {
		t.Fatalf("非关联客户端不应收到回包，却收到了 %d 字节", n)
	}
}
