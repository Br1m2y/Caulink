package download_test

// 验证「多连接 + Range 并行下载」在真实代理链路上是否可行。
//
// 只依赖仓库现有代码，不做任何修改：
//   ippool → scheduler → dialer → socks5 / httpproxy
//
// 用真实网络（清华 TUNA 镜像）、真实源 IP（10.6.93.195），
// 4 条连接各取 5MiB，拼接后与单连接整段下载逐字节比对。

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/proxy/dialer"
	"github.com/Br1m2y/caulink/internal/proxy/httpproxy"
	"github.com/Br1m2y/caulink/internal/proxy/socks5"
	"github.com/Br1m2y/caulink/internal/scheduler"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

const (
	targetURL = "https://mirrors.tuna.tsinghua.edu.cn/ubuntu-releases/22.04/ubuntu-22.04.5-desktop-amd64.iso"
	segSize   = 5 << 20
	srcIP     = "10.6.93.195"
	iface     = "WLAN"
)

// recordingPicker 记录每次 Pick 分到了哪个条目。
type recordingPicker struct {
	inner *scheduler.Scheduler
	mu    sync.Mutex
	picks []string
}

func (r *recordingPicker) Pick(network, addr string) (contract.IPPoolEntry, error) {
	e, err := r.inner.Pick(network, addr)
	if err == nil {
		r.mu.Lock()
		r.picks = append(r.picks, e.ID)
		r.mu.Unlock()
	}
	return e, err
}

func (r *recordingPicker) distribution() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := make(map[string]int)
	for _, id := range r.picks {
		m[id]++
	}
	return m
}

func setup(t *testing.T, entryLabels []string) (*recordingPicker, string, string, func()) {
	t.Helper()

	pool := ippool.New()
	for i, label := range entryLabels {
		e := contract.IPPoolEntry{
			ID:         label,
			Interface:  iface,
			SourceIP:   srcIP,
			AuthStatus: contract.AuthOnline,
		}
		if err := pool.Upsert(e); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		// 健康分拉满，保证可调度；RTT 给一个微小值避免除零。
		if err := pool.UpdateHealth(e.ID, 1.0, float64(5+i), 0); err != nil {
			t.Fatalf("update health: %v", err)
		}
	}

	sched, err := scheduler.New(pool, scheduler.DefaultRules())
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}
	rp := &recordingPicker{inner: sched}
	d := dialer.Dialer{Timeout: 10 * time.Second}

	lnS, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks: %v", err)
	}
	lnH, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen http: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s5 := &socks5.Server{ListenAddr: lnS.Addr().String(), Picker: rp, Dialer: d}
	hp := &httpproxy.Server{ListenAddr: lnH.Addr().String(), Picker: rp, Dialer: d}
	go func() { _ = s5.Serve(ctx, lnS) }()
	go func() { _ = hp.Serve(ctx, lnH) }()

	cleanup := func() {
		cancel()
		_ = lnS.Close()
		_ = lnH.Close()
	}
	return rp, lnS.Addr().String(), lnH.Addr().String(), cleanup
}

