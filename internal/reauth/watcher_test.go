package reauth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor 轮询等待条件成立，避免用固定 sleep 让测试变慢或不稳。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestWatcherFiresAfterThreshold(t *testing.T) {
	var called int32
	w := New(Options{
		Threshold: 3,
		Window:    time.Second,
		Cooldown:  time.Minute, // 本用例不涉及冷却
		Handler: func(context.Context, string) error {
			atomic.AddInt32(&called, 1)
			return nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// 前两次不够数，不应触发。
	w.OnDialFailure("10.0.0.2")
	w.OnDialFailure("10.0.0.2")
	time.Sleep(80 * time.Millisecond)
	if n := atomic.LoadInt32(&called); n != 0 {
		t.Fatalf("未达阈值就触发了 %d 次", n)
	}

	// 第三次达标。
	w.OnDialFailure("10.0.0.2")
	if !waitFor(t, 2*time.Second, func() bool { return atomic.LoadInt32(&called) == 1 }) {
		t.Fatal("达到阈值后没有触发重认证")
	}
}

func TestWatcherCooldownSuppressesBurst(t *testing.T) {
	var called int32
	w := New(Options{
		Threshold: 1,
		Window:    time.Second,
		Cooldown:  time.Minute,
		Handler: func(context.Context, string) error {
			atomic.AddInt32(&called, 1)
			return nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// 一次掉线会伴随成串失败：只应该触发一次。
	for i := 0; i < 50; i++ {
		w.OnDialFailure("10.0.0.3")
	}

	if !waitFor(t, 2*time.Second, func() bool { return atomic.LoadInt32(&called) >= 1 }) {
		t.Fatal("首次失败应触发重认证")
	}
	time.Sleep(200 * time.Millisecond)
	if n := atomic.LoadInt32(&called); n != 1 {
		t.Fatalf("冷却期内应只触发 1 次，实际 %d 次", n)
	}
}

func TestWatcherIsolatesSourceIPs(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	w := New(Options{
		Threshold: 2,
		Window:    time.Second,
		Cooldown:  time.Millisecond,
		Handler: func(_ context.Context, srcIP string) error {
			mu.Lock()
			seen[srcIP]++
			mu.Unlock()
			return nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// 两个 IP 各报一次：各自都没到阈值，不应触发。
	w.OnDialFailure("10.0.0.4")
	w.OnDialFailure("10.0.0.5")
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	if len(seen) != 0 {
		mu.Unlock()
		t.Fatalf("不同源 IP 的失败被错误地合并计数: %v", seen)
	}
	mu.Unlock()

	// 给其中一个补到阈值。
	w.OnDialFailure("10.0.0.4")
	if !waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seen["10.0.0.4"] == 1
	}) {
		t.Fatal("达到阈值的那个 IP 没有触发")
	}

	mu.Lock()
	defer mu.Unlock()
	if seen["10.0.0.5"] != 0 {
		t.Fatalf("另一个 IP 不应被触发: %v", seen)
	}
}

func TestWatcherIgnoresEmptyIP(t *testing.T) {
	var called int32
	w := New(Options{
		Threshold: 1,
		Handler: func(context.Context, string) error {
			atomic.AddInt32(&called, 1)
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	w.OnDialFailure("")
	time.Sleep(80 * time.Millisecond)
	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("空源 IP 不应触发")
	}
}

func TestWatcherStopsOnCancel(t *testing.T) {
	w := New(Options{
		Threshold: 1,
		Handler:   func(context.Context, string) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ctx 取消后 Run 没有退出")
	}
}
