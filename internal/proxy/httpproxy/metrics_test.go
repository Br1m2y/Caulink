package httpproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// countingMetrics 记录每个源 IP 的收发字节，用于断言数据面统计。
type countingMetrics struct {
	mu     sync.Mutex
	up     map[string]int64
	down   map[string]int64
	opened map[string]int
}

func newCountingMetrics() *countingMetrics {
	return &countingMetrics{up: map[string]int64{}, down: map[string]int64{}, opened: map[string]int{}}
}

func (m *countingMetrics) ConnOpened(id string) { m.mu.Lock(); m.opened[id]++; m.mu.Unlock() }
func (m *countingMetrics) ConnClosed(id string) {}
func (m *countingMetrics) AddUp(id string, n int64) {
	m.mu.Lock()
	m.up[id] += n
	m.mu.Unlock()
}
func (m *countingMetrics) AddDown(id string, n int64) {
	m.mu.Lock()
	m.down[id] += n
	m.mu.Unlock()
}
func (m *countingMetrics) AddError(id string) {}

func (m *countingMetrics) snapshot() (up, down int64, opened int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.up {
		up += v
	}
	for _, v := range m.down {
		down += v
	}
	for _, v := range m.opened {
		opened += v
	}
	return
}

// 明文 HTTP 转发的字节必须计入统计。
//
// 这是回归测试：Steam 的 CDN 下载走 80 端口的明文 HTTP，代理层当时只统计了
// CONNECT 隧道，导致流量明明在跑（实测 94 Mbps），面板却一直显示 0 速度，
// 用户会误以为调度没生效。
func TestForwardCountsBytes(t *testing.T) {
	const body = "0123456789abcdef" // 16 字节响应体

	target := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = io.WriteString(w, body)
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = target.Serve(ln) }()
	defer target.Close()

	met := newCountingMetrics()
	dialer := fakeDialer{dial: func(ctx context.Context, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}}

	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Picker: fakePicker{}, Dialer: dialer, Metrics: met}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Serve(ctx, proxyLn) }()

	conn, err := net.DialTimeout("tcp", proxyLn.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// 绝对 URI 形式：这是浏览器/Steam 走明文 HTTP 代理时的请求形态。
	fmt.Fprintf(conn, "GET http://example.test/x HTTP/1.1\r\nHost: example.test\r\n\r\n")

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("读响应失败: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(got) != body {
		t.Fatalf("响应体不对: %q", got)
	}

	// 等计数回调落定（resp.Write 返回后计数已完成，这里留一点余量）。
	deadline := time.Now().Add(time.Second)
	var down int64
	for time.Now().Before(deadline) {
		_, down, _ = met.snapshot()
		if down > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	up, down, opened := met.snapshot()
	if opened == 0 {
		t.Error("ConnOpened 没有被调用")
	}
	if down < int64(len(body)) {
		t.Errorf("下行字节统计不足：down=%d，至少应包含 %d 字节响应体", down, len(body))
	}
	if up == 0 {
		t.Error("上行请求（请求头）没有被统计")
	}
}
