// Package health 测量每条源 IP 链路的 RTT 与丢包，折算成调度权重写回 IP 池。
//
// 与认证层的分工（容易写重的地方）：
//   - internal/auth 判定"认证会话是否还活着"，几百秒级；
//   - 本包判定"这条链路质量如何"，秒级。
//
// 两者探测同一个 IP，但目的、周期、失败处置完全不同。合并会导致一次网络抖动
// 被误判成掉线，进而触发重认证。
package health

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/netsrc"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// ErrNilPool 在构造时传入空池的情况下返回。
var ErrNilPool = errors.New("health: pool 不能为空")

// Score 把 RTT 与丢包率折算成 [0,1] 的调度权重。
//
//	rttFactor = 1 / (1 + rttMs/200)
//	score     = (1 - lossRate) * rttFactor
//
// 含义：RTT 每增加 200ms，权重减半；丢包率直接按比例打折；两者相乘。
// 选这个形状是因为它单调、连续、没有阈值跳变，而且一句话就能解释清楚 ——
// 答辩时不需要画图也能说明白。分段打分的方案在阈值边界会有跳变，
// 一条链路可能因为 1ms 的抖动从 0.8 掉到 0.3。
func Score(rttMs, lossRate float64) float64 {
	if rttMs < 0 {
		rttMs = 0
	}
	if lossRate < 0 {
		lossRate = 0
	}
	if lossRate > 1 {
		lossRate = 1
	}

	rttFactor := 1.0 / (1.0 + rttMs/200.0)
	s := (1 - lossRate) * rttFactor

	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}

// Options 是探测参数。
type Options struct {
	// Interval 是探测周期，默认 10s。比认证层的 guardian 快一个量级，
	// 这样调度层能在几秒内感知到链路劣化。
	Interval time.Duration

	// Timeout 是单次 TCP 握手超时，默认 3s。
	Timeout time.Duration

	// Target 是探测目标，形如 host:port，默认 connect.rom.miui.com:80。
	//
	// 用 TCP 握手而不是 HTTP 请求：更轻量、不依赖应用层协议、RTT 含义明确。
	// 代价是它反映的是到目标的网络 RTT，不等于端到端应用延迟 —— 对调度足够。
	Target string

	// Window 是滑动窗口长度，默认 10 次。
	// 太短会被单次抖动带跑，太长则对劣化反应迟钝。
	Window int

	Logger *slog.Logger
}

func (o *Options) applyDefaults() {
	if o.Interval <= 0 {
		o.Interval = 10 * time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Second
	}
	if o.Target == "" {
		o.Target = "connect.rom.miui.com:80"
	}
	if o.Window <= 0 {
		o.Window = 10
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// Prober 周期性地探测池内所有在线 IP 的链路质量。
type Prober struct {
	pool *ippool.Pool
	opts Options

	mu      sync.Mutex
	windows map[string]*window
}

// New 创建探测器。
func New(pool *ippool.Pool, opts Options) (*Prober, error) {
	if pool == nil {
		return nil, ErrNilPool
	}
	opts.applyDefaults()
	return &Prober{
		pool:    pool,
		opts:    opts,
		windows: make(map[string]*window),
	}, nil
}

// Run 阻塞运行探测循环，直到 ctx 取消。
func (p *Prober) Run(ctx context.Context) {
	// 先探一次：等一个完整周期再出数据，UI 上会有一段"全是 0"的空窗。
	p.ProbeOnce(ctx)

	tk := time.NewTicker(p.opts.Interval)
	defer tk.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			p.ProbeOnce(ctx)
		}
	}
}

// ProbeOnce 对所有在线 IP 并发探测一轮，并把结果写回池。
//
// 并发而不是串行：串行时 4 个 IP 各 3 秒超时就是 12 秒，
// 探测周期会被自己的耗时撑爆。
func (p *Prober) ProbeOnce(ctx context.Context) {
	entries := p.pool.Snapshot()

	var wg sync.WaitGroup
	for _, e := range entries {
		if e.AuthStatus != contract.AuthOnline {
			continue
		}
		wg.Add(1)
		go func(e contract.IPPoolEntry) {
			defer wg.Done()
			p.probeOne(ctx, e)
		}(e)
	}
	wg.Wait()
}

