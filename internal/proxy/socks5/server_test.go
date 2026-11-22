package socks5

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Br1m2y/caulink/internal/proxy/dialer"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

type fakePicker struct {
	mu    sync.Mutex
	entry contract.IPPoolEntry
	err   error
	seen  []string
}

func (f *fakePicker) Pick(network, addr string) (contract.IPPoolEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, network+" "+addr)
	return f.entry, f.err
}

func (f *fakePicker) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.seen))
	copy(out, f.seen)
	return out
}

func startEcho(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String(), func() { ln.Close(); <-done }
}

func startProxy(t *testing.T, picker Picker) (addr string, stop func()) {
	t.Helper()

	s := &Server{
		ListenAddr:       "127.0.0.1:0",
		Picker:           picker,
		Dialer:           dialer.Dialer{Timeout: 2 * time.Second},
		HandshakeTimeout: 2 * time.Second,
	}
	if err := s.validate(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	stopFn := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("服务未能及时退出")
		}
	}
	return ln.Addr().String(), stopFn
}

// doHandshake 完成方法协商，返回服务端选定的方法。
func doHandshake(t *testing.T, c net.Conn) byte {
	t.Helper()
	if _, err := c.Write([]byte{5, 1, methodNone}); err != nil {
		t.Fatal(err)
	}
	var resp [2]byte
	if _, err := io.ReadFull(c, resp[:]); err != nil {
		t.Fatalf("读握手响应: %v", err)
	}
	if resp[0] != version5 {
		t.Fatalf("响应版本应为 5，实际 %d", resp[0])
	}
	return resp[1]
}

// sendConnect 发送 CONNECT 请求并读回响应码。
func sendConnect(t *testing.T, c net.Conn, target string) byte {
	t.Helper()

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		t.Fatalf("测试目标需为 IP 字面量: %s", target)
	}
	port, _ := strconv.Atoi(portStr)

	req := []byte{5, cmdConnect, 0, atypIPv4}
	req = append(req, ip.To4()...)
	req = append(req, byte(port>>8), byte(port))

	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}

	var resp [10]byte
	if _, err := io.ReadFull(c, resp[:]); err != nil {
		t.Fatalf("读响应: %v", err)
	}
	if resp[0] != version5 {
		t.Fatalf("响应版本应为 5，实际 %d", resp[0])
	}
	return resp[1]
}

func TestConnectAndForward(t *testing.T) {
	target, closeEcho := startEcho(t)
	defer closeEcho()

	picker := &fakePicker{entry: contract.IPPoolEntry{
		ID: "lo@127.0.0.1", Interface: "lo", SourceIP: "127.0.0.1",
	}}
	proxyAddr, stop := startProxy(t, picker)
	defer stop()

	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if m := doHandshake(t, c); m != methodNone {
		t.Fatalf("应选择无认证，实际 0x%02x", m)
	}
	if rep := sendConnect(t, c, target); rep != repSuccess {
		t.Fatalf("CONNECT 应成功，响应码 0x%02x", rep)
	}

	// 数据必须能双向透传
	const msg = "hello-caulink"
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("读回显: %v", err)
	}
	if string(buf) != msg {
		t.Fatalf("回显不一致: %q", buf)
	}

	// 调度器必须收到原始目标地址，否则选路依据就是错的
	calls := picker.calls()
	if len(calls) != 1 || calls[0] != "tcp "+target {
		t.Fatalf("调度器收到的目标不对: %v", calls)
	}
}

func TestRejectsClientWithoutNoAuthMethod(t *testing.T) {
	picker := &fakePicker{}
	proxyAddr, stop := startProxy(t, picker)
	defer stop()

	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// 只提供用户名密码认证（0x02），服务端应回 0xFF
	if _, err := c.Write([]byte{5, 1, 0x02}); err != nil {
		t.Fatal(err)
	}
	var resp [2]byte
	if _, err := io.ReadFull(c, resp[:]); err != nil {
		t.Fatal(err)
	}
	if resp[1] != methodNoAcceptable {
		t.Fatalf("应回应 0xFF（无可用方法），实际 0x%02x", resp[1])
	}
}

func TestRejectsNonConnectCommand(t *testing.T) {
	picker := &fakePicker{entry: contract.IPPoolEntry{ID: "x", Interface: "lo", SourceIP: "127.0.0.1"}}
	proxyAddr, stop := startProxy(t, picker)
	defer stop()

	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	doHandshake(t, c)

	// CMD=0x02 (BIND)，MVP 不支持，应回 0x07
	req := []byte{5, cmdBind, 0, atypIPv4, 127, 0, 0, 1, 0, 80}
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}
	var resp [10]byte
	if _, err := io.ReadFull(c, resp[:]); err != nil {
		t.Fatal(err)
	}
	if resp[1] != repCommandNotSupported {
		t.Fatalf("应回应 0x07（命令不支持），实际 0x%02x", resp[1])
	}
}

func TestNoCandidateReturnsNetworkUnreachable(t *testing.T) {
	picker := &fakePicker{err: errors.New("没有可调度的源 IP")}
	proxyAddr, stop := startProxy(t, picker)
	defer stop()

	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	doHandshake(t, c)
	if rep := sendConnect(t, c, "127.0.0.1:9"); rep != repNetworkUnreachable {
		t.Fatalf("无可用 IP 时应回 0x03，实际 0x%02x", rep)
	}
}

func TestDialFailureReturnsRefused(t *testing.T) {
	// 拿一个已关闭的端口当目标，拨号必然被拒
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := ln.Addr().String()
	ln.Close()

	picker := &fakePicker{entry: contract.IPPoolEntry{
		ID: "lo@127.0.0.1", Interface: "lo", SourceIP: "127.0.0.1",
	}}
	proxyAddr, stop := startProxy(t, picker)
	defer stop()

	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	doHandshake(t, c)
	rep := sendConnect(t, c, deadAddr)
	if rep != repConnectionRefused {
		t.Fatalf("端口关闭时应回 0x05（连接被拒），实际 0x%02x", rep)
	}
}

// 只允许监听回环地址。这是合规边界，不是配置项。
func TestRefusesNonLoopbackListen(t *testing.T) {
	cases := []string{"0.0.0.0:1080", "192.168.1.5:1080", ":1080"}
	for _, addr := range cases {
		s := &Server{
			ListenAddr: addr,
			Picker:     &fakePicker{},
			Dialer:     dialer.Dialer{},
		}
		if err := s.validate(); err == nil {
			t.Errorf("监听 %s 应被拒绝（只允许回环地址）", addr)
		}
	}
}

func TestValidateRequiresDependencies(t *testing.T) {
	s := &Server{ListenAddr: "127.0.0.1:1080", Dialer: dialer.Dialer{}}
	if err := s.validate(); err == nil {
		t.Error("缺少 Picker 应报错")
	}
	s = &Server{ListenAddr: "127.0.0.1:1080", Picker: &fakePicker{}}
	if err := s.validate(); err == nil {
		t.Error("缺少 Dialer 应报错")
	}
}
