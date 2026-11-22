package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Br1m2y/caulink/internal/netsrc"
)

// Sample 是一次瞬时吞吐采样。
type Sample struct {
	AtMS int64   `json:"at_ms"`
	MBps float64 `json:"mbps"`
}

// TargetDetail 是单个源 IP 在本次测量中的明细。
type TargetDetail struct {
	Iface    string   `json:"interface"`
	IP       string   `json:"source_ip"`
	Conns    int      `json:"connections"`
	AvgMBps  float64  `json:"avg_mbps"`
	Bytes    uint64   `json:"bytes"`
	Requests int64    `json:"requests"`
	Errors   []string `json:"errors,omitempty"`
}

// Measurement 是一次完整测量的结果。
type Measurement struct {
	Label      string         `json:"label"`
	ConnsPerIP int            `json:"connections_per_ip"`
	WarmupMS   int64          `json:"warmup_ms"`
	DurationMS int64          `json:"duration_ms"`
	TotalBytes uint64         `json:"total_bytes"`
	AvgMBps    float64        `json:"avg_mbps"`
	PeakMBps   float64        `json:"peak_mbps"`
	Samples    []Sample       `json:"samples"`
	PerTarget  []TargetDetail `json:"per_target"`
	Errors     []string       `json:"errors,omitempty"`
}

// newTransport 造一个绑定到指定源 IP 的连接池。
//
// 三个设置是测量正确性的前提，不是可选优化：
//   - ForceAttemptHTTP2=false：HTTP/2 会在一条 TCP 上多路复用，8 个并发请求
//     实际只有 1 条连接，测出来的就不是"多连接并发"。
//   - DisableCompression=true：否则会协商 gzip，既改变了传输字节数，
//     又把 CPU 消耗混进测量结果。
//   - MaxConnsPerHost=conns：把该 IP 上的 TCP 连接数钉死在 worker 数量上。
func newTransport(srcIP net.IP, conns int) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := &net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
				LocalAddr: &net.TCPAddr{IP: srcIP},
				// 测速工具本身也必须钉住出口网卡，否则双网卡同网段时会测出
				// "第二个 IP 0 MB/s"，把链路问题误记成软件问题。
				Control: netsrc.DialControl(srcIP),
			}
			return d.DialContext(ctx, network, addr)
		},
		MaxConnsPerHost:     conns,
		MaxIdleConnsPerHost: conns,
		MaxIdleConns:        conns,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
		ForceAttemptHTTP2:   false,
	}
}

type errCollector struct {
	mu   sync.Mutex
	msgs []string
	seen map[string]bool
}

func (e *errCollector) add(msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.seen == nil {
		e.seen = map[string]bool{}
	}
	if e.seen[msg] {
		return
	}
	e.seen[msg] = true
	if len(e.msgs) < 5 {
		e.msgs = append(e.msgs, msg)
	}
}

func (e *errCollector) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.msgs))
	copy(out, e.msgs)
	return out
}

