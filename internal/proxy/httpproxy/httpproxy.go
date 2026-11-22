// Package httpproxy 提供只监听回环的 HTTP 代理（CONNECT 隧道 + 明文转发）。
//
// 它在整个系统里的位置与 SOCKS5 完全一致：
//
//	应用 → HTTP 代理 → scheduler.Pick → dialer.Dial(绑定源 IP) → 目标
//
// 为什么还需要它：Steam 客户端的代理设置**只支持 HTTP 代理**，不支持 SOCKS5。
// 没有这个包，Steam 的多连接下载就没法走多 IP 调度。
//
// 安全约定与 SOCKS5 相同：只允许回环监听，绝不对外提供服务。
package httpproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// Picker 由 internal/scheduler 实现。
type Picker interface {
	Pick(network, addr string) (contract.IPPoolEntry, error)
}

// Dialer 由 internal/proxy/dialer 实现。
type Dialer interface {
	DialContext(ctx context.Context, network, address string, entry contract.IPPoolEntry) (net.Conn, error)
}

// Metrics 是数据面的统计回调，由 internal/metrics 实现。
type Metrics interface {
	ConnOpened(entryID string)
	ConnClosed(entryID string)
	AddUp(entryID string, n int64)
	AddDown(entryID string, n int64)
	AddError(entryID string)
}

// Server 是 HTTP 代理服务端。
type Server struct {
	// ListenAddr 必须是回环地址，例如 127.0.0.1:8080。
	ListenAddr string

	Picker Picker
	Dialer Dialer

	// Metrics 可选。
	Metrics Metrics

	// Upstream 可选。非空时，命中规则的目标域名交给上游 HTTP 代理，
	// 其余仍走多源 IP。用于"被墙的站点走 VPN、下载走聚合"。
	Upstream *Upstream

	// HandshakeTimeout 是读到完整请求头的超时，默认 10s。
	HandshakeTimeout time.Duration
	// DialTimeout 是建连超时，默认 10s。
	DialTimeout time.Duration

	Logger *slog.Logger
}

func (s *Server) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Server) handshakeTimeout() time.Duration {
	if s.HandshakeTimeout <= 0 {
		return 10 * time.Second
	}
	return s.HandshakeTimeout
}

func (s *Server) dialTimeout() time.Duration {
	if s.DialTimeout <= 0 {
		return 10 * time.Second
	}
	return s.DialTimeout
}

func (s *Server) validate() error {
	if s.Picker == nil {
		return errors.New("httpproxy: Picker 为空")
	}
	if s.Upstream != nil {
		if err := s.Upstream.Validate(); err != nil {
			return err
		}
	}
	if s.Dialer == nil {
		return errors.New("httpproxy: Dialer 为空")
	}
	if s.ListenAddr == "" {
		return errors.New("httpproxy: ListenAddr 为空")
	}
	host, _, err := net.SplitHostPort(s.ListenAddr)
	if err != nil {
		return fmt.Errorf("httpproxy: ListenAddr %q 非法: %w", s.ListenAddr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("httpproxy: 只允许监听回环地址，当前 %q", s.ListenAddr)
	}
	return nil
}

// ListenAndServe 监听 ListenAddr 并服务，直到 ctx 结束。
func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", s.ListenAddr)
	if err != nil {
		return fmt.Errorf("httpproxy: 监听 %s: %w", s.ListenAddr, err)
	}
	defer ln.Close()
	return s.Serve(ctx, ln)
}

// Serve 在给定 listener 上服务，便于测试注入。
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("httpproxy: accept: %w", err)
		}
		go s.handle(ctx, conn)
	}
}

func (s *Server) handle(ctx context.Context, client net.Conn) {
	defer client.Close()

	deadline := time.Now().Add(s.handshakeTimeout())
	_ = client.SetReadDeadline(deadline)

	br := bufio.NewReader(client)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = client.SetReadDeadline(time.Time{})

	if req.Method == http.MethodConnect {
		s.handleConnect(ctx, client, req)
		return
	}
	s.handleForward(ctx, client, br, req)
}

// handleConnect 建立 CONNECT 隧道：这是 HTTPS 与 Steam 下载走的主路径。
func (s *Server) handleConnect(ctx context.Context, client net.Conn, req *http.Request) {
	target := req.Host
	if target == "" {
		target = req.URL.Host
	}
	if target == "" {
		writeStatus(client, http.StatusBadRequest, "CONNECT 缺少目标")
		return
	}

	// 命中上游规则的域名（例如被墙的 Steam 社区）交给上游代理，
	// 其余仍走校园网多源 IP——这就是"社区能开 + 下载聚合"的分流点。
	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}
	if s.Upstream != nil && s.Upstream.Matches(host) {
		tun, err := s.hopToUpstream(ctx, target)
		if err != nil {
			s.logger().Warn("走上游代理失败，回退到直连",
				"target", target, "host", host, "err", err)
		} else {
			defer tun.Close()
			if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
				return
			}
			// 走上游的连接不计入本机源 IP 的统计：字节没走校园网链路，
			// 计进去会让"每 IP 速率"失真。
			pipe(client, tun, "", nil)
			return
		}
	}

	entry, err := s.Picker.Pick("tcp", target)
	if err != nil {
		s.recordError("")
		writeStatus(client, http.StatusServiceUnavailable, "没有可用的源 IP")
		return
	}

	dialCtx, cancel := context.WithTimeout(ctx, s.dialTimeout())
	upstream, err := s.Dialer.DialContext(dialCtx, "tcp", target, entry)
	cancel()
	if err != nil {
		s.recordError(entry.ID)
		writeStatus(client, http.StatusBadGateway, "建连失败")
		return
	}
	defer upstream.Close()

	if s.Metrics != nil {
		s.Metrics.ConnOpened(entry.ID)
		defer s.Metrics.ConnClosed(entry.ID)
	}

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	pipe(client, upstream, entry.ID, s.Metrics)
}

