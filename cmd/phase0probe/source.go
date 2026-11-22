package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Br1m2y/caulink/internal/netsrc"
)

// Source 是一个可用于发起连接的 (网卡, 源 IP) 组合。
type Source struct {
	Iface string
	IP    net.IP
}

func (s Source) String() string { return s.IP.String() + " (" + s.Iface + ")" }

// listSources 枚举本机所有可用的 IPv4 源地址。
//
// 一个网卡上可能有多个 IPv4（IP 别名），每个都算独立的候选源地址 ——
// Phase 0 要的正是"不同的源 IP"，而不是"不同的网卡"。
func listSources(cfg Config) ([]Source, error) {
	include, err := regexp.Compile(cfg.IncludeInterfaces)
	if err != nil {
		return nil, fmt.Errorf("include_interfaces 正则非法: %w", err)
	}
	exclude, err := regexp.Compile(cfg.ExcludeInterfaces)
	if err != nil {
		return nil, fmt.Errorf("exclude_interfaces 正则非法: %w", err)
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("枚举网卡: %w", err)
	}

	var out []Source
	seen := map[string]bool{}

	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if cfg.IncludeInterfaces != "" && !include.MatchString(ifi.Name) {
			continue
		}
		if exclude.MatchString(ifi.Name) {
			continue
		}

		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			// 链路本地地址（169.254.x.x）是自动分配的空地址，不能用来认证，也没有出口。
			if ip4.IsLinkLocalUnicast() || ip4.IsUnspecified() || ip4.IsMulticast() {
				continue
			}
			key := ip4.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Source{Iface: ifi.Name, IP: ip4})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Iface != out[j].Iface {
			return out[i].Iface < out[j].Iface
		}
		return out[i].IP.String() < out[j].IP.String()
	})

	if len(out) == 0 {
		return nil, fmt.Errorf("没有找到可用的 IPv4 源地址；检查网卡是否已连接且已配置 IP")
	}
	return out, nil
}

// 预检结果分类。区分这几种情况很重要：
// "未认证"和"链路不通"在测速数字上都表现为 0，但处置方式完全不同。
const (
	PreOK         = "ok"              // 能正常访问目标，已认证
	PreUnauth     = "unauthenticated" // 被重定向到认证页，或返回了非预期内容
	PreNetFail    = "network_fail"    // 连不上，链路/路由问题
	PreTargetFail = "target_fail"     // 能连目标但目标返回错误
)

// PreflightResult 记录单个源 IP 的连通性预检结果。
type PreflightResult struct {
	Source  Source
	Verdict string
	Status  int
	Note    string
}

// userAgent 用工具自己的名字，不要伪装浏览器。
//
// 2026-11 实测（清华 TUNA / 中科大 / Cloudflare）：
//   - 伪装 Chrome 的 UA → 镜像站返回 403（防盗链识别"冒充浏览器但 header 不全"）
//   - 完全不发 UA        → 镜像站返回 403
//   - Go 默认 UA         → 镜像站返回 403
//   - 工具自己的 UA      → 全部 200/206
//
// 所以"诚实"在这里不只是道德问题，是能不能拿到数据的问题。
const userAgent = "caulink-phase0probe/" + toolVersion

// preflight 从指定源 IP 向测速目标发一个极小的请求，确认三件事：
// 1) 这个源地址能不能真的建立连接（绑定是否生效）
// 2) 是否被校园网门户劫持（未认证的 IP 测速一定是 0，必须先排除）
// 3) 目标是不是可达
//
// 只读 1 个字节就让连接关闭，不污染后续的吞吐测量。
func preflight(ctx context.Context, src Source, url string) PreflightResult {
	res := PreflightResult{Source: src}

	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := &net.Dialer{
				Timeout:   8 * time.Second,
				LocalAddr: &net.TCPAddr{IP: src.IP},
				Control:   netsrc.DialControl(src.IP),
			}
			return d.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2: false,
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()

	// 允许跟随重定向：镜像站、CDN 正常跳转很常见（比如 CentOS 镜像 EOL 后 302 到归档路径），
	// 把所有 3xx 都当成"未认证"会大面积误判。
	client := &http.Client{Transport: tr, Timeout: 20 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		res.Verdict = PreNetFail
		res.Note = err.Error()
		return res
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		res.Verdict = PreNetFail
		res.Note = "无法从该源 IP 建立连接：" + err.Error()
		return res
	}
	defer resp.Body.Close()

	res.Status = resp.StatusCode

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		res.Verdict = PreTargetFail
		res.Note = fmt.Sprintf("目标返回 HTTP %d", resp.StatusCode)
		return res
	}

	var first [1]byte
	n, _ := io.ReadFull(resp.Body, first[:])
	if n == 0 {
		res.Verdict = PreTargetFail
		res.Note = "状态码正常但没有数据返回，测速目标可能不可用"
		return res
	}

	if isPortalHijack(req.URL, resp, first[:]) {
		res.Verdict = PreUnauth
		res.Note = "最终落到 " + resp.Request.URL.String() + " 并返回 HTML 页面，疑似校园网认证页劫持"
		return res
	}

	res.Verdict = PreOK
	res.Note = "可正常访问"
	return res
}

// isPortalHijack 判断"拿到的不是想要的数据，而是认证页"。
//
// 三个条件同时成立才算劫持，宁可放过也不误杀 —— 误判成"未认证"会让用户
// 反复折腾账号，而真正的问题其实只是目标 URL 失效了。
//  1. 请求被重定向到了另一个主机（真正的大文件下载通常不会跨站跳转）
//  2. 响应是 HTML
//  3. body 以 '<' 开头（是标记语言，不是二进制数据）
func isPortalHijack(origURL *url.URL, resp *http.Response, first []byte) bool {
	final := resp.Request.URL
	if final.Hostname() == origURL.Hostname() {
		return false
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "html") {
		return false
	}
	return len(first) > 0 && first[0] == '<'
}

// usableSources 只保留预检通过的源 IP。未认证的 IP 参与测速只会得出"0 MB/s"，
// 让整个结论失去意义。
func usableSources(ctx context.Context, cfg Config, url string) ([]Source, []PreflightResult, error) {
	all, err := listSources(cfg)
	if err != nil {
		return nil, nil, err
	}

	var results []PreflightResult
	var usable []Source
	for _, s := range all {
		r := preflight(ctx, s, url)
		results = append(results, r)
		if r.Verdict == PreOK {
			usable = append(usable, s)
		}
	}
	return usable, results, nil
}
