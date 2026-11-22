package metrics

import (
	"sync"
	"testing"
	"time"
)

func TestCounterLifecycle(t *testing.T) {
	r := New()

	r.ConnOpened("a")
	r.ConnOpened("a")
	r.ConnClosed("a")
	r.AddUp("a", 100)
	r.AddDown("a", 400)
	r.AddError("a")

	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("应有 1 条样本，实际 %d", len(got))
	}
	s := got[0]
	if s.ActiveConns != 1 {
		t.Errorf("活跃连接 = %d，期望 1", s.ActiveConns)
	}
	if s.TotalConns != 2 {
		t.Errorf("累计连接 = %d，期望 2", s.TotalConns)
	}
	if s.BytesUp != 100 || s.BytesDown != 400 {
		t.Errorf("字节 = up %d / down %d", s.BytesUp, s.BytesDown)
	}
	if s.Errors != 1 {
		t.Errorf("错误数 = %d", s.Errors)
	}
}

// 配对出错时不能让 UI 显示负数。
func TestConnClosedNeverGoesNegative(t *testing.T) {
	r := New()
	r.ConnClosed("a")
	r.ConnClosed("a")

	if got := r.Snapshot()[0].ActiveConns; got != 0 {
		t.Errorf("活跃连接 = %d，期望夹到 0", got)
	}
}

func TestAddIgnoresNonPositive(t *testing.T) {
	r := New()
	r.ConnOpened("a") // 先建条目，否则 AddUp 早退不会创建计数器
	r.AddUp("a", 0)
	r.AddUp("a", -5)
	r.AddDown("a", -1)

	s := r.Snapshot()[0]
	if s.BytesUp != 0 || s.BytesDown != 0 {
		t.Errorf("非正数字节不应累加: up %d down %d", s.BytesUp, s.BytesDown)
	}
}

// 只有非正数字节、从未有过连接的 ID 不该在快照里占位。
func TestNoCounterForUnknownId(t *testing.T) {
	r := New()
	r.AddUp("never-seen", 0)
	r.AddDown("never-seen", -3)

	if got := r.Snapshot(); len(got) != 0 {
		t.Errorf("不该为未使用的 ID 建条目，得到 %d 条", len(got))
	}
}

// 快照必须与内部状态隔离，调用方改了不影响下次读数。
func TestSnapshotIsIsolated(t *testing.T) {
	r := New()
	r.ConnOpened("a")

	first := r.Snapshot()
	first[0].ActiveConns = 999

	if got := r.Snapshot()[0].ActiveConns; got != 1 {
		t.Errorf("快照被写穿: %d", got)
	}
}

func TestSnapshotSortedByID(t *testing.T) {
	r := New()
	for _, id := range []string{"c", "a", "b"} {
		r.ConnOpened(id)
	}

	got := r.Snapshot()
	for i := 1; i < len(got); i++ {
		if got[i-1].ID > got[i].ID {
			t.Fatalf("未按 ID 排序: %v", []string{got[0].ID, got[1].ID, got[2].ID})
		}
	}
}

// 首次采样只建立基线，不能产出一个巨大且错误的速率。
func TestFirstSampleOnlySetsBaseline(t *testing.T) {
	r := New()
	t0 := time.Unix(1_700_000_000, 0)

	r.AddUp("a", 5000)
	r.sampleOnce(t0)

	if rate := r.Snapshot()[0].Rate; rate.UpBps != 0 || rate.DownBps != 0 {
		t.Fatalf("第一帧不应产出速率，得到 %+v", rate)
	}
}

func TestRateConvergesToActualThroughput(t *testing.T) {
	r := New()
	t0 := time.Unix(1_700_000_000, 0)

	r.ConnOpened("a") // 先建条目，基线才有参照对像
	r.sampleOnce(t0)

	// 连续 10 秒、每秒 1MB 上行
	const perSecond = 1_000_000
	now := t0
	var last float64
	for i := 0; i < 10; i++ {
		r.AddUp("a", perSecond)
		now = now.Add(time.Second)
		r.sampleOnce(now)
		last = r.Snapshot()[0].Rate.UpBps
	}

	// EWMA 从 0 起步，10 帧后应逼近真实值（0.7^10 ≈ 2.8% 残差）。
	if last < perSecond*0.95 || last > perSecond*1.05 {
		t.Fatalf("速率应收敛到 %d 附近，实际 %.0f", perSecond, last)
	}
}