// handleForward 处理明文 HTTP 请求（绝对 URI 形式），一次请求一条连接。
//
// 不支持 keep-alive 与 pipelining：代理只负责把请求转出去，
// 客户端要复用连接请走 CONNECT（HTTPS）或 SOCKS5。
func (s *Server) handleForward(ctx context.Context, client net.Conn, br *bufio.Reader, req *http.Request) {
	if !req.URL.IsAbs() || req.URL.Host == "" {
		writeStatus(client, http.StatusBadRequest, "只支持 CONNECT 或绝对 URI 形式的 HTTP 请求")
		return
	}
	target := req.URL.Host
	if req.URL.Port() == "" {
		target = net.JoinHostPort(req.URL.Hostname(), "80")
	}

	// 明文 HTTP 也要支持分流：命中规则就走上游代理。
	if s.Upstream != nil && s.Upstream.Matches(req.URL.Hostname()) {
		s.forwardViaUpstream(ctx, client, req)
		return
	}

	entry, err := s.Picker.Pick("tcp", target)
	if err != nil {
		s.recordError("")
		writeStatus(client, http.StatusServiceUnavailable, "没有可用的源 IP")
		return
	}

	dialCtx, cancel := context.WithTimeout(ctx, s.dialTimeout())
	upstream, err := s.Dialer.DialContext(dialCtx, "tcp", target, entry)
	cancel()
	if err != nil {
		s.recordError(entry.ID)
		writeStatus(client, http.StatusBadGateway, "建连失败")
		return
	}
	defer upstream.Close()

	if s.Metrics != nil {
		s.Metrics.ConnOpened(entry.ID)
		defer s.Metrics.ConnClosed(entry.ID)
	}

	// 转成 origin-form 再发给目标服务器。
	//
	// 收发两条路径都要挂计数：Steam 的 CDN 下载走的是明文 HTTP（80 端口），
	// 如果不在这里计数，面板会显示 0 速度——用户会以为调度没生效，
	// 实际上流量正从两个源 IP 上跑着（实测 94 Mbps，面板却是 0）。
	var up, down *countingWriter
	if s.Metrics != nil {
		up = &countingWriter{w: upstream, onWrite: func(n int64) { s.Metrics.AddUp(entry.ID, n) }}
		down = &countingWriter{w: client, onWrite: func(n int64) { s.Metrics.AddDown(entry.ID, n) }}
	}

	req.RequestURI = ""
	req.URL.Scheme = ""
	req.URL.Host = ""
	req.Header.Set("Connection", "close")
	if req.ContentLength == 0 {
		req.Body = http.NoBody
	}
	var reqWriter io.Writer = upstream
	if up != nil {
		reqWriter = up
	}
	if err := req.Write(reqWriter); err != nil {
		s.recordError(entry.ID)
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(upstream), req)
	if err != nil {
		s.recordError(entry.ID)
		return
	}
	defer resp.Body.Close()

	var respWriter io.Writer = client
	if down != nil {
		respWriter = down
	}
	if err := resp.Write(respWriter); err != nil {
		return
	}
}

func (s *Server) recordError(entryID string) {
	if s.Metrics != nil && entryID != "" {
		s.Metrics.AddError(entryID)
	}
}

func writeStatus(w io.Writer, code int, msg string) {
	body := msg + "\n"
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(body), body)
}

// pipe 双向搬运并统计字节数。任一端 EOF 时半关闭另一端的写方向。
func pipe(a, b net.Conn, entryID string, m Metrics) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		n, _ := io.Copy(&countingWriter{w: b, onWrite: func(n int64) {
			if m != nil && entryID != "" {
				m.AddUp(entryID, n)
			}
		}}, a)
		_ = n
		closeWrite(b)
	}()

	go func() {
		defer wg.Done()
		n, _ := io.Copy(&countingWriter{w: a, onWrite: func(n int64) {
			if m != nil && entryID != "" {
				m.AddDown(entryID, n)
			}
		}}, b)
		_ = n
		closeWrite(a)
	}()

	wg.Wait()
}

type countingWriter struct {
	w       io.Writer
	onWrite func(int64)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.onWrite(int64(n))
	}
	return n, err
}

// closeWrite 尽力半关闭写方向，让对端知道数据发完了。
func closeWrite(c net.Conn) {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.SetWriteDeadline(time.Now())
}
