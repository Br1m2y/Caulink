// Package drcom 实现中国农业大学 Dr.COM ePortal 的登录、注销与在线判定。
//
// 协议是在真实门户上抓出来的（详见 docs/adr/0004-drcom-auth.md）：
//
//	登录  POST http://<portal>:801/eportal/?c=ACSetting&a=Login
//	      表单 DDDDD=<学号>@<realm>&upass=<密码>&R1=0&R2=&R3=0&R6=0&para=00&0MKKey=123456
//	      成功 → 302 到 .../3.htm；失败 → 302 到 .../2.htm 且带 RetCode
//	注销  GET  .../eportal/?c=ACSetting&a=Logout&wlanuserip=<IP>
//
// 两条硬约束：
//
//  1. 账号必须带 realm 后缀，否则 RADIUS 报 "no AT symbol in realm"；
//  2. 请求必须从目标源 IP 发出（net.Dialer.LocalAddr），否则登录的是别的设备；
//     Windows 双网卡同网段时还要钉住出口网卡（netsrc.DialControl），否则包会从
//     另一块网卡发出，登录请求直接超时。
//
// 本包被两处使用：认证子进程（cmd/caulink-auth）做稳态守护，
// 守护进程（caulinkd）在代理层观察到上游失败时做即时重认证。
package drcom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Br1m2y/caulink/internal/netsrc"
)

// DefaultPort 是农大 ePortal 的实际端口。
const DefaultPort = 801

// defaultProbeURL 是在线判定地址：200 且内容匹配才算在线。
const defaultProbeURL = "http://connect.rom.miui.com/generate_204"

// probeExpect 是 connecttest.txt 的固定内容片段，用来识破"200 但是劫持页"。
const probeExpect = "Microsoft Connect Test"

// Client 是绑定到某个源 IP 的 Dr.COM 客户端。
type Client struct {
	// Portal 是门户地址，如 10.3.191.9。
	Portal string
	// Port 是 ePortal 端口，0 表示用 DefaultPort。
	Port int
	// Username 是学号，**不带** realm 后缀。
	Username string
	// Realm 是认证域后缀，登录时拼成 <username>@<realm>。
	Realm    string
	Password string
	// SourceIP 是发请求时绑定的源地址。
	SourceIP string
	// Timeout 是单次请求超时，0 表示 15s。
	Timeout time.Duration
	// ProbeURL 可覆盖在线判定地址（测试用）。
	ProbeURL string
	// HTTPClient 仅供测试注入；零值由本包构造绑定 SourceIP 的客户端。
	HTTPClient *http.Client
}

// ErrRejected 表示门户明确拒绝了登录（账号密码或参数问题）。
var ErrRejected = errors.New("drcom: 门户拒绝登录")

func (c Client) port() int {
	if c.Port > 0 {
		return c.Port
	}
	return DefaultPort
}

func (c Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 15 * time.Second
}

// Account 返回要提交给门户的账号形式（带 realm）。
func (c Client) Account() string {
	if c.Realm == "" {
		return c.Username
	}
	return c.Username + "@" + c.Realm
}

func (c Client) baseURL() string {
	return fmt.Sprintf("http://%s:%d", c.Portal, c.port())
}

// client 返回绑定到 SourceIP、且**不跟随重定向**的 HTTP 客户端。
//
// 不跟随重定向是刻意的：Dr.COM 用 Location 里的 3.htm / 2.htm 表达结果，
// 跟随之后既拿不到判断依据，又会把"离线"误判成登录页的 200。
// 这条规则是协议的一部分，所以对注入的 HTTPClient 同样生效。
func (c Client) client() *http.Client {
	var base *http.Client
	if c.HTTPClient != nil {
		base = c.HTTPClient
	} else {
		ip := net.ParseIP(c.SourceIP)
		tr := &http.Transport{
			DisableKeepAlives: true,
			Proxy:             nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				d := net.Dialer{
					Timeout: 10 * time.Second,
					// 与 LocalAddr 配套：LocalAddr 决定源 IP，Control 决定出口网卡。
					// 少了后者，Windows 上第二个 IP 的登录会一路超时。
					Control: netsrc.DialControl(ip),
				}
				if ip != nil {
					d.LocalAddr = &net.TCPAddr{IP: ip}
				}
				return d.DialContext(ctx, network, addr)
			},
		}
		base = &http.Client{Transport: tr}
	}

	clone := *base
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