// 计数器被重置（条目重建）时这一帧必须丢弃，不能算出负数或天文数字。
func TestRateHandlesCounterReset(t *testing.T) {
	r := New()
	t0 := time.Unix(1_700_000_000, 0)

	r.AddUp("a", 1_000_000)
	r.sampleOnce(t0)

	// 模拟条目重建：计数器归零
	r.get("a").BytesUp.Store(0)
	r.AddUp("a", 500)
	r.sampleOnce(t0.Add(time.Second))

	if rate := r.Snapshot()[0].Rate.UpBps; rate < 0 {
		t.Fatalf("重置后速率出现负值: %f", rate)
	}
}

// 实际间隔偏离标称值时，速率要按真实 dt 算。
func TestRateUsesActualInterval(t *testing.T) {
	r := New()
	t0 := time.Unix(1_700_000_000, 0)

	r.ConnOpened("a")
	r.sampleOnce(t0)

	r.AddUp("a", 2_000_000)
	// 间隔 2 秒而不是 1 秒，速率应是 1MB/s 而不是 2MB/s
	r.sampleOnce(t0.Add(2 * time.Second))

	rate := r.Snapshot()[0].Rate.UpBps
	// 首帧 EWMA：0.3 * 1_000_000 = 300_000
	if rate < 290_000 || rate > 310_000 {
		t.Fatalf("按真实 dt 计算的速率应为 300000 左右，实际 %.0f", rate)
	}
}

// 条目的第一帧没有参照值，只记录基线，速率为 0，下一帧才有效。
func TestNewEntryGetsRateFromSecondSample(t *testing.T) {
	r := New()
	t0 := time.Unix(1_700_000_000, 0)
	r.sampleOnce(t0) // 空基线

	r.AddUp("late", 1_000_000)
	r.sampleOnce(t0.Add(time.Second))
	if rate := r.Snapshot()[0].Rate.UpBps; rate != 0 {
		t.Fatalf("新条目首帧速率应为 0，实际 %.0f", rate)
	}

	r.AddUp("late", 1_000_000)
	r.sampleOnce(t0.Add(2 * time.Second))
	if rate := r.Snapshot()[0].Rate.UpBps; rate <= 0 {
		t.Fatalf("第二帧应开始产出速率，实际 %.0f", rate)
	}
}

func TestRemovedEntryStopsProducingRate(t *testing.T) {
	r := New()
	t0 := time.Unix(1_700_000_000, 0)

	r.AddUp("gone", 1000)
	r.sampleOnce(t0)
	r.AddUp("gone", 1000)
	r.sampleOnce(t0.Add(time.Second))

	// 条目被移出注册表（模拟 IP 下线后清理）
	r.mu.Lock()
	delete(r.counters, "gone")
	r.mu.Unlock()

	r.sampleOnce(t0.Add(2 * time.Second))

	r.mu.RLock()
	_, hasRate := r.rates["gone"]
	r.mu.RUnlock()
	if hasRate {
		t.Error("已移除的条目不应残留速率记录")
	}
}

func TestConcurrentAccess(t *testing.T) {
	r := New()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.ConnOpened("a")
				r.AddUp("a", 10)
				r.AddDown("a", 20)
				r.ConnClosed("a")
				_ = r.Snapshot()
			}
		}()
	}
	wg.Wait()

	s := r.Snapshot()[0]
	if s.ActiveConns != 0 {
		t.Errorf("并发结束后活跃连接应为 0，实际 %d", s.ActiveConns)
	}
	if s.TotalConns != 1600 {
		t.Errorf("累计连接 = %d，期望 1600", s.TotalConns)
	}
}
