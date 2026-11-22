// Package integration_test 验证跨模块链路：IP 池 → 健康检查 → 调度 → 拨号。
//
// 这个测试不需要多网卡、不需要真实的 BitSrunLoginGo，用回环地址就能跑，
// 所以它可以进 CI。真正的多 IP 行为验证属于 Phase 0，不在这里。
package integration_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Br1m2y/caulink/internal/health"
	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/proxy/dialer"
	"github.com/Br1m2y/caulink/internal/scheduler"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

func startEchoServer(t *testing.T) (addr string, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()

	return ln.Addr().String(), func() {
		ln.Close()
		<-done
	}
}

// 完整链路：池里有可用 IP → 健康检查打分 → 调度器选中 → 拨号器从该 IP 建连 → 数据往返。
func TestPoolToDialLink(t *testing.T) {
	addr, closeSrv := startEchoServer(t)
	defer closeSrv()

	pool := ippool.New()
	entry := contract.IPPoolEntry{
		ID:        "lo@127.0.0.1",
		Interface: "lo",
		SourceIP:  "127.0.0.1",
	}
	if err := pool.Upsert(entry); err != nil {
		t.Fatal(err)
	}
	if err := pool.UpdateAuth(entry.ID, contract.AuthOnline, time.Now()); err != nil {
		t.Fatal(err)
	}

	// 健康检查：从该源 IP 握手一次，写入健康分。
	prober, err := health.New(pool, health.Options{
		Target:  addr,
		Timeout: time.Second,
		Window:  3,
	})
	if err != nil {
		t.Fatal(err)
	}
	prober.ProbeOnce(context.Background())

	scored, _ := pool.Get(entry.ID)
	if scored.HealthScore <= 0 {
		t.Fatalf("健康检查后分数应大于 0，实际 %.3f", scored.HealthScore)
	}

	// 调度：此时池里只有一条可用链路，必然选中它。
	sched, err := scheduler.New(pool, scheduler.DefaultRules())
	if err != nil {
		t.Fatal(err)
	}
	picked, err := sched.Pick("tcp", addr)
	if err != nil {
		t.Fatalf("调度失败: %v", err)
	}
	if picked.ID != entry.ID {
		t.Fatalf("应选中 %s，实际 %s", entry.ID, picked.ID)
	}

	// 拨号：用调度结果建连，并确认数据能往返。
	d := dialer.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", addr, picked)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer conn.Close()

	const msg = "caulink"
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	buf := make([]byte, len(msg))
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(buf) != msg {
		t.Fatalf("回显不一致: %q", buf)
	}
}

// 池里没有可调度链路时，调度器必须明确报错，
// 而不是返回一个零值条目让上层拿去拨号（那会拨到 0.0.0.0）。
func TestSchedulerFailsWhenAllLinksDown(t *testing.T) {
	pool := ippool.New()
	e := contract.IPPoolEntry{ID: "a@10.0.0.2", Interface: "eth0", SourceIP: "10.0.0.2"}
	if err := pool.Upsert(e); err != nil {
		t.Fatal(err)
	}
	// 认证失败
	if err := pool.UpdateAuth(e.ID, contract.AuthOffline, time.Now()); err != nil {
		t.Fatal(err)
	}

	sched, err := scheduler.New(pool, scheduler.DefaultRules())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sched.Pick("tcp", "example.com:443"); err != scheduler.ErrNoCandidate {
		t.Fatalf("应返回 ErrNoCandidate，实际 %v", err)
	}
}
