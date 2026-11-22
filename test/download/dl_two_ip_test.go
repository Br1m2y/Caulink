package download_test

// 双源 IP 叠加测试。
//
// 与 dl_test.go 的区别：那个用单个 IP 验证机制，这个验证「两份独立额度能否叠加」。
// 需要两个已认证、能独立出网的源 IP；缺一个就跳过，不会给出误导性的结论。
//
// 用法：
//   CAU_IFACE_A=WLAN    CAU_SRC_A=10.6.93.195 \
//   CAU_IFACE_B=WLAN 2  CAU_SRC_B=10.6.94.77 \
//   go test ./test/download/ -run TestTwoIP -v -timeout 900s

import (
	"context"
	"crypto/md5"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/metrics"
	"github.com/Br1m2y/caulink/internal/proxy/dialer"
	"github.com/Br1m2y/caulink/internal/proxy/httpproxy"
	"github.com/Br1m2y/caulink/internal/scheduler"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

type srcSpec struct {
	iface string
	ip    string
}

// setupMulti 用给定的源地址列表建池，起一个 HTTP 代理，返回其地址与统计注册表。
func setupMulti(t *testing.T, srcs []srcSpec) (*recordingPicker, string, *metrics.Registry, func()) {
	t.Helper()

	pool := ippool.New()
	for _, s := range srcs {
		e := contract.IPPoolEntry{
			ID:         s.iface + "@" + s.ip,
			Interface:  s.iface,
			SourceIP:   s.ip,
			AuthStatus: contract.AuthOnline,
		}
		if err := pool.Upsert(e); err != nil {
			t.Fatalf("加入 %s: %v", e.ID, err)
		}
		if err := pool.UpdateHealth(e.ID, 1.0, 8, 0); err != nil {
			t.Fatalf("设置健康分 %s: %v", e.ID, err)
		}
	}

	sched, err := scheduler.New(pool, scheduler.DefaultRules())
	if err != nil {
		t.Fatalf("调度器: %v", err)
	}
	rp := &recordingPicker{inner: sched}
	reg := metrics.New()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	srv := &httpproxy.Server{
		ListenAddr: ln.Addr().String(),
		Picker:     rp,
		Dialer:     dialer.Dialer{Timeout: 10 * time.Second},
		Metrics:    reg,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx, ln) }()

	return rp, ln.Addr().String(), reg, func() { cancel(); _ = ln.Close() }
}

// measure 用 n 条并发连接各取 segBytes，返回拼接结果与墙钟。
func measure(t *testing.T, cl *http.Client, n int, segBytes int64) ([]byte, time.Duration) {
	t.Helper()

	parts := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup

	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := int64(i) * segBytes
			parts[i], errs[i] = fetchRange(cl, s, s+segBytes-1)
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	var all []byte
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("第 %d 片失败: %v", i+1, errs[i])
		}
		if int64(len(parts[i])) != segBytes {
			t.Fatalf("第 %d 片长度 %d，期望 %d", i+1, len(parts[i]), segBytes)
		}
		all = append(all, parts[i]...)
	}
	return all, elapsed
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func TestTwoIP(t *testing.T) {
	a := srcSpec{envOr("CAU_IFACE_A", "WLAN"), os.Getenv("CAU_SRC_A")}
	b := srcSpec{envOr("CAU_IFACE_B", "WLAN-USB"), os.Getenv("CAU_SRC_B")}

	if a.ip == "" || b.ip == "" {
		t.Skip("未设置 CAU_SRC_A / CAU_SRC_B，跳过双源测试")
	}
	if a.ip == b.ip {
		t.Fatalf("两个源 IP 相同（%s），叠加测试没有意义", a.ip)
	}

	const total = int64(20 << 20)
	const seg = int64(2560 << 10) // 8 片 × 2.5MiB

	t.Logf("源 A = %s (%s)，源 B = %s (%s)", a.ip, a.iface, b.ip, b.iface)

	// 参照内容：直连取整段。
	ref, err := directGet(0, total-1)
	if err != nil {
		t.Fatalf("参照下载失败: %v", err)
	}
	refSum := md5.Sum(ref)
	t.Logf("参照 20MiB md5 = %x", refSum)

	// 逐个 IP 单独测，得到各自额度。
	type solo struct {
		spec  srcSpec
		speed float64
		check [16]byte
	}
	var solos []solo
	for _, s := range []srcSpec{a, b} {
		_, addr, _, cleanup := setupMulti(t, []srcSpec{s})
		cl := clientVia("http://"+addr, nil)

		body, dur := measure(t, cl, 1, total)
		sp := float64(len(body)) / dur.Seconds() / 1e6
		sum := md5.Sum(body)
		t.Logf("单独 %-16s %.2f MB/s，md5 %s", s.ip, sp, short(sum))
		if sum != refSum {
			cleanup()
			t.Fatalf("源 %s 单流内容与参照不一致", s.ip)
		}
		solos = append(solos, solo{s, sp, sum})
		cleanup()
	}

	// 两个 IP 一起：8 条连接，SWRR 应各分 4 条。
	rp, addr, reg, cleanup := setupMulti(t, []srcSpec{a, b})
	defer cleanup()
	cl := clientVia("http://"+addr, nil)

	body, dur := measure(t, cl, 8, seg)
	agg := float64(len(body)) / dur.Seconds() / 1e6
	sum := md5.Sum(body)
	t.Logf("双源 8 连接 %.2fs，聚合 %.2f MB/s，md5 %s", dur.Seconds(), agg, short(sum))
	if sum != refSum {
		t.Fatalf("双源并行拼接内容与参照不一致")
	}

	// 每个 IP 实际扛了多少字节 —— 这是"两条线都在干活"的直接证据。
	t.Log("各源 IP 承载：")
	for _, sm := range reg.Snapshot() {
		t.Logf("   %-24s 下行 %6.2f MiB", sm.ID, float64(sm.BytesDown)/(1<<20))
	}
	t.Logf("调度分配：%v", rp.distribution())

	// 判定
	var sumSolo, maxSolo float64
	for _, s := range solos {
		sumSolo += s.speed
		if s.speed > maxSolo {
			maxSolo = s.speed
		}
	}
	ratioSum := agg / sumSolo
	ratioMax := agg / maxSolo

	t.Logf("单源合计 %.2f MB/s，最大单源 %.2f MB/s", sumSolo, maxSolo)
	t.Logf("聚合/单源合计 = %.2f，聚合/最大单源 = %.2f", ratioSum, ratioMax)

	switch {
	case ratioSum >= 0.80:
		t.Log("判定：**叠加成立** —— 两个 IP 的额度可以相加，走聚合主线")
	case ratioMax <= 1.25:
		t.Log("判定：**未叠加** —— 总速率没有超过单条链路，需要查路由是否真的从两块网卡出去")
	default:
		t.Log("判定：部分叠加，介于两者之间，建议重测 2–3 次取中位数")
	}
}

func short(s [16]byte) string { return fmt.Sprintf("%x", s[:6]) }
