package health

import (
	"context"
	"io"
	"log/slog"
	"math"
	"net"
	"testing"
	"time"

	"github.com/Br1m2y/caulink/internal/ippool"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

func TestScoreBounds(t *testing.T) {
	cases := []struct {
		rtt, loss float64
		want      float64
	}{
		{0, 0, 1.0},           // 完美链路
		{200, 0, 0.5},         // RTT 200ms 权重减半
		{600, 0, 0.25},        // 600ms → 1/(1+3) = 0.25
		{0, 1.0, 0},           // 全丢包
		{100, 0.5, 1.0 / 3.0}, // (1-0.5) * 1/(1+0.5)
	}
	for _, c := range cases {
		got := Score(c.rtt, c.loss)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("Score(%.0f, %.2f) = %.4f，期望 %.4f", c.rtt, c.loss, got, c.want)
		}
	}
}

func TestScoreIsMonotonic(t *testing.T) {
	// 权重必须随 RTT 上升严格递减、随丢包率上升严格递减，
	// 否则调度器会把流量往更差的链路上送。
	prev := 1.1
	for rtt := 0.0; rtt <= 2000; rtt += 50 {
		s := Score(rtt, 0)
		if s > prev {
			t.Fatalf("RTT %.0f 时权重不应上升: %.4f > %.4f", rtt, s, prev)
		}
		prev = s
	}

	prev = 1.1
	for loss := 0.0; loss <= 1.0; loss += 0.1 {
		s := Score(100, loss)
		if s > prev {
			t.Fatalf("丢包 %.1f 时权重不应上升: %.4f > %.4f", loss, s, prev)
		}
		prev = s
	}
}

func TestScoreClampsInputs(t *testing.T) {
	if got := Score(-10, -1); got != 1.0 {
		t.Errorf("负值应夹紧为 0，期望 1.0，实际 %.3f", got)
	}
	if got := Score(100, 3); got != 0 {
		t.Errorf("丢包率 >1 应夹紧为 1，期望 0，实际 %.3f", got)
	}
}

func TestWindowStats(t *testing.T) {
	w := newWindow(4)
	w.push(10, true)
	w.push(20, true)
	w.push(0, false)
	w.push(30, true)

	avg, loss := w.stats()
	if math.Abs(avg-20) > 1e-9 {
		t.Errorf("平均 RTT 应为 20，实际 %.2f", avg)
	}
	if math.Abs(loss-0.25) > 1e-9 {
		t.Errorf("丢包率应为 0.25，实际 %.2f", loss)
	}
}

func TestWindowDropsOldSamples(t *testing.T) {
	w := newWindow(2)
	w.push(100, true)
	w.push(100, true)
	// 窗口满了，再推两个全丢，旧的应该被挤出去
	w.push(0, false)
	w.push(0, false)

	_, loss := w.stats()
	if loss != 1.0 {
		t.Fatalf("窗口内应该全是丢包，实际 %.2f", loss)
	}
}

func TestWindowEmpty(t *testing.T) {
	w := newWindow(3)
	avg, loss := w.stats()
	if avg != 0 || loss != 0 {
		t.Fatalf("空窗口应返回 0,0，实际 %.2f,%.2f", avg, loss)
	}
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestPool(t *testing.T, ip string) *ippool.Pool {
	t.Helper()
	p := ippool.New()
	e := contract.IPPoolEntry{ID: "lo@" + ip, Interface: "lo", SourceIP: ip}
	if err := p.Upsert(e); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateAuth(e.ID, contract.AuthOnline, time.Now()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProbeOnceWritesHealthyScore(t *testing.T) {
	// 起一个本地监听当作探测目标，握手必然成功。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	pool := newTestPool(t, "127.0.0.1")
	p, err := New(pool, Options{
		Target:  ln.Addr().String(),
		Timeout: time.Second,
		Window:  5,
		Logger:  quiet(),
	})
	if err != nil {
		t.Fatal(err)
	}

	p.ProbeOnce(context.Background())

	e, ok := pool.Get("lo@127.0.0.1")
	if !ok {
		t.Fatal("池内应有该条目")
	}
	if e.HealthScore <= 0 {
		t.Fatalf("握手成功时健康分应大于 0，实际 %.3f", e.HealthScore)
	}
	if e.LossRate != 0 {
		t.Fatalf("成功探测不应产生丢包，实际 %.2f", e.LossRate)
	}
	if e.RTTMs < 0 {
		t.Fatal("RTT 不应为负")
	}
}

func TestProbeOnceMarksLossWhenUnreachable(t *testing.T) {
	// 拿一个已关闭的端口当目标。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	pool := newTestPool(t, "127.0.0.1")
	p, err := New(pool, Options{
		Target:  addr,
		Timeout: 300 * time.Millisecond,
		Window:  2,
		Logger:  quiet(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 连续两轮失败后窗口内全是丢包
	p.ProbeOnce(context.Background())
	p.ProbeOnce(context.Background())

	e, _ := pool.Get("lo@127.0.0.1")
	if e.LossRate == 0 {
		t.Fatal("目标不可达时丢包率应大于 0")
	}
	if e.HealthScore != 0 {
		t.Fatalf("全丢包时健康分应为 0，实际 %.3f", e.HealthScore)
	}
}

func TestProbeSkipsOfflineEntries(t *testing.T) {
	pool := ippool.New()
	e := contract.IPPoolEntry{ID: "x@127.0.0.1", Interface: "lo", SourceIP: "127.0.0.1"}
	_ = pool.Upsert(e)
	// 认证不在线，不该被探测

	p, err := New(pool, Options{Target: "127.0.0.1:1", Timeout: 100 * time.Millisecond, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	p.ProbeOnce(context.Background())

	got, _ := pool.Get(e.ID)
	if got.LossRate != 0 || got.HealthScore != 0 {
		t.Fatalf("离线条目不应被写入健康数据: loss=%.2f score=%.2f", got.LossRate, got.HealthScore)
	}
}

func TestNewRejectsNilPool(t *testing.T) {
	if _, err := New(nil, Options{}); err == nil {
		t.Fatal("nil pool 应报错")
	}
}
