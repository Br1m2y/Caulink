package scheduler

import (
	"testing"
	"time"

	"github.com/Br1m2y/caulink/internal/ippool"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

func mkEntry(id string, score float64) contract.IPPoolEntry {
	return contract.IPPoolEntry{
		ID:          id,
		Interface:   "eth0",
		SourceIP:    "10.0.0." + id,
		HealthScore: score,
	}
}

func mkPool(t *testing.T, entries ...contract.IPPoolEntry) *ippool.Pool {
	t.Helper()
	p := ippool.New()
	for _, e := range entries {
		if err := p.Upsert(e); err != nil {
			t.Fatal(err)
		}
		if err := p.UpdateAuth(e.ID, contract.AuthOnline, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func bulkOnly() Classifier {
	return ClassifyFunc(func(string, string) Class { return ClassBulk })
}

func interactiveOnly() Classifier {
	return ClassifyFunc(func(string, string) Class { return ClassInteractive })
}

func TestPickReturnsErrorOnEmptyPool(t *testing.T) {
	s, err := New(ippool.New(), bulkOnly())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pick("tcp", "example.com:443"); err != ErrNoCandidate {
		t.Fatalf("空池应返回 ErrNoCandidate，实际 %v", err)
	}
}

func TestNewRejectsNilPool(t *testing.T) {
	if _, err := New(nil, nil); err == nil {
		t.Fatal("nil pool 应报错")
	}
}

// 平滑加权轮询必须让分配比例等于健康分比例。
func TestWeightedDistribution(t *testing.T) {
	pool := mkPool(t, mkEntry("1", 0.8), mkEntry("2", 0.2))
	s, err := New(pool, bulkOnly())
	if err != nil {
		t.Fatal(err)
	}

	const rounds = 1000
	counts := map[string]int{}
	for i := 0; i < rounds; i++ {
		e, err := s.Pick("tcp", "example.com:443")
		if err != nil {
			t.Fatal(err)
		}
		counts[e.ID]++
	}

	// 权重 80:20，允许少量浮动
	if counts["1"] < 750 || counts["1"] > 850 {
		t.Errorf("高健康分 IP 应占比约 80%%，实际 %d/%d", counts["1"], rounds)
	}
	if counts["2"] < 150 || counts["2"] > 250 {
		t.Errorf("低健康分 IP 应占比约 20%%，实际 %d/%d", counts["2"], rounds)
	}
}

// 权重极小的 IP 仍然要被选中一部分，不能因为取整变成永不使用。
func TestTinyWeightStillGetsTraffic(t *testing.T) {
	pool := mkPool(t, mkEntry("1", 0.99), mkEntry("2", 0.004))
	s, err := New(pool, bulkOnly())
	if err != nil {
		t.Fatal(err)
	}

	counts := map[string]int{}
	for i := 0; i < 2000; i++ {
		e, _ := s.Pick("tcp", "example.com:443")
		counts[e.ID]++
	}
	if counts["2"] == 0 {
		t.Fatal("健康分 0.004 的 IP 也应分到流量（权重下限为 1）")
	}
}

func TestPickSkipsOfflineEntry(t *testing.T) {
	pool := mkPool(t, mkEntry("1", 0.9), mkEntry("2", 0.5))
	s, _ := New(pool, bulkOnly())

	// 把 1 下线，之后所有连接都必须走 2
	if err := pool.UpdateAuth("1", contract.AuthOffline, time.Now()); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		e, err := s.Pick("tcp", "example.com:443")
		if err != nil {
			t.Fatal(err)
		}
		if e.ID != "2" {
			t.Fatalf("1 已离线，不该被选中，实际 %s", e.ID)
		}
	}
}

// 敏感流量必须固定到最优 IP，而不是每次重选。
func TestInteractiveIsSticky(t *testing.T) {
	pool := mkPool(t, mkEntry("1", 0.9), mkEntry("2", 0.5))
	s, _ := New(pool, interactiveOnly())

	first, err := s.Pick("tcp", "game:27015")
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 10; i++ {
		e, _ := s.Pick("tcp", "game:27015")
		if e.ID != first.ID {
			t.Fatalf("敏感流量每次都应走同一个 IP，第一次 %s，第 %d 次 %s", first.ID, i, e.ID)
		}
	}
	if s.StickyID() != first.ID {
		t.Errorf("StickyID 应为 %s，实际 %s", first.ID, s.StickyID())
	}
}

// 更好的 IP 出现时，只有在明显更优的情况下才切换，否则会引发频繁换线掉线。
func TestInteractiveHysteresis(t *testing.T) {
	pool := mkPool(t, mkEntry("1", 0.9), mkEntry("2", 0.5))
	s, _ := New(pool, interactiveOnly())

	first, _ := s.Pick("tcp", "game:27015")
	if first.ID != "1" {
		t.Fatalf("首次应选最优的 1，实际 %s", first.ID)
	}

	// 加入一个只略好的 IP：不应该抢走粘性
	if err := pool.Upsert(mkEntry("3", 0.95)); err != nil {
		t.Fatal(err)
	}
	_ = pool.UpdateAuth("3", contract.AuthOnline, time.Now())

	got, _ := s.Pick("tcp", "game:27015")
	if got.ID != "1" {
		t.Errorf("0.95 只比 0.9 高一点，不该切换（滞回阈值 0.8），实际切到 %s", got.ID)
	}

	// 当前 IP 明显劣化后必须切换
	if err := pool.UpdateHealth("1", 0.1, 500, 0.5); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Pick("tcp", "game:27015")
	if got.ID != "3" {
		t.Errorf("1 劣化到 0.1 后应切到最优的 3，实际 %s", got.ID)
	}
}

// 粘住的 IP 掉线后必须立刻换，不能因为"粘性"继续往死路上送。
func TestInteractiveSwitchesWhenStickyGoesOffline(t *testing.T) {
	pool := mkPool(t, mkEntry("1", 0.9), mkEntry("2", 0.5))
	s, _ := New(pool, interactiveOnly())

	first, _ := s.Pick("tcp", "game:27015")
	_ = pool.UpdateAuth(first.ID, contract.AuthOffline, time.Now())

	got, err := s.Pick("tcp", "game:27015")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == first.ID {
		t.Fatal("粘性 IP 已离线，不该继续选中")
	}
}

func TestNewEntryJoinsDistribution(t *testing.T) {
	pool := mkPool(t, mkEntry("1", 0.9))
	s, _ := New(pool, bulkOnly())

	if e, _ := s.Pick("tcp", "x:443"); e.ID != "1" {
		t.Fatalf("只有 1 个 IP 时应选它，实际 %s", e.ID)
	}

	if err := pool.Upsert(mkEntry("2", 0.9)); err != nil {
		t.Fatal(err)
	}
	_ = pool.UpdateAuth("2", contract.AuthOnline, time.Now())

	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		e, _ := s.Pick("tcp", "x:443")
		counts[e.ID]++
	}
	if counts["2"] == 0 {
		t.Fatal("新加入的 IP 应参与分配")
	}
}

// 池里的 IP 全部消失后，中间状态必须被清理，否则长时间运行会持续增长。
func TestGCClearsStaleState(t *testing.T) {
	pool := mkPool(t, mkEntry("1", 0.9), mkEntry("2", 0.9))
	s, _ := New(pool, bulkOnly())
	for i := 0; i < 10; i++ {
		_, _ = s.Pick("tcp", "x:443")
	}

	pool.Remove("1")
	pool.Remove("2")
	pool.Upsert(mkEntry("3", 0.9))
	_ = pool.UpdateAuth("3", contract.AuthOnline, time.Now())

	_, _ = s.Pick("tcp", "x:443")

	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.current {
		if id == "1" || id == "2" {
			t.Fatalf("已移除的 IP %s 仍留在 current 里", id)
		}
	}
}
