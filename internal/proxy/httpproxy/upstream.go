package httpproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// Upstream 是"部分流量交给另一个代理"的配置。
//
// 场景：校园网里 Steam 的商店/社区被墙，但 CDN 下载不受影响。
// 用户想同时要"社区能开"和"下载走多 IP 聚合"——Steam 只支持填一个代理，
// 所以由我们自己分流：
//
//	Steam → 我们(8080) ─┬─ 匹配 Domains 的域名 → 上游代理（本地 VPN 客户端，如 0dcloud:17891）
//	                     └─ 其余（CDN 下载）   → 校园网多源 IP 并行
//
// 上游只支持 HTTP 代理。CONNECT 隧道与绝对 URI 两种请求形态都能链上去。
type Upstream struct {
	// Proxy 是上游 HTTP 代理地址，形如 http://127.0.0.1:17891。
	Proxy string

	// Domains 是需要走上游的域名规则：
	//   - "example.com"     精确匹配
	//   - "*.example.com"   只匹配子域（不含 example.com 本身）
	//   - ".example.com"    匹配 example.com 及其所有子域
	// 留空表示全部走上游（等价于把本代理退化成纯转发）。
	Domains []string

	// Timeout 是连上游与握手的超时，默认 15s。
	Timeout time.Duration
}

// Validate 校验配置。
func (u Upstream) Validate() error {
	if u.Proxy == "" {
		return errors.New("httpproxy: upstream.proxy 为空")
	}
	if _, err := url.Parse(u.Proxy); err != nil {
		return fmt.Errorf("httpproxy: upstream.proxy 非法: %w", err)
	}
	return nil
}

func (u Upstream) timeout() time.Duration {
	if u.Timeout > 0 {
		return u.Timeout
	}
	return 15 * time.Second
}

func (u Upstream) addr() (string, error) {
	parsed, err := url.Parse(u.Proxy)
	if err != nil {
		return "", fmt.Errorf("httpproxy: 解析上游地址: %w", err)
	}
	if parsed.Port() == "" {
		return net.JoinHostPort(parsed.Hostname(), "80"), nil
	}
	return parsed.Host, nil
}

// Matches 判断某个目标主机是否应该走上游。
func (u Upstream) Matches(host string) bool {
	if u.Proxy == "" {
		return false
	}
	if len(u.Domains) == 0 {
		return true
	}
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, rule := range u.Domains {
		rule = strings.ToLower(strings.TrimSpace(rule))
		if rule == "" {
			continue
		}
		switch {
		case strings.HasPrefix(rule, "*."):
			// *.example.com：只匹配子域
			bare := rule[2:]
			if strings.HasSuffix(h, "."+bare) {
				return true
			}
		case strings.HasPrefix(rule, "."):
			// .example.com：匹配自身与子域
			if h == rule[1:] || strings.HasSuffix(h, rule) {
				return true
			}
		default:
			if h == rule {
				return true
			}
		}
	}
	return false
}

// bufferedConn 让"已经读进 bufio 的字节"和"后续 socket 字节"接续起来。
//
// CONNECT 握手读完响应头之后，bufio.Reader 里可能已经缓存了隧道的第一批数据；
// 直接拿裸连接读会把它们丢掉。
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// hopToUpstream 建立一条"经上游代理"的隧道（CONNECT 链）。
func (s *Server) hopToUpstream(ctx context.Context, target string) (net.Conn, error) {
	up := s.Upstream
	if up == nil {
		return nil, errors.New("httpproxy: 未配置上游")
	}
	upAddr, err := up.addr()
	if err != nil {
		return nil, err
	}

	d := net.Dialer{Timeout: up.timeout()}
	conn, err := d.DialContext(ctx, "tcp", upAddr)
	if err != nil {
		return nil, fmt.Errorf("httpproxy: 连接上游 %s 失败: %w", upAddr, err)
	}

	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n\r\n", target, target)
	if _, err := io.WriteString(conn, connectReq); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("httpproxy: 向上游发 CONNECT 失败: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(up.timeout()))
	br := bufio.NewReader(conn)
	tp := textproto.NewReader(br)
	statusLine, err := tp.ReadLine()
	if err == nil {
		_, err = tp.ReadMIMEHeader()
	}
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("httpproxy: 读上游 CONNECT 响应失败: %w", err)
	}
	if !is2xxStatus(statusLine) {
		_ = conn.Close()
		return nil, fmt.Errorf("httpproxy: 上游拒绝 CONNECT: %s", strings.TrimSpace(statusLine))
	}

	return &bufferedConn{Conn: conn, r: br}, nil
}

// forwardViaUpstream 把明文 HTTP 请求转发给上游代理。
//
// 关键点：发给 HTTP 代理时必须保留**绝对 URI**（这是代理协议的约定），
// 所以这里不能像直连路径那样清空 req.URL。
func (s *Server) forwardViaUpstream(ctx context.Context, client net.Conn, req *http.Request) {
	up := s.Upstream
	upAddr, err := up.addr()
	if err != nil {
		writeStatus(client, http.StatusBadGateway, "上游地址非法")
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, up.timeout())
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(reqCtx, "tcp", upAddr)
	if err != nil {
		writeStatus(client, http.StatusBadGateway, "连接上游代理失败")
		return
	}
	defer conn.Close()

	req.Header.Set("Connection", "close")
	if req.ContentLength == 0 {
		req.Body = http.NoBody
	}
	req.RequestURI = ""
	if req.URL != nil && req.URL.Scheme == "" {
		// 兜底：没带 scheme 的绝对 URI 补回 http，否则代理认不出来。
		req.URL.Scheme = "http"
	}
	if err := req.Write(conn); err != nil {
		writeStatus(client, http.StatusBadGateway, "向上游转发请求失败")
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		writeStatus(client, http.StatusBadGateway, "读上游响应失败")
		return
	}
	defer resp.Body.Close()
	_ = resp.Write(client)
}

func is2xxStatus(statusLine string) bool {
	fields := strings.Fields(statusLine)
	if len(fields) < 2 {
		return false
	}
	return strings.HasPrefix(fields[1], "2")
}
