package main

// 通用「按住系统代理」控制器。
//
// 场景：某个程序只认系统代理（例如迅雷的"使用系统代理服务器"选项），
// 而本地 VPN 客户端每 8 秒会把系统代理改回它自己。这里持续把代理按在
// caulinkd 的 HTTP 代理上，持续 N 分钟后自动还原。
//
// 与 Steam 模式的区别：不碰任何进程，不重启谁，纯粹压注册表。
// 所以它适用于"打开后自己读一次代理"的程序，不适合需要重启才生效的程序
// （那种请用 Steam 模式）。

import (
	"context"
	"errors"
	"fmt"
	"github.com/Br1m2y/caulink/internal/control"
	"log/slog"
	"sync"
	"time"
)

// 默认按住 10 分钟：够下完一个中等文件，又不至于忘了恢复。
const defaultProxyHold = 10 * time.Minute

type proxyHoldController struct {
	logger *slog.Logger
	proxy  string

	mu sync.Mutex
	// running / started / endsAt 描述"是否在按住、按了多久"
	running bool
	started time.Time
	endsAt  time.Time
	// snapshot 是开始按住前的完整系统代理配置，结束时按原样还原
	snapshot proxySnapshot
	forever  bool
	lastErr  string
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func newProxyHoldController(logger *slog.Logger, proxyAddr string) *proxyHoldController {
	return &proxyHoldController{logger: logger, proxy: proxyAddr}
}

// ProxyHoldStatus 是给控制面的状态快照。
type ProxyHoldStatus struct {
	Running         bool      `json:"running"`
	Proxy           string    `json:"proxy"`
	RemainSeconds   int       `json:"remain_seconds"`
	PreviousProxy   string    `json:"previous_proxy,omitempty"`
	PreviousEnabled bool      `json:"previous_enabled"`
	Error           string    `json:"error,omitempty"`
	At              time.Time `json:"at"`
}

// Start 开始按住系统代理。
func (c *proxyHoldController) Start(seconds int) error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return errors.New("已经在按住系统代理了")
	}
	// seconds 语义：
	//   > 0  按时长自动结束
	//   = 0  用默认时长
	//   < 0  一直按住，直到手动关闭（大文件下载需要）
	forever := seconds < 0
	dur := defaultProxyHold
	if seconds > 0 {
		dur = time.Duration(seconds) * time.Second
	}

	// 完整快照：地址 + 开关 + 例外列表 + PAC。
	// 只记地址会在停止时把系统留在"启用代理但指向别处"，直接断网。
	snap := readProxySnapshot()
	// 原值就是我们自己时不记录地址，否则停止后会把代理"还原"成我们，
	// 导致系统代理一直停在 8080（踩过这个坑）。
	if snap.Server == c.proxy {
		snap.Server = ""
	}

	if err := setSystemProxy(c.proxy); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("写入系统代理失败: %w", err)
	}

	c.running = true
	c.started = time.Now()
	c.forever = forever
	if forever {
		c.endsAt = time.Time{}
	} else {
		c.endsAt = c.started.Add(dur)
	}
	c.snapshot = snap
	c.lastErr = ""
	c.stopCh = make(chan struct{})
	c.doneCh = make(chan struct{})
	stopCh, doneCh := c.stopCh, c.doneCh
	c.mu.Unlock()

	go c.holdLoop(dur, forever, stopCh, doneCh)
	c.logger.Info("开始按住系统代理", "proxy", c.proxy, "duration", dur.String())
	return nil
}

// Stop 提前停止并还原。
func (c *proxyHoldController) Stop() error {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	stopCh := c.stopCh
	c.running = false
	c.mu.Unlock()

	close(stopCh)
	c.restore()
	return nil
}

// Status 返回当前状态。
func (c *proxyHoldController) Status() ProxyHoldStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	remain := 0
	if c.running && !c.forever {
		if d := time.Until(c.endsAt); d > 0 {
			remain = int(d.Seconds())
		}
	}
	if c.running && c.forever {
		remain = -1 // UI 用它显示"直到手动关闭"
	}
	return ProxyHoldStatus{
		Running:         c.running,
		Proxy:           c.proxy,
		RemainSeconds:   remain,
		PreviousProxy:   c.snapshot.Server,
		PreviousEnabled: c.snapshot.Enable,
		Error:           c.lastErr,
		At:              time.Now(),
	}
}

// holdLoop 每 700ms 抢回一次系统代理。
//
// 频率与被抢的实测一致：本地 VPN 客户端约 8 秒改一次，
// 700ms 足够让任何"启动时读一次代理"的程序都读到我们的值。
func (c *proxyHoldController) holdLoop(dur time.Duration, forever bool, stopCh, doneCh chan struct{}) {
	defer close(doneCh)
	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()

	deadline := time.Now().Add(dur)
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			if err := setSystemProxy(c.proxy); err != nil {
				c.mu.Lock()
				c.lastErr = err.Error()
				c.mu.Unlock()
			}
			if !forever && time.Now().After(deadline) {
				c.mu.Lock()
				c.running = false
				c.mu.Unlock()
				c.restore()
				c.logger.Info("按住系统代理结束，已还原")
				return
			}
		}
	}
}

// restore 把系统代理完整还原成开始前的样子（地址 / 开关 / 例外 / PAC）。
func (c *proxyHoldController) restore() {
	c.mu.Lock()
	snap := c.snapshot
	c.mu.Unlock()

	if err := restoreProxySnapshot(snap); err != nil {
		c.logger.Warn("还原系统代理失败", "err", err)
		return
	}
	if snap.Enable {
		c.logger.Info("系统代理已还原", "server", snap.Server, "enabled", true)
	} else {
		c.logger.Info("系统代理已还原为「不使用代理」")
	}
}

// StopWithContext 让调用方受 context 约束（当前未使用，保留给优雅退出）。
func (c *proxyHoldController) StopWithContext(ctx context.Context) error {
	done := make(chan struct{})
	go func() { _ = c.Stop(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---- 适配 control.ProxyHoldController ----

// StartProxyHold 实现 control.ProxyHoldController。
func (c *proxyHoldController) StartProxyHold(seconds int) error { return c.Start(seconds) }

// StopProxyHold 实现 control.ProxyHoldController。
func (c *proxyHoldController) StopProxyHold() error { return c.Stop() }

// ProxyHoldStatus 实现 control.ProxyHoldController。
func (c *proxyHoldController) ProxyHoldStatus() control.ProxyHoldStatus {
	st := c.Status()
	return control.ProxyHoldStatus{
		Running:       st.Running,
		Proxy:         st.Proxy,
		RemainSeconds: st.RemainSeconds,
		PreviousProxy: st.PreviousProxy,
		Error:         st.Error,
	}
}