// socks5Dialer 是最小可用的 SOCKS5 客户端（CONNECT，无认证，域名直传）。
func socks5Dialer(proxyAddr string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: 10 * time.Second}
		c, err := d.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, err
		}
		if _, err := c.Write([]byte{5, 1, 0}); err != nil {
			c.Close()
			return nil, err
		}
		var resp [2]byte
		if _, err := io.ReadFull(c, resp[:]); err != nil {
			c.Close()
			return nil, err
		}
		if resp[0] != 5 || resp[1] != 0 {
			c.Close()
			return nil, fmt.Errorf("socks5 握手失败: %v", resp)
		}
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			c.Close()
			return nil, err
		}
		var port int
		fmt.Sscanf(portStr, "%d", &port)
		buf := []byte{5, 1, 0}
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			buf = append(buf, 1)
			buf = append(buf, ip.To4()...)
		} else {
			buf = append(buf, 3, byte(len(host)))
			buf = append(buf, host...)
		}
		buf = binary.BigEndian.AppendUint16(buf, uint16(port))
		if _, err := c.Write(buf); err != nil {
			c.Close()
			return nil, err
		}
		var head [4]byte
		if _, err := io.ReadFull(c, head[:]); err != nil {
			c.Close()
			return nil, err
		}
		if head[1] != 0 {
			c.Close()
			return nil, fmt.Errorf("socks5 CONNECT 被拒: rep=%d", head[1])
		}
		switch head[3] {
		case 1:
			var b [4]byte
			_, err = io.ReadFull(c, b[:])
		case 3:
			var l [1]byte
			_, err = io.ReadFull(c, l[:])
			if err == nil {
				_, err = io.CopyN(io.Discard, c, int64(l[0]))
			}
		case 4:
			var b [16]byte
			_, err = io.ReadFull(c, b[:])
		}
		if err != nil {
			c.Close()
			return nil, err
		}
		var pb [2]byte
		if _, err := io.ReadFull(c, pb[:]); err != nil {
			c.Close()
			return nil, err
		}
		return c, nil
	}
}

func clientVia(proxyURL string, dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	tr := &http.Transport{
		Proxy:                 func(*http.Request) (*url.URL, error) { return url.Parse(proxyURL) },
		DialContext:           dial,
		ForceAttemptHTTP2:     false,
		DisableCompression:    true,
		MaxIdleConnsPerHost:   16,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: 3 * time.Minute}
}

func fetchRange(cl *http.Client, start, end int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	req.Header.Set("User-Agent", "caulink-phase-test/1.0")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func directGet(start, end int64) ([]byte, error) {
	cl := &http.Client{Transport: &http.Transport{
		Proxy:              nil,
		ForceAttemptHTTP2:  false,
		DisableCompression: true,
	}, Timeout: 3 * time.Minute}
	return fetchRange(cl, start, end)
}

// parallelFetch 起 n 条连接各取一段，返回拼接结果与墙钟时间。
func parallelFetch(t *testing.T, cl *http.Client, n int) ([]byte, time.Duration) {
	t.Helper()
	parts := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := int64(i * segSize)
			e := s + segSize - 1
			parts[i], errs[i] = fetchRange(cl, s, e)
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	var all []byte
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("分片 %d 失败: %v", i, errs[i])
		}
		if len(parts[i]) != segSize {
			t.Fatalf("分片 %d 长度 %d，期望 %d", i, len(parts[i]), segSize)
		}
		all = append(all, parts[i]...)
	}
	return all, elapsed
}

