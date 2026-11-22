//go:build windows

package download_test

// 诊断：双源并行时，每次上游拨号究竟走了哪个条目、成功与否、搬了多少字节。

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

type dialRecord struct {
	entry string
	local string
	err   string
	ms    int64
	read  int64
	write int64
}

// countingConn 记录单条连接搬运的字节，用来区分「两条链路各扛一半」和「一条扛全部」。
type countingConn struct {
	net.Conn
	r, w *int64
}

func (c countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	atomicAdd(c.r, int64(n))
	return n, err
}

func (c countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	atomicAdd(c.w, int64(n))
	return n, err
}

type recDialer struct {
	inner unicastDialer
	mu    sync.Mutex
	recs  []*dialRecord
}

func (r *recDialer) DialContext(ctx context.Context, network, address string, e contract.IPPoolEntry) (net.Conn, error) {
	t0 := time.Now()
	c, err := r.inner.DialContext(ctx, network, address, e)
	rec := &dialRecord{entry: e.ID, ms: time.Since(t0).Milliseconds()}
	if c != nil {
		rec.local = c.LocalAddr().String()
	}
	if err != nil {
		rec.err = err.Error()
	}
	r.mu.Lock()
	r.recs = append(r.recs, rec)
	r.mu.Unlock()
	if c != nil {
		return countingConn{Conn: c, r: &rec.read, w: &rec.write}, nil
	}
	return c, err
}

func (r *recDialer) dump() []*dialRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*dialRecord, len(r.recs))
	copy(out, r.recs)
	return out
}

func atomicAdd(p *int64, n int64) { atomic.AddInt64(p, n) }

func TestDiagTwoIP(t *testing.T) {
	a := srcSpec{"WLAN", "10.6.93.195"}
	b := srcSpec{"WLAN 2", "10.6.54.214"}

	index := map[string]int{}
	for _, s := range []srcSpec{a, b} {
		ifi, err := net.InterfaceByName(s.iface)
		if err != nil {
			t.Fatalf("%s: %v", s.iface, err)
		}
		index[s.iface] = ifi.Index
	}
	d := &recDialer{inner: unicastDialer{timeout: 10 * time.Second, index: index}}

	const seg = int64(2560 << 10)
	_, addr, reg, cleanup := setupMultiDial(t, []srcSpec{a, b}, d)
	defer cleanup()

	// 关键：禁用 keep-alive。否则 8 个 Range 请求会复用到同一条隧道上，
	// 测出来的是"单连接串行"，不是并行。
	tr := &http.Transport{
		Proxy:              func(*http.Request) (*url.URL, error) { return url.Parse("http://" + addr) },
		ForceAttemptHTTP2:  false,
		DisableCompression: true,
		DisableKeepAlives:  true,
	}
	cl := &http.Client{Transport: tr, Timeout: 3 * time.Minute}
	body, dur := measure(t, cl, 8, seg)
	t.Logf("8 连接：%d 字节，%.2fs", len(body), dur.Seconds())

	t.Log("== 每次拨号 ==")
	for i, r := range d.dump() {
		line := fmt.Sprintf("%2d) %-26s local=%-18s %4dms  收 %6.2f MiB 发 %5.2f MiB", i+1, r.entry, r.local, r.ms,
			float64(r.read)/(1<<20), float64(r.write)/(1<<20))
		if r.err != "" {
			line += "  ERR: " + r.err
		}
		t.Log(line)
	}

	t.Log("== 各条目字节 ==")
	for _, sm := range reg.Snapshot() {
		t.Logf("   %-26s 下行 %6.2f MiB 连接 %d", sm.ID, float64(sm.BytesDown)/(1<<20), sm.TotalConns)
	}
}
