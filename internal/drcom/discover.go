package drcom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Br1m2y/caulink/internal/portal"
)

// 本文件解决"登录发错门户节点"的问题。
//
// 校园网 ePortal 是按网段/AC 分流的一组节点。把登录提交给错误节点时，门户
// 仍然返回 3.htm（登录成功页），但该源 IP 的流量继续被劫持——表现就是
// "认证一直成功、网却一直不通"，客户端于是每 2 秒重登一次。
//
// 对策两条：
//  1. 登录后必须**探测到在线**才算成功（不再只认 3.htm）；
//  2. 从拦截页里找出本网段真正该用的节点，按源 IP 缓存，下次直接用。

// ErrLoginIneffective 表示登录接口报成功，但该源 IP 仍未获得连通性
// （探测明确看到门户劫持页）。
var ErrLoginIneffective = errors.New("drcom: 登录返回成功，但该源 IP 的流量仍被门户劫持")

// ErrVerifyUnreachable 表示登录后探测目标全部不可达，无法判断是否生效。
//
// 与 ErrLoginIneffective 区分开很重要：前者换门户节点也没用（是探测路径本身
// 的问题），后者才值得去试别的节点。日志也该说清楚是哪一种。
var ErrVerifyUnreachable = errors.New("drcom: 登录后无法验证（探测目标全部不可达）")

// jsRedirectRe 匹配拦截页里的 JS 跳转地址。
//
// 实测的门户劫持页长这样（HTTP 200 + HTML）：
//
//	<script>location.href="http://10.3.38.7"</script>
//
// 而不是 302。所以不能只依赖 Location 头。
var jsRedirectRe = regexp.MustCompile(`(?i)location(?:\.href|\.replace)?\s*\(?\s*=?\s*["'](https?://[^"']+)["']`)

// probeTarget 返回在线判定地址。
func (c Client) probeTarget() string {
	if c.ProbeURL != "" {
		return c.ProbeURL
	}
	return defaultProbeURL
}

// DiscoverPortal 探测一次；若被门户拦截，返回拦截页指向的门户 host:port。
//
// 找不到时返回空串。两种拦截形态都覆盖：
//   - 302/301 被带到别的主机（看最终 URL 的 host）
//   - 200 + HTML 里的 JS 跳转（看正文）
func (c Client) DiscoverPortal(ctx context.Context) string {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	target := c.probeTarget()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", probeUserAgent)

	resp, err := c.client().Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	// 形态一：重定向到别的 host（门户登录页通常就在那个 host 上）。
	if final := resp.Request.URL; final != nil {
		if orig, err := url.Parse(target); err == nil && !strings.EqualFold(final.Hostname(), orig.Hostname()) {
			return hostPortOf(final)
		}
	}

	// 形态二：200 + JS 跳转。
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if m := jsRedirectRe.FindSubmatch(body); len(m) == 2 {
		if u, err := url.Parse(string(m[1])); err == nil && u.Hostname() != "" {
			return hostPortOf(u)
		}
	}
	return ""
}

// hostPortOf 把 URL 转成 "host:port"，缺端口时按 scheme 补默认值。
func hostPortOf(u *url.URL) string {
	if u == nil || u.Hostname() == "" {
		return ""
	}
	if p := u.Port(); p != "" {
		return u.Hostname() + ":" + p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return u.Hostname() + ":443"
	}
	return u.Hostname() + ":80"
}

// probeUserAgent 与探测用的 UA 保持一致，避免个别门户按 UA 区别对待。
const probeUserAgent = "Microsoft NCSI"

// LoginAndVerify 登录并确认真的生效：登录之后必须探测到在线。
//
// 这是本次修复的核心：门户返回 3.htm 只说明"账号密码没错"，不代表这个源 IP
// 被放行。不校验就会把"假成功"写进状态，随后探测又把状态打回离线，形成
// 每 2 秒一次的闪烁与重登。
func (c Client) LoginAndVerify(ctx context.Context) error {
	if err := c.Login(ctx); err != nil {
		return err
	}
	// 登录生效可能有几百毫秒延迟，给 3 次机会。
	var lastProbeErr error
	hijacked := false
	for i := 0; i < 3; i++ {
		online, perr := c.Probe(ctx)
		if online {
			return nil
		}
		if perr != nil {
			lastProbeErr = perr
		} else {
			hijacked = true
			lastProbeErr = nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	if lastProbeErr != nil && !hijacked {
		return fmt.Errorf("%w: %v", ErrVerifyUnreachable, lastProbeErr)
	}
	return ErrLoginIneffective
}

// LoginWithDiscovery 依次尝试候选门户节点，直到"登录 + 验证"真正通过。
//
// 候选顺序：
//  1. 缓存里该源 IP 上次成功过的节点（最可能对，且省一次探测）；
//  2. 配置里写的门户（默认值，单节点校园网就是它）；
//  3. 从拦截页里发现的节点（新网段/换 AC 时的兜底）。
//
// 成功的节点会写回缓存，下次直接命中。
func (c Client) LoginWithDiscovery(ctx context.Context, cache *portal.Cache) error {
	var cands []string
	seen := make(map[string]bool)
	add := func(hp string) {
		hp = strings.TrimSpace(hp)
		if hp == "" || seen[hp] {
			return
		}
		seen[hp] = true
		cands = append(cands, hp)
	}

	if cache != nil {
		add(cache.Get(c.SourceIP))
	}
	if c.Portal != "" {
		add(c.Portal + ":" + itoa(c.port()))
	}
	add(c.DiscoverPortal(ctx))

	var lastErr error
	for _, hp := range cands {
		host, port, err := portal.SplitHostPort(hp, c.port())
		if err != nil || host == "" {
			continue
		}
		attempt := c
		attempt.Portal = host
		attempt.Port = port
		if err := attempt.LoginAndVerify(ctx); err != nil {
			lastErr = err
			// 探测目标全部不可达时，换节点也不可能验证成功，直接返回。
			if errors.Is(err, ErrVerifyUnreachable) {
				return err
			}
			continue
		}
		if cache != nil {
			cache.Set(c.SourceIP, host+":"+itoa(port))
		}
		return nil
	}
	if lastErr == nil {
		lastErr = ErrLoginIneffective
	}
	return lastErr
}

// itoa 是 strconv.Itoa 的小包装，避免为一次格式化引入额外 import。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
