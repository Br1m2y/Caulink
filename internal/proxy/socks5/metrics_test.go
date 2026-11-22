package socks5

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Br1m2y/caulink/internal/proxy/dialer"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// fakeMetrics 记录回调，用来验证数据面真的上报了统计。
type fakeMetrics struct {
	mu     sync.Mutex
	opened map[string]int
	closed map[string]int
	up     map[string]int64
	down   map[string]int64
	errs   map[string]int
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{
		opened: map[string]int{},
		closed: map[string]int{},
		up:     map[string]int64{},
		down:   map[string]int64{},
		errs:   map[string]int{},
	}
}

func (f *fakeMetrics) ConnOpened(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened[id]++
}

func (f *fakeMetrics) ConnClosed(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed[id]++
}

func (f *fakeMetrics) AddUp(id string, n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.up[id] += n
}

func (f *fakeMetrics) AddDown(id string, n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down[id] += n
}

func (f *fakeMetrics) AddError(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[id]++
}

func (f *fakeMetrics) get(id string) (opened, closed int, up, down int64, errs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opened[id], f.closed[id], f.up[id], f.down[id], f.errs[id]
}

func startProxyWithMetrics(t *testing.T, m Metrics) (string, func()) {
	t.Helper()

	s := &Server{
		ListenAddr:       "127.0.0.1:0",
		Picker:           &fakePicker{entry: contract.IPPoolEntry{ID: "lo@127.0.0.1", Interface: "lo", SourceIP: "127.0.0.1"}},
		Dialer:           dialer.Dialer{Timeout: 2 * time.Second},
		Metrics:          m,
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

	return ln.Addr().String(), func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("服务未能及时退出")
		}
	}
}

// waitFor 轮询直到条件成立。计数在连接关闭后异步落到回调里，
// 直接断言会拿到"还没减"的中间态。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

func TestMetricsCountsConnectTraffic(t *testing.T) {
	target, closeEcho := startEcho(t)
	defer closeEcho()

	m := newFakeMetrics()
	proxyAddr, stop := startProxyWithMetrics(t, m)
	defer stop()

	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}

	doHandshake(t, c)
	if rep := sendConnect(t, c, target); rep != repSuccess {
		t.Fatalf("CONNECT 失败 0x%02x", rep)
	}

	const msg = "0123456789abcdef" // 16 字节，双向各一次
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("读回显: %v", err)
	}

	c.Close()

	const id = "lo@127.0.0.1"
	waitFor(t, "连接关闭计数归位", func() bool {
		_, closed, _, _, _ := m.get(id)
		return closed == 1
	})

	opened, closed, up, down, errs := m.get(id)
	if opened != 1 {
		t.Errorf("ConnOpened = %d，期望 1", opened)
	}
	if closed != 1 {
		t.Errorf("ConnClosed = %d，期望 1", closed)
	}
	if up != int64(len(msg)) {
		t.Errorf("上行字节 = %d，期望 %d", up, len(msg))
	}
	if down != int64(len(msg)) {
		t.Errorf("下行字节 = %d，期望 %d", down, len(msg))
	}
	if errs != 0 {
		t.Errorf("不该有错误计数，得到 %d", errs)
	}
}

func TestMetricsCountsDialFailure(t *testing.T) {
	// 拿一个已关闭的端口当目标
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := ln.Addr().String()
	ln.Close()

	m := newFakeMetrics()
	proxyAddr, stop := startProxyWithMetrics(t, m)
	defer stop()

	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	doHandshake(t, c)
	sendConnect(t, c, deadAddr)

	const id = "lo@127.0.0.1"
	waitFor(t, "错误计数", func() bool {
		_, _, _, _, errs := m.get(id)
		return errs == 1
	})

	// 建连失败的连接不该被算成"开过"
	if opened, _, _, _, _ := m.get(id); opened != 0 {
		t.Errorf("失败的连接不该计入 ConnOpened，得到 %d", opened)
	}
}

func TestMetricsCountsUDP(t *testing.T) {
	echoAddr, closeEcho := startUDPEcho(t)
	defer closeEcho()

	m := newFakeMetrics()
	proxyAddr, stop := startProxyWithMetrics(t, m)
	defer stop()

	ctrl, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}

	doHandshake(t, ctrl)
	bnd, rep := doUDPAssociate(t, ctrl)
	if rep != repSuccess {
		t.Fatalf("UDP ASSOCIATE 失败 0x%02x", rep)
	}

	uc := newUDPClient(t)
	target, _ := net.ResolveUDPAddr("udp", echoAddr)
	const msg = "udp-payload"

	if _, err := uc.WriteToUDP(buildUDPRequest(target, []byte(msg)), bnd); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 4096)
	_ = uc.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := uc.ReadFromUDP(buf); err != nil {
		t.Fatalf("等待 UDP 回包: %v", err)
	}

	const id = "lo@127.0.0.1"
	waitFor(t, "UDP 上下行计数", func() bool {
		_, _, up, down, _ := m.get(id)
		return up == int64(len(msg)) && down == int64(len(msg))
	})

	// 关掉控制连接，会话应被回收并减计数
	ctrl.Close()
	waitFor(t, "UDP 会话关闭计数", func() bool {
		_, closed, _, _, _ := m.get(id)
		return closed == 1
	})

	if opened, _, _, _, _ := m.get(id); opened != 1 {
		t.Errorf("UDP 会话 ConnOpened = %d，期望 1", opened)
	}
}