func (p *Prober) probeOne(ctx context.Context, e contract.IPPoolEntry) {
	// 探测同样要遵守"源与目标同族"：用 v4 源去连解析出 v6 的目标会被内核拒绝，
	// 健康分会一路掉到 0，把一条完全正常的链路误判成坏。
	target, err := netsrc.Resolve(ctx, p.opts.Target, e)
	if err != nil {
		p.opts.Logger.Warn("探测目标不可用（源地址族不匹配）", "id", e.ID, "target", p.opts.Target, "err", err)
		return
	}

	rtt, ok := dialRTT(ctx, target.Network, target.Addr, target.Source, p.opts.Timeout)

	w := p.windowFor(e.ID)
	w.push(rtt, ok)
	avg, loss := w.stats()

	score := Score(avg, loss)
	if err := p.pool.UpdateHealth(e.ID, score, avg, loss); err != nil {
		p.opts.Logger.Warn("回写健康分失败", "id", e.ID, "err", err)
	}
}

func (p *Prober) windowFor(id string) *window {
	p.mu.Lock()
	defer p.mu.Unlock()

	w, ok := p.windows[id]
	if !ok {
		w = newWindow(p.opts.Window)
		p.windows[id] = w
	}
	return w
}

// Forget 清掉某个 IP 的统计窗口。IP 被移出池时调用，避免长跑时 map 无限增长。
func (p *Prober) Forget(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.windows, id)
}

// dialRTT 用指定源地址与目标完成一次 TCP 握手，返回耗时（毫秒）。
//
// 绑定 LocalAddr 是必须的：不绑的话所有 IP 的探测都从默认路由出去，
// 测出来的是同一条链路的质量，健康分完全失真。
// network 显式区分 tcp4/tcp6，保证源地址族与目标一致。
func dialRTT(ctx context.Context, network, target string, srcIP net.IP, timeout time.Duration) (float64, bool) {
	d := &net.Dialer{
		Timeout:   timeout,
		LocalAddr: &net.TCPAddr{IP: srcIP},
		// 同 dialer：只绑源 IP 在 Windows 双网卡同网段时会把包发到另一块网卡，
		// 探测全部失败 → 健康分被打成 0 → 调度层直接弃用这条看起来正常的线路。
		Control: netsrc.DialControl(srcIP),
	}

	start := time.Now()
	conn, err := d.DialContext(ctx, network, target)
	elapsed := time.Since(start)

	if err != nil {
		return 0, false
	}
	_ = conn.Close()

	return float64(elapsed.Microseconds()) / 1000.0, true
}

// window 是固定长度的滑动窗口，记录最近 N 次探测。
type window struct {
	size int
	rtt  []float64
	ok   []bool
	i    int
	n    int
}

func newWindow(size int) *window {
	return &window{
		size: size,
		rtt:  make([]float64, size),
		ok:   make([]bool, size),
	}
}

func (w *window) push(rtt float64, success bool) {
	w.rtt[w.i] = rtt
	w.ok[w.i] = success
	w.i = (w.i + 1) % w.size
	if w.n < w.size {
		w.n++
	}
}

// stats 返回成功样本的平均 RTT（毫秒）与丢包率。
func (w *window) stats() (avgRTT, loss float64) {
	if w.n == 0 {
		return 0, 0
	}

	var sum float64
	var okCount, lost int
	for i := 0; i < w.n; i++ {
		if !w.ok[i] {
			lost++
			continue
		}
		sum += w.rtt[i]
		okCount++
	}

	loss = float64(lost) / float64(w.n)
	if okCount > 0 {
		avgRTT = sum / float64(okCount)
	}
	return avgRTT, loss
}
