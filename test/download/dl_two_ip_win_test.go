//go:build windows

package download_test

// Windows 双网卡同网段时的出口问题与验证。
//
// 现象：两块网卡都在 10.6.0.0/17、默认网关都是 10.6.0.1 时，只 bind 源 IP 不够 ——
// 内核按目的地址选路，选中 metric 更低的那块网卡，于是源 IP 属于 A 网卡的包从 B 网卡发出，
// 被接入交换机按「源 IP/MAC 不匹配」丢掉。表现为：时通时不通，最终全部超时。
//
// 修法：Windows 的 IP_UNICAST_IF（IPPROTO_IP, 31）按 socket 强制出口网卡。
// 注意它要求接口索引是网络字节序，这一点容易写错。

import (
	"context"
	"crypto/md5"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"testing"
	"time"

	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/metrics"
	"github.com/Br1m2y/caulink/internal/proxy/httpproxy"
	"github.com/Br1m2y/caulink/internal/scheduler"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

const ipUnicastIFOpt = 31 // Windows SDK: IP_UNICAST_IF

// clientNoKeepAlive 强制每个请求一条连接。
// 不这样做的话，HTTP/1.1 keep-alive 会把 8 个 Range 请求复用到一条隧道上，
// 测出来的是"单连接串行"，不是并行下载。
func clientNoKeepAlive(proxyAddr string) *http.Client {
	tr := &http.Transport{
		Proxy:              func(*http.Request) (*url.URL, error) { return url.Parse("http://" + proxyAddr) },
		ForceAttemptHTTP2:  false,
		DisableCompression: true,
		DisableKeepAlives:  true,
	}
	return &http.Client{Transport: tr, Timeout: 3 * time.Minute}
}

func htonl(v uint32) uint32 {
	return v<<24 | (v&0x0000ff00)<<8 | (v&0x00ff0000)>>8 | v>>24
}

// unicastDialer 在 bind 源 IP 之外，再用 IP_UNICAST_IF 锁定出口网卡。
type unicastDialer struct {
	timeout time.Duration
	index   map[string]int
}

func (d unicastDialer) DialContext(ctx context.Context, network, address string, e contract.IPPoolEntry) (net.Conn, error) {
	idx, ok := d.index[e.Interface]
	if !ok {
		return nil, &net.OpError{Op: "dial", Net: network, Err: errUnknownIface(e.Interface)}
	}

	nd := &net.Dialer{
		Timeout:   d.timeout,
		LocalAddr: &net.TCPAddr{IP: net.ParseIP(e.SourceIP)},
		Control: func(network, address string, c syscall.RawConn) error {
			if network != "tcp4" {
				return nil
			}
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IP, ipUnicastIFOpt, int(htonl(uint32(idx))))
			}); err != nil {
				return err
			}
			return serr
		},
	}
	return nd.DialContext(ctx, network, address)
}

type errUnknownIface string

func (e errUnknownIface) Error() string { return "未知网卡: " + string(e) }

// setupMultiDial 与 setupMulti 相同，只是允许注入自定义拨号器。
func setupMultiDial(t *testing.T, srcs []srcSpec, d httpproxy.Dialer) (*recordingPicker, string, *metrics.Registry, func()) {
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
			t.Fatalf("健康分 %s: %v", e.ID, err)
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
		Dialer:     d,
		Metrics:    reg,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx, ln) }()

	return rp, ln.Addr().String(), reg, func() { cancel(); _ = ln.Close() }
}

func TestTwoIPUnicast(t *testing.T) {
	a := srcSpec{envOr("CAU_IFACE_A", "WLAN"), envOr("CAU_SRC_A", "10.6.93.195")}
	b := srcSpec{envOr("CAU_IFACE_B", "WLAN 2"), envOr("CAU_SRC_B", "10.6.54.214")}

	if a.ip == b.ip {
		t.Skip("两个源 IP 相同，跳过")
	}

	index := map[string]int{}
	for _, s := range []srcSpec{a, b} {
		ifi, err := net.InterfaceByName(s.iface)
		if err != nil {
			t.Fatalf("找不到网卡 %q: %v", s.iface, err)
		}
		index[s.iface] = ifi.Index
		t.Logf("%s → ifIndex %d", s.iface, ifi.Index)
	}
	d := unicastDialer{timeout: 10 * time.Second, index: index}

	const total = int64(20 << 20)
	const seg = int64(2560 << 10)

	ref, err := directGet(0, total-1)
	if err != nil {
		t.Fatalf("参照下载失败: %v", err)
	}
	refSum := md5sum(ref)
	t.Logf("参照 20MiB md5 = %x", refSum)

	// 逐个测，确认每块网卡单独都能跑满。
	speeds := map[string]float64{}
	for _, s := range []srcSpec{a, b} {
		_, addr, _, cleanup := setupMultiDial(t, []srcSpec{s}, d)
		cl := clientNoKeepAlive(addr)
		body, dur := measure(t, cl, 1, total)
		sp := float64(len(body)) / dur.Seconds() / 1e6
		if md5sum(body) != refSum {
			cleanup()
			t.Fatalf("源 %s 内容与参照不一致", s.ip)
		}
		speeds[s.ip] = sp
		t.Logf("单独 %-14s %.2f MB/s", s.ip, sp)
		cleanup()
	}

	// 两条一起，8 条连接。
	rp, addr, reg, cleanup := setupMultiDial(t, []srcSpec{a, b}, d)
	defer cleanup()
	cl := clientNoKeepAlive(addr)

	body, dur := measure(t, cl, 8, seg)
	agg := float64(len(body)) / dur.Seconds() / 1e6
	if md5sum(body) != refSum {
		t.Fatalf("双源拼接内容与参照不一致")
	}

	t.Logf("双源 8 连接：%.2fs，聚合 %.2f MB/s", dur.Seconds(), agg)
	for _, sm := range reg.Snapshot() {
		t.Logf("   %-26s 下行 %6.2f MiB", sm.ID, float64(sm.BytesDown)/(1<<20))
	}
	t.Logf("调度分配：%v", rp.distribution())

	sumSolo := speeds[a.ip] + speeds[b.ip]
	maxSolo := speeds[a.ip]
	if speeds[b.ip] > maxSolo {
		maxSolo = speeds[b.ip]
	}
	t.Logf("单源合计 %.2f MB/s，最大单源 %.2f MB/s", sumSolo, maxSolo)
	t.Logf("聚合/合计 = %.2f，聚合/最大 = %.2f", agg/sumSolo, agg/maxSolo)

	switch {
	case agg/sumSolo >= 0.80:
		t.Log("判定：叠加成立")
	case agg/maxSolo <= 1.25:
		t.Log("判定：未叠加")
	default:
		t.Log("判定：部分叠加，建议重测")
	}
}

func md5sum(b []byte) [16]byte { return md5.Sum(b) }
