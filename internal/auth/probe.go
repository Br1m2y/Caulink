package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Br1m2y/caulink/internal/netsrc"
)

// ProbeKind 是单次探测的结论类型。
//
// 必须区分"未认证"和"链路不通"：前者需要重认证，后者重认证也没用，
// 混为一谈会导致网络抖动时疯狂重登，把自己的账号搞进风控。
type ProbeKind int

const (
	// ProbeOnline 表示从该源 IP 能拿到预期内容，认证有效。
	ProbeOnline ProbeKind = iota
	// ProbeUnauthenticated 表示请求被门户劫持，或拿到了认证页。
	ProbeUnauthenticated
	// ProbeNetworkError 表示连接失败、超时或目标异常，认证状态未知。
	ProbeNetworkError
)

func (k ProbeKind) String() string {
	switch k {
	case ProbeOnline:
		return "online"
	case ProbeUnauthenticated:
		return "unauthenticated"
	default:
		return "network_error"
	}
}

// ProbeResult 是一次探测的完整结果。
type ProbeResult struct {
	Kind   ProbeKind
	Status int
	Reason string
	RTT    time.Duration
	At     time.Time
}

// probeUserAgent 用工具自己的名字。
// 伪装浏览器会被镜像站/门户的防盗链拦成 403，反而拿不到真实结果。
const probeUserAgent = "caulink-auth-probe/0.1.0"

// probeOnce 从指定源 IP 发起一次极轻量的在线探测。
//
// 绑定 LocalAddr 是关键：不绑的话，无论哪个实例探测，流量都从默认路由的网卡出去，
// 所有 IP 得到同一个答案，认证状态就完全失真了。
//
// Control 是同一件事的另一半：Windows 双网卡同网段时，只绑源 IP 的包仍可能从
// 另一块网卡出去（按目的地址选路），探测结果是"链路不通"，而链路其实是好的。
func probeOnce(parent context.Context, srcIP net.IP, rawURL string, timeout time.Duration) ProbeResult {
	res := ProbeResult{At: time.Now()}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := &net.Dialer{
				Timeout:   timeout,
				LocalAddr: &net.TCPAddr{IP: srcIP},
				Control:   netsrc.DialControl(srcIP),
			}
			return d.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2: false,
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()

	client := &http.Client{Transport: tr, Timeout: timeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		res.Kind = ProbeNetworkError
		res.Reason = "构造请求失败: " + err.Error()
		return res
	}
	req.Header.Set("User-Agent", probeUserAgent)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		res.Kind = ProbeNetworkError
		res.Reason = "连接失败: " + err.Error()
		return res
	}
	defer resp.Body.Close()

	res.RTT = time.Since(start)
	res.Status = resp.StatusCode

	// 落到别的主机：典型的口径是校园网门户把任意 http 请求劫持到认证页。
	if resp.Request.URL.Hostname() != req.URL.Hostname() {
		res.Kind = ProbeUnauthenticated
		res.Reason = "请求被重定向到 " + resp.Request.URL.Host
		return res
	}

	// 返回 HTML：认证页通常以 200 返回一整个登录页，而不是 3xx。
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "html") {
		res.Kind = ProbeUnauthenticated
		res.Reason = "返回 HTML 页面，疑似认证页"
		return res
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		res.Kind = ProbeOnline
		res.Reason = "在线"
		return res
	}

	res.Kind = ProbeNetworkError
	res.Reason = fmt.Sprintf("探测目标返回 HTTP %d", resp.StatusCode)
	return res
}
