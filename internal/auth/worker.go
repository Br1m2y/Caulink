package auth

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// worker 管一个源 IP 的完整生命周期：拉起子进程、看护存活、探测在线状态、必要时重启。
type worker struct {
	entry   contract.IPPoolEntry
	srcIP   net.IP
	cfgPath string
	dir     string
	opts    Options
	emit    func(Event)

	mu     sync.Mutex
	status contract.AuthStatus
	reason string
	proc   *process

	// netErrs 只被 probeLoop 这个 goroutine 访问，不需要锁。
	netErrs int

	// lastForce 是上一次事件驱动重认证的时间，用于冷却。
	lastForce time.Time

	// reauthing 保证同一实例同时只有一个事件驱动重认证在飞。
	reauthing atomic.Bool

	// lastActive 记录上一次探测模式，只在切换时打日志。
	// 初值为 true：首次探测若处于空闲，会输出一条"切到省流模式"，便于确认自适应生效。
	lastActive bool

	cancel context.CancelFunc
	done   chan struct{}
	wg     sync.WaitGroup
}

func newWorker(entry contract.IPPoolEntry, srcIP net.IP, cfgPath, dir string, opts Options, emit func(Event), cancel context.CancelFunc) *worker {
	return &worker{
		entry:      entry,
		srcIP:      srcIP,
		cfgPath:    cfgPath,
		dir:        dir,
		opts:       opts,
		emit:       emit,
		status:     contract.AuthOffline,
		lastActive: true,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
}

func (w *worker) currentStatus() contract.AuthStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

func (w *worker) currentReason() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reason
}

// setStatus 只在状态真正变化时发事件，避免心跳式噪声把 UI 的事件流刷爆。
func (w *worker) setStatus(next contract.AuthStatus, reason string) {
	w.mu.Lock()
	prev := w.status
	w.status = next
	w.reason = reason
	w.mu.Unlock()

	if prev == next {
		return
	}
	if w.emit != nil {
		w.emit(Event{
			ID:     w.entry.ID,
			Prev:   prev,
			Next:   next,
			Reason: reason,
			At:     time.Now(),
		})
	}
}

func (w *worker) setProc(p *process) {
	w.mu.Lock()
	w.proc = p
	w.mu.Unlock()
}

func (w *worker) getProc() *process {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.proc
}

// run 是进程看护主循环。
//
// 只关心"进程还在不在"，不判断"认证成功没有" —— 后者是子进程内建 backoff 的事。
// 两层重试混在一起会互相干扰：外层疯狂重启时，内层还在退避，谁也做不成事。
func (w *worker) run(ctx context.Context) {
	defer close(w.done)

	delay := w.opts.RestartBase
	var crashes []time.Time

	for {
		if ctx.Err() != nil {
			return
		}

		p, err := startProcess(w.opts.BinaryPath, w.cfgPath, w.entry.Interface, w.dir)
		if err != nil {
			w.setStatus(contract.AuthOffline, "启动子进程失败: "+err.Error())
			if !sleepCtx(ctx, delay) {
				return
			}
			delay = nextBackoff(delay, w.opts.RestartMax)
			continue
		}

		w.setProc(p)
		w.setStatus(contract.AuthReauthenticating, "子进程已启动，等待探测确认在线")

		stopProbe := make(chan struct{})
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			w.probeLoop(ctx, stopProbe)
		}()

		code, _ := p.waitExit()
		// 必须立刻算存活时长。放到 wg.Wait() 之后算会把探测协程的收尾时间
		// 也算进去，导致"启动即崩"被误判成"稳定运行后崩溃"，
		// 配置错误的进程于是被无限重启，正好绕过下面这道防线。
		elapsed := time.Since(p.startedAt)

		close(stopProbe)
		w.wg.Wait()
		p.closeLog()
		w.setProc(nil)

		if ctx.Err() != nil {
			return
		}

		// 启动后立刻退出：几乎一定是配置问题（密码错、字段名对不上、二进制不可执行）。
		// 这种情况下无限重启只会反复冲击认证服务器，还会把账号推进风控。
		if elapsed < w.opts.ConfigErrorWindow {
			w.trip("子进程启动后 %.1f 秒即退出（退出码 %d），判定为配置错误，已停止重启。请检查 Config.yaml 与二进制版本是否匹配",
				elapsed.Seconds(), code)
			return
		}

		now := time.Now()
		crashes = append(crashes, now)
		crashes = withinWindow(crashes, now.Add(-w.opts.CrashWindow))
		if len(crashes) >= w.opts.CrashLimit {
			w.trip("%s 内崩溃 %d 次，已熔断。停止重启，等待人工介入（多半是配置或环境问题）",
				w.opts.CrashWindow.String(), len(crashes))
			return
		}

		// 稳定运行过才算真有进展，才把退避复位。
		// 没有这个窗口，启动即崩的 flapping 会让退避永远停在 1 秒。
		if elapsed >= w.opts.StabilizeAfter {
			delay = w.opts.RestartBase
		}

		w.setStatus(contract.AuthReauthenticating,
			fmt.Sprintf("子进程已退出（退出码 %d），%.0f 秒后重启", code, delay.Seconds()))

		if !sleepCtx(ctx, delay) {
			return
		}
		delay = nextBackoff(delay, w.opts.RestartMax)
	}
}