// Login 执行一次登录。成功返回 nil，门户拒绝返回 ErrRejected 包装的错误。
func (c Client) Login(ctx context.Context) error {
	endpoint := c.baseURL() + "/eportal/?c=ACSetting&a=Login"
	form := url.Values{
		"DDDDD":  {c.Account()},
		"upass":  {c.Password},
		"R1":     {"0"},
		"R2":     {""},
		"R3":     {"0"},
		"R6":     {"0"},
		"para":   {"00"},
		"0MKKey": {"123456"},
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("drcom: 登录请求失败: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	loc := resp.Header.Get("Location")
	switch {
	case strings.Contains(loc, "3.htm"):
		return nil
	case strings.Contains(loc, "2.htm"):
		return fmt.Errorf("%w: %s", ErrRejected, DecodeReason(loc))
	default:
		return fmt.Errorf("drcom: 未识别的登录响应 HTTP %d Location=%q", resp.StatusCode, loc)
	}
}

// Logout 注销 SourceIP 对应的会话。
func (c Client) Logout(ctx context.Context) error {
	endpoint := c.baseURL() + "/eportal/?c=ACSetting&a=Logout&wlanuserip=" +
		url.QueryEscape(c.SourceIP)

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("drcom: 注销请求失败: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "2.htm") {
		return fmt.Errorf("drcom: 注销响应异常 HTTP %d Location=%q", resp.StatusCode, loc)
	}
	return nil
}

// Probe 判断该源 IP 当前是否真的能上网。
//
// 三个要点（都踩过）：
//   - 不跟随重定向：未认证时 portal 会 302 到登录页，跟随会拿到登录页的 200；
//   - 校验响应内容：避免"200 但其实是劫持页"的假在线；
//   - 失败一律视为离线，宁可多登一次也不要漏判。
//
// probeState 是一次探测的三种结论。
//
// 区分"劫持"和"不可达"至关重要：
//   - 劫持：门户把我们拦下来了，需要重新认证；
//   - 不可达：探测目标本身从这个源 IP 走不通（实测 connect.rom.miui.com
//     会对某些源 IP 整段超时，而该 IP 的 HTTPS 是通的）。
//
// 过去两者混为一谈，于是"探测目标挂了"被当成"掉线"，触发无限重认证。
type probeState int

const (
	probeUnreachable probeState = iota
	probeOnline
	probeHijacked
)

// probeTargets 是探测候选地址，按顺序尝试，任一给出确定结论即停止。
//
// 为什么是多个：单一探测目标迟早会从某条路径不可达（实测过小米节点对某个
// 源 IP 完全超时）。多目标 + 明确的"不可达"语义，才能把"误判掉线"挡在门外。
var probeTargets = []string{
	"http://connect.rom.miui.com/generate_204",
	"http://www.msftconnecttest.com/connecttest.txt",
	"http://connectivitycheck.platform.hicloud.com/generate_204",
}

// Probe 判断该源 IP 当前是否在线。
//
// 返回 (false, nil) 表示**确定离线**（被门户劫持）；返回 (false, err) 表示
// 所有探测目标都不可达——这是"未知"，调用方不应据此重新登录。
func (c Client) Probe(ctx context.Context) (bool, error) {
	targets := probeTargets
	if c.ProbeURL != "" {
		targets = []string{c.ProbeURL}
	}

	var errs []string
	for _, raw := range targets {
		state, err := c.probeOne(ctx, raw)
		switch state {
		case probeOnline:
			return true, nil
		case probeHijacked:
			return false, nil
		default:
			if err != nil {
				errs = append(errs, raw+": "+err.Error())
			}
		}
	}
	return false, fmt.Errorf("drcom: 探测目标全部不可达: %s", strings.Join(errs, "; "))
}

// probeOne 探测单个目标。
func (c Client) probeOne(ctx context.Context, rawURL string) (probeState, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return probeUnreachable, err
	}
	if probeUserAgent != "" {
		req.Header.Set("User-Agent", probeUserAgent)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return probeUnreachable, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	// 落到别的主机 = 被门户重定向。
	if final := resp.Request.URL; final != nil {
		if orig, perr := url.Parse(rawURL); perr == nil && !strings.EqualFold(final.Hostname(), orig.Hostname()) {
			return probeHijacked, nil
		}
	}

	// 204 No Content 是"生成式探测"端点的标准回答，既无响应体，也不会被
	// 劫持页替换成 HTML。
	if resp.StatusCode == http.StatusNoContent {
		return probeOnline, nil
	}
	// 3xx：门户劫持的典型形态（跳登录页）。判"确定离线"而不是"不可达"，
	// 否则会被当成未知而跳过重认证。
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return probeHijacked, nil
	}
	if resp.StatusCode != http.StatusOK {
		return probeUnreachable, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// 200 + HTML：门户劫持页通常就是这样返回的。
	ctype := strings.ToLower(resp.Header.Get("Content-Type"))
	text := string(body)
	if probeExpect != "" && strings.Contains(text, probeExpect) {
		return probeOnline, nil
	}
	if strings.Contains(ctype, "html") || strings.Contains(strings.ToLower(text), "<html") {
		return probeHijacked, nil
	}
	// 其余 200（纯文本等）视为在线。
	return probeOnline, nil
}

// DecodeReason 从 Location 里抽出可读的错误信息。
func DecodeReason(loc string) string {
	u, err := url.Parse(loc)
	if err != nil {
		return loc
	}
	if msg := u.Query().Get("RetCode"); msg != "" {
		return msg
	}
	if msg := u.Query().Get("ErrorMsg"); msg != "" {
		if dec, err := url.QueryUnescape(msg); err == nil && dec != "" {
			return dec
		}
	}
	return loc
}
