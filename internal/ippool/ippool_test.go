package ippool

import (
	"sync"
	"testing"
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

func entry(id, iface, ip string) contract.IPPoolEntry {
	return contract.IPPoolEntry{ID: id, Interface: iface, SourceIP: ip}
}

// 这是本包存在的唯一理由：A 和 B 各自写自己的字段，互不覆盖。
func TestUpdateAuthDoesNotClobberHealth(t *testing.T) {
	p := New()
	e := entry("eth0@10.0.0.2", "eth0", "10.0.0.2")
	if err := p.Upsert(e); err != nil {
		t.Fatal(err)
	}

	if err := p.UpdateHealth(e.ID, 0.9, 12.5, 0.01); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateAuth(e.ID, contract.AuthOnline, time.Now()); err != nil {
		t.Fatal(err)
	}

	got, _ := p.Get(e.ID)
	if got.HealthScore != 0.9 || got.RTTMs != 12.5 || got.LossRate != 0.01 {
		t.Fatalf("UpdateAuth 污染了健康字段: score=%.2f rtt=%.2f loss=%.2f",
			got.HealthScore, got.RTTMs, got.LossRate)
	}
	if got.AuthStatus != contract.AuthOnline {
		t.Fatalf("auth_status 应为 online，实际 %s", got.AuthStatus)
	}
}

func TestUpdateHealthDoesNotClobberAuth(t *testing.T) {
	p := New()
	e := entry("eth0@10.0.0.2", "eth0", "10.0.0.2")
	if err := p.Upsert(e); err != nil {
		t.Fatal(err)
	}

	at := time.Now().Add(-time.Minute)
	if err := p.UpdateAuth(e.ID, contract.AuthOnline, at); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateHealth(e.ID, 0.5, 80, 0.2); err != nil {
		t.Fatal(err)
	}

	got, _ := p.Get(e.ID)
	if got.AuthStatus != contract.AuthOnline {
		t.Fatalf("UpdateHealth 污染了 auth_status: %s", got.AuthStatus)
	}
	if !got.LastSeen.Equal(at.UTC()) {
		t.Fatalf("UpdateHealth 不应改动 last_seen")
	}
}

func TestSchedulableFiltersOfflineAndZeroScore(t *testing.T) {
	p := New()
	must := func(e contract.IPPoolEntry) {
		if err := p.Upsert(e); err != nil {
			t.Fatal(err)
		}
	}
	must(entry("a@1.1.1.1", "a", "1.1.1.1"))
	must(entry("b@2.2.2.2", "b", "2.2.2.2"))
	must(entry("c@3.3.3.3", "c", "3.3.3.3"))

	// a: 在线且健康 → 可调度
	_ = p.UpdateAuth("a@1.1.1.1", contract.AuthOnline, time.Now())
	_ = p.UpdateHealth("a@1.1.1.1", 0.8, 20, 0)

	// b: 认证中 → 不可调度
	_ = p.UpdateAuth("b@2.2.2.2", contract.AuthReauthenticating, time.Now())
	_ = p.UpdateHealth("b@2.2.2.2", 0.9, 20, 0)

	// c: 在线但健康分为 0 → 不可调度
	_ = p.UpdateAuth("c@3.3.3.3", contract.AuthOnline, time.Now())
	_ = p.UpdateHealth("c@3.3.3.3", 0, 20, 1)

	got := p.Schedulable()
	if len(got) != 1 || got[0].ID != "a@1.1.1.1" {
		t.Fatalf("应只有 a 可调度，实际 %+v", got)
	}
}

func TestUpdateHealthClampsOutOfRange(t *testing.T) {
	p := New()
	e := entry("eth0@10.0.0.2", "eth0", "10.0.0.2")
	_ = p.Upsert(e)

	// 探测抖动可能给出越界值，夹紧而不是报错，否则一条链路会因为一次毛刺被丢弃。
	if err := p.UpdateHealth(e.ID, 1.7, -5, 1.9); err != nil {
		t.Fatalf("越界值不应报错: %v", err)
	}
	got, _ := p.Get(e.ID)
	if got.HealthScore != 1 || got.RTTMs != 0 || got.LossRate != 1 {
		t.Fatalf("夹紧结果不对: score=%.2f rtt=%.2f loss=%.2f",
			got.HealthScore, got.RTTMs, got.LossRate)
	}
}

func TestUpdateRejectsUnknownID(t *testing.T) {
	p := New()
	if err := p.UpdateAuth("nope", contract.AuthOnline, time.Now()); err == nil {
		t.Fatal("未知 ID 的 UpdateAuth 应报错")
	}
	if err := p.UpdateHealth("nope", 0.5, 10, 0); err == nil {
		t.Fatal("未知 ID 的 UpdateHealth 应报错")
	}
}

func TestUpdateAuthRejectsInvalidStatus(t *testing.T) {
	p := New()
	e := entry("eth0@10.0.0.2", "eth0", "10.0.0.2")
	_ = p.Upsert(e)
	if err := p.UpdateAuth(e.ID, "online-ish", time.Now()); err == nil {
		t.Fatal("非法 auth_status 应报错")
	}
}

func TestUpsertFillsDefaults(t *testing.T) {
	p := New()
	e := entry("eth0@10.0.0.2", "eth0", "10.0.0.2")
	if err := p.Upsert(e); err != nil {
		t.Fatal(err)
	}
	got, _ := p.Get(e.ID)
	if got.AuthStatus != contract.AuthOffline {
		t.Errorf("默认认证状态应为 offline，实际 %q", got.AuthStatus)
	}
	if got.LastSeen.IsZero() {
		t.Error("last_seen 不应为零值")
	}
}

func TestUpsertRejectsBadInput(t *testing.T) {
	p := New()
	cases := map[string]contract.IPPoolEntry{
		"空 ID": {Interface: "eth0", SourceIP: "10.0.0.2"},
		"空网卡":  {ID: "x", SourceIP: "10.0.0.2"},
		"坏 IP": {ID: "x", Interface: "eth0", SourceIP: "10.0.0"},
	}
	for name, e := range cases {
		if err := p.Upsert(e); err == nil {
			t.Errorf("%s：应报错", name)
		}
	}
}

func TestSnapshotIsCopy(t *testing.T) {
	p := New()
	e := entry("eth0@10.0.0.2", "eth0", "10.0.0.2")
	_ = p.Upsert(e)
	_ = p.UpdateHealth(e.ID, 0.5, 10, 0)

	snap := p.Snapshot()
	snap[0].HealthScore = 0.99

	got, _ := p.Get(e.ID)
	if got.HealthScore == 0.99 {
		t.Fatal("快照必须是拷贝，改快照不能影响池内状态")
	}
}

func TestRemove(t *testing.T) {
	p := New()
	e := entry("eth0@10.0.0.2", "eth0", "10.0.0.2")
	_ = p.Upsert(e)
	if !p.Remove(e.ID) {
		t.Fatal("第一次删除应返回 true")
	}
	if p.Remove(e.ID) {
		t.Fatal("重复删除应返回 false")
	}
	if p.Len() != 0 {
		t.Fatal("删除后池应为空")
	}
}

// 认证层与健康检查层会并发写同一个条目，必须靠 -race 兜住。
func TestConcurrentUpdates(t *testing.T) {
	p := New()
	ids := []string{"a@1.1.1.1", "b@2.2.2.2", "c@3.3.3.3"}
	for _, id := range ids {
		_ = p.Upsert(contract.IPPoolEntry{ID: id, Interface: id[:1], SourceIP: "1.1.1.1"})
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			for _, id := range ids {
				_ = p.UpdateAuth(id, contract.AuthOnline, time.Now())
			}
		}()
		go func() {
			defer wg.Done()
			for _, id := range ids {
				_ = p.UpdateHealth(id, 0.7, 30, 0.01)
			}
		}()
		go func() {
			defer wg.Done()
			for range ids {
				_ = p.Snapshot()
				_ = p.Schedulable()
			}
		}()
	}
	wg.Wait()

	for _, id := range ids {
		e, ok := p.Get(id)
		if !ok {
			t.Fatalf("%s 应存在", id)
		}
		if e.AuthStatus != contract.AuthOnline {
			t.Errorf("%s 认证状态被覆盖: %s", id, e.AuthStatus)
		}
		if e.HealthScore != 0.7 {
			t.Errorf("%s 健康分被覆盖: %.2f", id, e.HealthScore)
		}
	}
}
