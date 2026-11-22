package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// errSupervisorClosed 在 Close 之后再 Start 时返回。
var errSupervisorClosed = errors.New("auth: Supervisor 已关闭")

// Supervisor 是 Manager 的实现，管理全部认证实例。
type Supervisor struct {
	opts Options

	mu      sync.Mutex
	workers map[string]*worker
	closed  bool

	// sendMu 保护 events 的发送与关闭，避免 emit 与 close 竞态导致 panic。
	sendMu sync.RWMutex
	events chan Event
}

// Start 见 Manager 接口。
//
// 注意 ctx 只用于"启动前是否已取消"的检查，不作为实例生命周期。
// 调用方传进来的 ctx 通常绑在短作用域（一次 HTTP 请求、一个启动阶段）上，
// 拿它当生命周期会导致实例刚起来就被取消。
func (s *Supervisor) Start(ctx context.Context, entry contract.IPPoolEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateEntry(entry); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return errSupervisorClosed
	}
	if _, exists := s.workers[entry.ID]; exists {
		return nil // 幂等
	}

	cfgPath, dir, err := s.opts.writeInstanceConfig(entry)
	if err != nil {
		return err
	}

	srcIP := net.ParseIP(entry.SourceIP)
	if srcIP == nil {
		return fmt.Errorf("auth: source_ip %q 无法解析", entry.SourceIP)
	}

	workerCtx, cancel := context.WithCancel(context.Background())
	w := newWorker(entry, srcIP, cfgPath, dir, s.opts, s.emit, cancel)
	s.workers[entry.ID] = w

	s.opts.Logger.Info("启动认证实例",
		"id", entry.ID, "iface", entry.Interface, "src_ip", entry.SourceIP)

	go w.run(workerCtx)
	return nil
}

// Stop 见 Manager 接口。
func (s *Supervisor) Stop(id string) error {
	s.mu.Lock()
	w, ok := s.workers[id]
	if ok {
		delete(s.workers, id)
	}
	s.mu.Unlock()

	if !ok {
		return nil
	}
	w.stop(s.opts.StopTimeout)
	return nil
}

// Status 见 Manager 接口。
func (s *Supervisor) Status(id string) (contract.AuthStatus, error) {
	s.mu.Lock()
	w, ok := s.workers[id]
	s.mu.Unlock()

	if !ok {
		return contract.AuthOffline, nil
	}
	return w.currentStatus(), nil
}

// Reason 返回最近一次状态变更的原因，用于 UI 展示与排障。
// 不在 Manager 接口里：接口保持最小，需要细节的调用方拿具体类型即可。
func (s *Supervisor) Reason(id string) string {
	s.mu.Lock()
	w, ok := s.workers[id]
	s.mu.Unlock()

	if !ok {
		return ""
	}
	return w.currentReason()
}

// Events 见 Manager 接口。
func (s *Supervisor) Events() <-chan Event {
	return s.events
}

// Close 停止所有实例。
//
// 顺序很关键：先停 worker（它们还在发事件），再关 events。
// 反过来会 panic: send on closed channel。
func (s *Supervisor) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ws := make([]*worker, 0, len(s.workers))
	for _, w := range s.workers {
		ws = append(ws, w)
	}
	s.workers = map[string]*worker{}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, w := range ws {
		wg.Add(1)
		go func(w *worker) {
			defer wg.Done()
			w.stop(s.opts.StopTimeout)
		}(w)
	}
	wg.Wait()

	s.sendMu.Lock()
	close(s.events)
	s.sendMu.Unlock()
	return nil
}

// emit 非阻塞发送事件。
//
// 队列满时丢弃而不是阻塞：调度层来不及消费不能反过来卡住认证层，
// 否则一个慢消费者就能拖垮整个认证子系统。丢弃会有日志，便于发现。
func (s *Supervisor) emit(e Event) {
	s.sendMu.RLock()
	defer s.sendMu.RUnlock()

	select {
	case s.events <- e:
	default:
		s.opts.Logger.Warn("事件队列已满，丢弃事件",
			"id", e.ID, "next", string(e.Next))
	}
}

// validateEntry 只校验认证层真正需要的字段。
//
// 不调用 entry.Validate()：那条校验要求 health_score、rtt_ms、last_seen 等
// 由 B 层填写的字段也合法，而认证层启动时这些字段本来就是空的。
func validateEntry(e contract.IPPoolEntry) error {
	if e.ID == "" {
		return errors.New("auth: entry.ID 为空")
	}
	if e.Interface == "" {
		return fmt.Errorf("auth: entry.Interface 为空 (id %q)", e.ID)
	}
	ip := net.ParseIP(e.SourceIP)
	if ip == nil {
		return fmt.Errorf("auth: source_ip %q 不是合法 IP (id %q)", e.SourceIP, e.ID)
	}
	if ip.IsUnspecified() || ip.IsLoopback() {
		return fmt.Errorf("auth: source_ip %q 不能用于认证 (id %q)", e.SourceIP, e.ID)
	}
	return nil
}