// runMeasure 是核心测量函数。单 IP 和 多 IP 并发走同一套逻辑，
// 区别只在 sources 的长度 —— 这样两种场景的测量口径完全一致，结果才可比。
func runMeasure(
	parent context.Context,
	label string,
	sources []Source,
	conns int,
	warmup, dur time.Duration,
	url string,
) Measurement {
	ctx, cancel := context.WithTimeout(parent, warmup+dur)
	defer cancel()

	n := len(sources)
	total := new(atomic.Uint64)
	perSrc := make([]*atomic.Uint64, n)
	perReq := make([]*atomic.Int64, n)
	errs := make([]*errCollector, n)
	transports := make([]*http.Transport, n)

	for i := range sources {
		perSrc[i] = new(atomic.Uint64)
		perReq[i] = new(atomic.Int64)
		errs[i] = &errCollector{}
	}

	start := time.Now()

	// 采样器：每 100ms 记一次累计字节。所有读写都在这个 goroutine 内完成，
	// 主流程在它关闭后才读取，因此不需要额外加锁。
	type point struct {
		at    time.Duration
		bytes uint64
	}
	var points []point
	var warmSnap []uint64
	warmCaptured := false

	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()

		points = append(points, point{0, 0})
		for {
			select {
			case <-ctx.Done():
				points = append(points, point{time.Since(start), total.Load()})
				return
			case now := <-tk.C:
				b := total.Load()
				points = append(points, point{now.Sub(start), b})
				if !warmCaptured && now.Sub(start) >= warmup {
					warmSnap = make([]uint64, n)
					for i := range perSrc {
						warmSnap[i] = perSrc[i].Load()
					}
					warmCaptured = true
				}
			}
		}
	}()

	var wg sync.WaitGroup
	for i, s := range sources {
		tr := newTransport(s.IP, conns)
		transports[i] = tr
		client := &http.Client{Transport: tr}
		for c := 0; c < conns; c++ {
			wg.Add(1)
			go func(i int, client *http.Client) {
				defer wg.Done()
				worker(ctx, client, url, total, perSrc[i], perReq[i], errs[i])
			}(i, client)
		}
	}

	wg.Wait()
	cancel()
	<-samplerDone

	for _, tr := range transports {
		tr.CloseIdleConnections()
	}

	m := Measurement{
		Label:      label,
		ConnsPerIP: conns,
		WarmupMS:   warmup.Milliseconds(),
		DurationMS: dur.Milliseconds(),
		TotalBytes: total.Load(),
	}

	// 稳态窗口：丢弃预热期，只用之后的数据算平均速率。
	// 不丢预热的话，TCP 慢启动会让结果系统性偏低 20% 到 40%。
	var steadyFrom uint64
	steadyStart := warmup
	if warmCaptured {
		var sum uint64
		for _, v := range warmSnap {
			sum += v
		}
		steadyFrom = sum
	}
	if len(points) > 0 && points[len(points)-1].at > warmup {
		steadyEnd := points[len(points)-1].at
		steadyDur := (steadyEnd - steadyStart).Seconds()
		if steadyDur > 0 {
			m.AvgMBps = float64(m.TotalBytes-steadyFrom) / steadyDur / 1e6
		}
	}

	// 逐点瞬时速率，同时算峰值。
	for i := 1; i < len(points); i++ {
		dt := (points[i].at - points[i-1].at).Seconds()
		if dt <= 0 {
			continue
		}
		rate := float64(points[i].bytes-points[i-1].bytes) / dt / 1e6
		m.Samples = append(m.Samples, Sample{AtMS: points[i].at.Milliseconds(), MBps: rate})
		if points[i].at >= warmup && rate > m.PeakMBps {
			m.PeakMBps = rate
		}
	}

	for i, s := range sources {
		d := TargetDetail{
			Iface:    s.Iface,
			IP:       s.IP.String(),
			Conns:    conns,
			Bytes:    perSrc[i].Load(),
			Requests: perReq[i].Load(),
			Errors:   errs[i].list(),
		}
		if warmCaptured && len(points) > 0 {
			steadyDur := (points[len(points)-1].at - warmup).Seconds()
			if steadyDur > 0 {
				d.AvgMBps = float64(d.Bytes-warmSnap[i]) / steadyDur / 1e6
			}
		}
		m.PerTarget = append(m.PerTarget, d)
	}

	return m
}

// worker 反复从同一个源 IP 拉取目标 URL，直到上下文取消。
//
// 循环请求而不是只请求一次：测速目标若是有限大小的文件，单次请求会提前结束，
// 导致后半段无流量。循环让小文件也能测，代价是 redis 增加了请求开销 ——
// 所以配置里要求目标是"大文件或无限流端点"，循环只是兜底。
func worker(
	ctx context.Context,
	client *http.Client,
	url string,
	total, mine *atomic.Uint64,
	reqs *atomic.Int64,
	errs *errCollector,
) {
	buf := make([]byte, 64*1024)

	backoff := func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
			return true
		}
	}

	for {
		if ctx.Err() != nil {
			return
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			errs.add("构造请求失败: " + err.Error())
			return
		}
		req.Header.Set("User-Agent", userAgent)

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			errs.add(err.Error())
			if !backoff() {
				return
			}
			continue
		}

		reqs.Add(1)

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errs.add(fmt.Sprintf("目标返回 HTTP %d", resp.StatusCode))
			resp.Body.Close()
			if !backoff() {
				return
			}
			continue
		}

		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				total.Add(uint64(n))
				mine.Add(uint64(n))
			}
			if rerr != nil {
				break
			}
			if ctx.Err() != nil {
				break
			}
		}
		resp.Body.Close()
	}
}
