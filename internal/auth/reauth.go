package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/Br1m2y/caulink/internal/drcom"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// drcomClient 用实例的账号与源 IP 构造客户端。
func (w *worker) drcomClient() drcom.Client {
	return drcom.Client{
		Portal:   w.opts.Account.Portal,
		Port:     w.opts.Account.Port,
		Username: w.opts.Account.Username,
		Realm:    w.opts.Account.Realm,
		Password: w.opts.Account.Password,
		SourceIP: w.entry.SourceIP,
	}
}

// triggerReauth 异步发起一次事件驱动重认证。
//
// 用独立的 ctx（WithoutCancel）：进程正在收尾时也要把这次登录做完，
// 否则会留下"已判定掉线但没重登"的空窗。
//
// 用原子标志防止探测频繁时堆积 goroutine：同一实例同时只允许一次在飞。
func (w *worker) triggerReauth(ctx context.Context, reason string) {
	if !w.reauthing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer w.reauthing.Store(false)
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := w.forceReauth(rctx, reason); err != nil && w.opts.Logger != nil {
			w.opts.Logger.Error("即时重认证失败", "id", w.entry.ID, "err", err)
		}
	}()
}

// forceReauth 是事件驱动的重认证入口：探测一次，确实离线才登录。
//
// 为什么要先探测再登录：触发源（代理层建连失败、探测发现劫持）可能来自
// 目标站点或瞬时抖动，而不是认证掉了。拿"从该源 IP 能否拿到连通性检测页"做
// 一次确认，代价只有几百字节，却能把误判挡在门外。
//
// 冷却期内的调用直接返回 nil：一次掉线会伴随成串的触发信号，没必要重复登录。
func (w *worker) forceReauth(ctx context.Context, reason string) error {
	w.mu.Lock()
	if !w.lastForce.IsZero() && time.Since(w.lastForce) < w.opts.ForceCooldown {
		w.mu.Unlock()
		return nil
	}
	w.lastForce = time.Now()
	w.mu.Unlock()

	client := w.drcomClient()

	online, err := client.Probe(ctx)
	if err != nil && w.opts.Logger != nil {
		w.opts.Logger.Debug("事件驱动：在线探测失败", "id", w.entry.ID, "err", err)
	}
	if online {
		// 链路本来是通的：触发信号来自目标站点，不动认证状态。
		return nil
	}

	w.setStatus(contract.AuthReauthenticating, reason+"，立即重认证")

	if err := client.Login(ctx); err != nil {
		w.setStatus(contract.AuthOffline, "事件驱动重认证失败: "+err.Error())
		return fmt.Errorf("auth: 源 IP %s 事件驱动重认证失败: %w", w.entry.SourceIP, err)
	}

	w.setStatus(contract.AuthOnline, "事件驱动重认证成功")
	return nil
}

// ReauthNow 见 Manager 接口。
func (s *Supervisor) ReauthNow(ctx context.Context, sourceIP string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSupervisorClosed
	}
	var target *worker
	for _, cand := range s.workers {
		if cand.entry.SourceIP == sourceIP {
			target = cand
			break
		}
	}
	s.mu.Unlock()

	if target == nil {
		return fmt.Errorf("auth: 没有源 IP %s 的认证实例", sourceIP)
	}
	return target.forceReauth(ctx, "代理层观察到上游失败")
}
