// Package metrics 采集每条源 IP 的流量与连接统计，供状态接口与 UI 读取。
//
// 为什么单独一个包，而不是塞进 ippool：
//
//	ippool 回答的是「这条链路**怎么样**」——认证状态、健康分、RTT，由探测驱动。
//	metrics 回答的是「这条链路**干了多少活**」——字节、连接数，由数据面驱动。
//
// 两者的更新时机、更新者、失效条件都不一样。混进 ippool 会让那个刻意收窄的
// 写入接口（UpdateAuth / UpdateHealth）变成大杂烩，A/B/C 的字段归属立刻失守。
//
// 本包不持有任何链路状态，只做计数。空 Registry 是合法的，所有方法都能用。
package metrics

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// 速率平滑系数。
//
// 原始差分（每秒采样）在 UI 上会剧烈跳动：一个请求结束就能让曲线从 5MB/s 掉到 0。
// EWMA 让曲线读起来像"最近几秒的平均水平"，α=0.3 对应的反应时间约 3 个采样周期。
// 不在本包里做更长窗口的平均，是因为「瞬时速率」和「平均速率」是两个不同的指标，
// 需要后者时由调用方自己累计字节算。
const rateSmoothing = 0.3

// Counter 是单条源 IP 的计数器。所有字段用 atomic，读侧不需要加锁。
type Counter struct {
	ActiveConns atomic.Int64
	TotalConns  atomic.Uint64
	BytesUp     atomic.Uint64
	BytesDown   atomic.Uint64
	// Errors 统计建连失败与转发异常，用于 UI 区分"链路慢"和"链路在报错"。
	Errors atomic.Uint64
}

// Rate 是平滑后的瞬时速率，单位字节/秒。
type Rate struct {
	UpBps   float64 `json:"up_bps"`
	DownBps float64 `json:"down_bps"`
}

// Sample 是某条源 IP 在其一个采样时刻的完整读数。
type Sample struct {
	ID          string
	ActiveConns int64
	TotalConns  uint64
	BytesUp     uint64
	BytesDown   uint64
	Errors      uint64
	Rate        Rate
}

// Registry 是按源 IP 索引的计数器集合，并发安全。
type Registry struct {
	mu       sync.RWMutex
	counters map[string]*Counter
	rates    map[string]Rate
	prev     map[string]rawSample
	prevAt   time.Time
}

type rawSample struct {
	up   uint64
	down uint64
}

// New 创建空注册表。
func New() *Registry {
	return &Registry{
		counters: make(map[string]*Counter),
		rates:    make(map[string]Rate),
		prev:     make(map[string]rawSample),
	}
}

// get 取或创建计数器。用双检避免每次计数都抢写锁 ——
// 数据面上每次连接建立都会走到这里。
func (r *Registry) get(id string) *Counter {
	r.mu.RLock()
	c := r.counters[id]
	r.mu.RUnlock()
	if c != nil {
		return c
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if c = r.counters[id]; c == nil {
		c = &Counter{}
		r.counters[id] = c
	}
	return c
}

// ConnOpened 记录一条连接（或一个 UDP 会话）开始使用该源 IP。
func (r *Registry) ConnOpened(id string) {
	c := r.get(id)
	c.ActiveConns.Add(1)
	c.TotalConns.Add(1)
}

// ConnClosed 记录一条连接结束。
//
// 必须有调用方保证与 ConnOpened 成对出现（用 defer）。漏掉一次的后果是
// UI 上的活跃连接数只增不减，而且看不出是哪条漏了。
func (r *Registry) ConnClosed(id string) {
	c := r.get(id)
	if c.ActiveConns.Add(-1) < 0 {
		// 计数变成负数说明配对出错。归零比让 UI 显示 -1 好，但这是个 bug 信号。
		c.ActiveConns.Store(0)
	}
}

// AddUp 累加发往上游的字节数。
func (r *Registry) AddUp(id string, n int64) {
	if n <= 0 {
		return
	}
	r.get(id).BytesUp.Add(uint64(n))
}

// AddDown 累加从上游收到的字节数。
func (r *Registry) AddDown(id string, n int64) {
	if n <= 0 {
		return
	}
	r.get(id).BytesDown.Add(uint64(n))
}

// AddError 累加一次失败。
func (r *Registry) AddError(id string) {
	r.get(id).Errors.Add(1)
}

// Snapshot 返回按 ID 排序的一致读数。
func (r *Registry) Snapshot() []Sample {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Sample, 0, len(r.counters))
	for id, c := range r.counters {
		out = append(out, Sample{
			ID:          id,
			ActiveConns: c.ActiveConns.Load(),
			TotalConns:  c.TotalConns.Load(),
			BytesUp:     c.BytesUp.Load(),
			BytesDown:   c.BytesDown.Load(),
			Errors:      c.Errors.Load(),
			Rate:        r.rates[id],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// StartSampler 周期性把累计字节转成瞬时速率，直到 ctx 取消。
//
// 采样周期决定速率的分辨率，1 秒足够：再密只是增加 EWMA 的计算量，
// 人眼在 UI 上也看不出差别。
func (r *Registry) StartSampler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			r.sampleOnce(now)
		}
	}
}

// sampleOnce 计算一次速率。首次调用只建立基线，不产出速率 ——
// 没有上一帧就没法算差分，硬算会得到一个巨大且错误的值。
func (r *Registry) sampleOnce(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 用真实间隔而不是标称间隔：调度延迟、GC 停顿都会让实际 dt 偏离 1s，
	// 用标称值会在系统繁忙时系统性高估速率。
	dt := now.Sub(r.prevAt).Seconds()
	baseline := r.prevAt.IsZero() || dt <= 0

	if baseline {
		r.prevAt = now
		for id, c := range r.counters {
			r.prev[id] = rawSample{up: c.BytesUp.Load(), down: c.BytesDown.Load()}
		}
		return
	}

	for id, c := range r.counters {
		up, down := c.BytesUp.Load(), c.BytesDown.Load()

		p, ok := r.prev[id]
		// 计数器变小意味着条目被重建过。这一帧丢弃，下一帧重新对齐。
		if !ok || up < p.up || down < p.down {
			r.prev[id] = rawSample{up: up, down: down}
			r.rates[id] = Rate{}
			continue
		}

		upBps := float64(up-p.up) / dt
		downBps := float64(down-p.down) / dt

		old := r.rates[id]
		r.rates[id] = Rate{
			UpBps:   rateSmoothing*upBps + (1-rateSmoothing)*old.UpBps,
			DownBps: rateSmoothing*downBps + (1-rateSmoothing)*old.DownBps,
		}
		r.prev[id] = rawSample{up: up, down: down}
	}

	// 池里已删除的条目不再产生速率，清掉避免 map 只增不减。
	for id := range r.rates {
		if _, ok := r.counters[id]; !ok {
			delete(r.rates, id)
			delete(r.prev, id)
		}
	}

	r.prevAt = now
}