// probeLoop 周期性从本实例的源 IP 探测在线状态。
func (w *worker) probeLoop(ctx context.Context, stop <-chan struct{}) {
	// 子进程刚起来就探一定失败，白记一次错误。等它完成首次登录。
	// 这段等待必须能被 stop 打断：进程秒退时如果还傻等 3 秒，
	// 会把"启动即崩"的判定窗口撑过阈值。
	select {
	case <-ctx.Done():
		return
	case <-stop:
		return
	case <-time.After(3 * time.Second):
	}

	for {
		w.probeOnce(ctx)

		// 自适应周期：有流量时密（掉线用户立刻有感），空闲时疏（省数据）。
		// 用 Timer 而不是 Ticker，因为间隔会随活动状态变化。
		interval := w.opts.ProbeInterval
		active := true
		if w.opts.ActivityFunc != nil && !w.opts.ActivityFunc() {
			interval = w.opts.IdleProbeInterval
			active = false
		}
		if active != w.lastActive {
			w.lastActive = active
			if active {
				w.opts.Logger.Debug("探测切换到快速模式（有流量）",
					"id", w.entry.ID, "interval", interval)
			} else {
				w.opts.Logger.Debug("探测切换到省流模式（空闲）",
					"id", w.entry.ID, "interval", interval)
			}
		}
		timer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (w *worker) probeOnce(ctx context.Context) {
	r := probeOnce(ctx, w.srcIP, w.opts.ProbeURL, w.opts.ProbeTimeout)

	switch r.Kind {
	case ProbeOnline:
		w.netErrs = 0
		w.setStatus(contract.AuthOnline, "探测通过，在线")

	case ProbeUnauthenticated:
		w.netErrs = 0
		// 门户劫持是**确定的掉线信号**（不是网络抖动），立刻重认证，
		// 不等子进程的周期轮询——这是 HTTPS 流量下唯一可靠的观察点：
		// 未认证时门户在网络层拦截 TCP，代理层看到的建连是成功的，
		// 只有应用层探测才能识破。
		w.setStatus(contract.AuthReauthenticating, r.Reason)
		w.triggerReauth(ctx, "探测发现门户劫持："+r.Reason)

	case ProbeNetworkError:
		w.netErrs++
		if w.netErrs >= w.opts.NetworkErrorLimit {
			w.setStatus(contract.AuthReauthenticating,
				fmt.Sprintf("连续 %d 次探测失败：%s", w.netErrs, r.Reason))
		}
		// 单次失败不改状态。网络抖一下就把 IP 踢出调度池，
		// 会让调度层白白丢掉一条可用链路。
	}
}

// trip 熔断：状态置 offline，不再自动重启。
func (w *worker) trip(format string, args ...any) {
	reason := fmt.Sprintf(format, args...)
	w.setStatus(contract.AuthOffline, reason)
	w.opts.Logger.Error("认证实例熔断", "id", w.entry.ID, "reason", reason)
}

// stop 取消看护循环并确保子进程被清理。
func (w *worker) stop(timeout time.Duration) {
	w.cancel()

	if p := w.getProc(); p != nil {
		_ = p.stop(timeout)
	}

	<-w.done
	w.setStatus(contract.AuthOffline, "已停止")
	w.opts.Logger.Info("认证实例已停止", "id", w.entry.ID)
}

// nextBackoff 指数退避，封顶在 max。
func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next <= 0 {
		// 防溢出：cur 已经很大时乘 2 会变负。
		return max
	}
	if next > max {
		return max
	}
	return next
}

// withinWindow 丢掉窗口之外的崩溃记录，只保留最近的。
func withinWindow(ts []time.Time, since time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if t.After(since) {
			out = append(out, t)
		}
	}
	return out
}

// sleepCtx 可被取消的等待，返回 false 表示被取消。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
