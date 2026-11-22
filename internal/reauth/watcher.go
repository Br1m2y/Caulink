// Package reauth 把"代理层看到的上游失败"转成"立刻重认证"。
//
// 为什么需要它：轮询模式下，掉线最坏要等一个检查周期才被发现。而下载本身
// 正在不停地建连，代理层是第一个知道"从某个源 IP 连不出去了"的地方——
// 用这个信号触发重认证，恢复时间可以压到接近 0。
//
// 但信号有两个问题，必须在这里处理掉：
//
//  1. **噪声大**：一次掉线会伴随成串的建连失败（几百个并发连接同时失败），
//     不去抖就会并发发起几十次登录，把门户打爆、账号还可能被临时限制；
//  2. **会误判**：建连失败也可能只是目标站点挂了/端口不可达，与认证无关。
//     因此真正触发前由 Handler 再做一次在线探测确认（见 internal/auth 的
//     forceReauth），本包只负责"攒够证据再举手"。
package reauth

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Handler 在确认需要重认证时被调用，参数是源 IP。
//
// 实现通常直接调 auth.Manager.ReauthNow：它内部还会做一次在线探测，
// 所以本包不需要（也不应该）自己判断"是不是真的掉线"。
type Handler func(ctx context.Context, sourceIP string) error

// Options 是 Watcher 的构造参数。
type Options struct {
	// Threshold 是窗口内需要累计多少次建连失败才触发，默认 3。
	Threshold int
	// Window 是计数窗口，默认 5s。
	Window time.Duration
	// Cooldown 是两次触发之间的最小间隔，默认 10s。
	Cooldown time.Duration
	// Handler 必填。
	Handler Handler
	// Logger 可选。
	Logger *slog.Logger
}

// Watcher 收集建连失败并决定何时触发重认证。
//
// 并发安全：OnDialFailure 可由任意 goroutine 调用（它在拨号路径上，
// 只做一次非阻塞发送），所有状态改动都发生在 Run 所在的 goroutine 里。
type Watcher struct {
	opts Options
	ch   chan string

	mu       sync.Mutex
	hits     map[string][]time.Time
	lastFire map[string]time.Time
}

// New 创建 Watcher。调用方需要另起 goroutine 跑 Run。
func New(opts Options) *Watcher {
	if opts.Threshold <= 0 {
		opts.Threshold = 3
	}
	if opts.Window <= 0 {
		opts.Window = 5 * time.Second
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = 10 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Watcher{
		opts:     opts,
		ch:       make(chan string, 256),
		hits:     map[string][]time.Time{},
		lastFire: map[string]time.Time{},
	}
}

// OnDialFailure 上报一次从 sourceIP 发起的建连失败。
//
// 非阻塞：拨号路径不能被阻塞，channel 满了就丢弃——丢掉的失败由后续失败或
// 轮询兜底补上，宁可少触发一次也不要卡住用户连接。
func (w *Watcher) OnDialFailure(sourceIP string) {
	if sourceIP == "" {
		return
	}
	select {
	case w.ch <- sourceIP:
	default:
	}
}

// Run 消费失败事件，直到 ctx 取消。
func (w *Watcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ip := <-w.ch:
			if w.shouldFire(ip) {
				w.fire(ctx, ip)
			}
		}
	}
}

// shouldFire 判断这次失败是否让该 IP 达到了触发条件。
func (w *Watcher) shouldFire(sourceIP string) bool {
	now := time.Now()

	w.mu.Lock()
	defer w.mu.Unlock()

	// 冷却期内直接忽略，并且不累计：避免冷却结束后瞬间又被旧计数触发。
	if last, ok := w.lastFire[sourceIP]; ok && now.Sub(last) < w.opts.Cooldown {
		return false
	}

	kept := w.hits[sourceIP][:0]
	for _, t := range w.hits[sourceIP] {
		if now.Sub(t) <= w.opts.Window {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	w.hits[sourceIP] = kept

	if len(kept) < w.opts.Threshold {
		return false
	}

	w.lastFire[sourceIP] = now
	w.hits[sourceIP] = nil
	return true
}

// fire 在独立 goroutine 里调用 Handler：它内部有网络请求，不能挡住事件循环。
func (w *Watcher) fire(ctx context.Context, sourceIP string) {
	w.opts.Logger.Warn("代理层观察到上游失败，触发即时重认证",
		"src_ip", sourceIP, "threshold", w.opts.Threshold, "window", w.opts.Window)

	go func() {
		// 用独立的 ctx：调用方退出时也要把这次重认证做完，否则可能出现
		// "已经判定离线但没有登录"的空窗。
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := w.opts.Handler(runCtx, sourceIP); err != nil {
			w.opts.Logger.Error("即时重认证失败", "src_ip", sourceIP, "err", err)
		}
	}()
}