func TestParallelRange(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过真实网络测试")
	}

	// 参照：单连接取整段，作为内容基准，同时给出单连接速度。
	t.Log("== 参照：单连接 20MiB ==")
	refStart := time.Now()
	ref, err := directGet(0, int64(4*segSize-1))
	if err != nil {
		t.Fatalf("参照下载失败: %v", err)
	}
	refDur := time.Since(refStart)
	refSum := md5.Sum(ref)
	t.Logf("   %d 字节, %.2f MB/s, md5=%x", len(ref), float64(len(ref))/refDur.Seconds()/1e6, refSum)

	singleThroughProxy := 0.0
	_ = singleThroughProxy

	type result struct {
		name string
		sum  [16]byte
		dur  time.Duration
		dist map[string]int
	}
	var results []result

	// HTTP 代理（Steam 走的就是这条）
	{
		_, _, httpAddr, cleanup := setup(t, []string{iface + "@" + srcIP})
		defer cleanup()
		cl := clientVia("http://"+httpAddr, nil)

		// 对照组：同一条代理链路上的单连接。没有这一步，"4 连接 vs 直连"
		// 就不是受控比较——代理本身也可能有开销。
		t.Log("== 1 连接：HTTP 代理（对照组） ==")
		t0 := time.Now()
		one, err := fetchRange(cl, 0, int64(4*segSize-1))
		if err != nil {
			t.Fatalf("单连接（代理）失败: %v", err)
		}
		oneDur := time.Since(t0)
		t.Logf("   %d 字节, 墙钟 %.2fs, %.2f MB/s, md5=%x",
			len(one), oneDur.Seconds(), float64(len(one))/oneDur.Seconds()/1e6, md5.Sum(one))
		if md5.Sum(one) != refSum {
			t.Fatalf("经代理的单连接内容与直连不一致")
		}
		singleThroughProxy = float64(len(one)) / oneDur.Seconds() / 1e6

		t.Log("== 4 连接并行：HTTP 代理 ==")
		body, dur := parallelFetch(t, cl, 4)
		sum := md5.Sum(body)
		t.Logf("   %d 字节, 墙钟 %.2fs, 聚合 %.2f MB/s, md5=%x  （对单连接 %.2fx）",
			len(body), dur.Seconds(), float64(len(body))/dur.Seconds()/1e6, sum,
			float64(len(body))/dur.Seconds()/1e6/singleThroughProxy)
		if sum != refSum {
			t.Fatalf("HTTP 代理：拼接内容与单连接不一致")
		}
		results = append(results, result{"http-proxy", sum, dur, nil})
	}

	// SOCKS5
	{
		rp, socksAddr, _, cleanup := setup(t, []string{iface + "@" + srcIP})
		defer cleanup()
		cl := clientVia("socks5h://"+socksAddr, nil)
		t.Log("== 4 连接并行：SOCKS5 ==")
		body, dur := parallelFetch(t, cl, 4)
		sum := md5.Sum(body)
		t.Logf("   %d 字节, 墙钟 %.2fs, 聚合 %.2f MB/s, md5=%x",
			len(body), dur.Seconds(), float64(len(body))/dur.Seconds()/1e6, sum)
		if sum != refSum {
			t.Fatalf("SOCKS5：拼接内容与单连接不一致")
		}
		t.Logf("   调度分配: %v", rp.distribution())
		results = append(results, result{"socks5", sum, dur, rp.distribution()})
	}

	for _, r := range results {
		t.Logf("RESULT %-12s md5_ok=%v 聚合=%.2f MB/s 单连接=%.2f MB/s",
			r.name, r.sum == refSum, float64(4*segSize)/r.dur.Seconds()/1e6,
			float64(len(ref))/refDur.Seconds()/1e6)
	}
}

// TestSpreadAcrossEntries 用两个条目（同一物理 IP，只验证分配行为）
// 确认并发 Pick 会落到不同条目——并行下载能铺开的前提。
func TestSpreadAcrossEntries(t *testing.T) {
	rp, _, _, cleanup := setup(t, []string{"A@" + srcIP, "B@" + srcIP})
	defer cleanup()

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = rp.Pick("tcp", "example.com:443") }()
	}
	wg.Wait()

	dist := rp.distribution()
	if len(dist) != 2 {
		t.Fatalf("两条并发连接应落到两个不同条目，实际 %v", dist)
	}
	t.Logf("分配结果: %v", dist)
}

// TestPlainHTTPRePicks 验证明文 HTTP 路径的一个关键行为：
// handleForward 强制 Connection: close，每条请求都是一条新连接、一次新 Pick。
// 也就是说——同一个逻辑下载的连续请求会被分到不同源 IP。
// 这正是「下载中途换了 IP 就断」说法的合理来源（对 IP 绑定的站点）。
func TestPlainHTTPRePicks(t *testing.T) {
	rp, _, httpAddr, cleanup := setup(t, []string{"A@" + srcIP, "B@" + srcIP})
	defer cleanup()

	cl := clientVia("http://"+httpAddr, nil)
	for i := 0; i < 4; i++ {
		resp, err := cl.Get("http://www.msftconnecttest.com/connecttest.txt")
		if err != nil {
			t.Fatalf("第 %d 次请求失败: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Logf("第 %d 次: HTTP %d, %d 字节", i+1, resp.StatusCode, len(body))
	}
	dist := rp.distribution()
	t.Logf("4 次独立请求的分配: %v", dist)
	if len(dist) < 2 {
		t.Fatalf("连续请求应交替落到不同条目，实际 %v", dist)
	}
}
