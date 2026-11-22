// Package socks5 是本地 SOCKS5 代理服务端，把每条 CONNECT 请求交给调度器选路。
//
// 它在整个系统里的位置：
//
//	应用 → SOCKS5 服务端 → scheduler.Pick → dialer.Dial(绑定源 IP) → 目标
//
// 只监听 127.0.0.1（《要求.md》§1 非目标 5：不对外提供代理服务）。
// 这一条不是建议，是项目的合规边界，不要因为"方便调试"改成 0.0.0.0。
package socks5

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// Picker 由 internal/scheduler 实现。这里用窄接口而不是具体类型，
// 是为了让 SOCKS5 层能在测试里注入假调度器，不必搭一套真实的 IP 池。
type Picker interface {
	Pick(network, addr string) (contract.IPPoolEntry, error)
}

// Dialer 由 internal/proxy/dialer 实现。
type Dialer interface {
	DialContext(ctx context.Context, network, address string, entry contract.IPPoolEntry) (net.Conn, error)
}

// Metrics 是数据面的统计回调，由 internal/metrics 实现。
//
// 用窄接口而不是具体类型：socks5 不需要知道速率怎么算、快照怎么组织，
// 测试里也不必为了跑一条连接而搭一套统计设施。所有方法都允许被调用方忽略。
type Metrics interface {
	ConnOpened(entryID string)
	ConnClosed(entryID string)
	AddUp(entryID string, n int64)
	AddDown(entryID string, n int64)
	AddError(entryID string)
}

// Server 是 SOCKS5 服务端。
type Server struct {
	// ListenAddr 必须是回环地址，例如 127.0.0.1:1080。
	ListenAddr string

	Picker Picker
	Dialer Dialer

	// Metrics 可选。非 nil 时统计每条源 IP 的连接数与上下行字节。
	Metrics Metrics

	// HandshakeTimeout 是握手阶段的总超时，默认 10s。
	// 没有这个超时，一个连上就不发数据的客户端会永久占住一个 goroutine。
	HandshakeTimeout time.Duration

	// DialTimeout 是建连超时，默认 10s。
	DialTimeout time.Duration

	Logger *slog.Logger
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

func (s *Server) logger() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

func (s *Server) validate() error {
	if s.Picker == nil {
		return errors.New("socks5: Picker 为空")
	}
	if s.Dialer == nil {
		return errors.New("socks5: Dialer 为空")
	}

	host, _, err := net.SplitHostPort(s.ListenAddr)
	if err != nil {
		return fmt.Errorf("socks5: ListenAddr %q 非法: %w", s.ListenAddr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("socks5: 只允许监听回环地址，当前 %q。"+
			"对外监听会把已认证的校园网会话分享给整个网络，既违规也不安全", s.ListenAddr)
	}
	return nil
}

// ListenAndServe 阻塞运行直到 ctx 取消。
func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", s.ListenAddr)
	if err != nil {
		return fmt.Errorf("socks5: 监听 %s: %w", s.ListenAddr, err)
	}

	return s.Serve(ctx, ln)
}

// Serve 在已有 listener 上服务。测试用它可以拿到随机端口。
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if err := s.validate(); err != nil {
		return err
	}

	// ctx 取消时关闭 listener，让 Accept 立刻返回。
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var wg sync.WaitGroup
	defer wg.Wait()

	s.logger().Info("SOCKS5 服务已启动", "addr", ln.Addr().String())

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("socks5: Accept: %w", err)
			}
		}

		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			s.handle(ctx, c)
		}(conn)
	}
}

func (s *Server) handle(ctx context.Context, local net.Conn) {
	defer local.Close()

	// 握手与请求阶段必须有超时：客户端连上不发数据是常见现象
	// （端口探测、半开扫描），没有超时会一直占着 goroutine。
	_ = local.SetDeadline(time.Now().Add(s.handshakeTimeout()))

	if err := handshake(local); err != nil {
		s.logger().Debug("握手失败", "err", err, "remote", local.RemoteAddr().String())
		return
	}

	req, err := readRequest(local)
	if err != nil {
		s.logger().Debug("请求解析失败", "err", err)
		_ = writeReply(local, repGeneralFailure)
		return
	}

	switch req.cmd {
	case cmdConnect:
		s.handleConnect(ctx, local, req)

	case cmdUDPAssociate:
		s.handleUDPAssociate(ctx, local, req)

	case cmdBind:
		// FTP 主动模式才用 BIND，浏览器和下载器都不需要它。
		// 明确拒绝比假装成功好：假成功会让客户端等一个永远不会来的连接。
		_ = writeReply(local, repCommandNotSupported)

	default:
		_ = writeReply(local, repCommandNotSupported)
	}
}

func (s *Server) handleConnect(ctx context.Context, local net.Conn, req request) {
	target := req.addr()

	entry, err := s.Picker.Pick("tcp", target)
	if err != nil {
		// 池里没有可用 IP。回"网络不可达"比回通用失败更有信息量，
		// 客户端日志里能直接看出是链路问题而不是代理内部错误。
		s.logger().Warn("没有可调度的源 IP", "target", target, "err", err)
		_ = writeReply(local, repNetworkUnreachable)
		return
	}

	dialCtx, cancel := context.WithTimeout(ctx, s.dialTimeout())
	defer cancel()

	upstream, err := s.Dialer.DialContext(dialCtx, "tcp", target, entry)
	if err != nil {
		s.logger().Warn("上游连接失败",
			"target", target, "source_ip", entry.SourceIP, "err", err)
		if s.Metrics != nil {
			s.Metrics.AddError(entry.ID)
		}
		_ = writeReply(local, replyCodeFor(err))
		return
	}
	defer upstream.Close()

	if err := writeReply(local, repSuccess); err != nil {
		return
	}

	s.logger().Debug("连接已建立",
		"target", target, "source_ip", entry.SourceIP, "iface", entry.Interface)

	// 握手结束后清掉 deadline：长连接（下载、视频）跑几十分钟很正常。
	_ = local.SetDeadline(time.Time{})

	var upBytes, downBytes func(int64)
	if m := s.Metrics; m != nil {
		m.ConnOpened(entry.ID)
		// 必须在 pipe 返回之后才减计数，否则 UI 上会看到连接立刻消失、
		// 然后字节数在"没有连接"的情况下继续涨。
		defer m.ConnClosed(entry.ID)
		upBytes = func(n int64) { m.AddUp(entry.ID, n) }
		downBytes = func(n int64) { m.AddDown(entry.ID, n) }
	}

	pipe(local, upstream, downBytes, upBytes)
}

// replyCodeFor 把拨号错误映射成 RFC 1928 的响应码。
//
// 映射准确与否影响排障体验：客户端看到"连接被拒绝"会去查目标端口，
// 看到"通用失败"只会来问我们。
func replyCodeFor(err error) byte {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return repHostUnreachable
	}

	if isConnRefused(err) {
		return repConnectionRefused
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return repHostUnreachable
	}

	return repGeneralFailure
}

// pipe 双向转发，并正确处理半关闭。
//
// 只用 io.Copy 两条会有一个隐患：一端发完 FIN 之后，另一端的 io.Copy 还在等数据，
// 连接要等两边都自然结束才释放。显式 CloseWrite 能让对端立刻收到 EOF，
// 于是像 HTTP/1.0 这种"服务端先关"的协议不会悬着。
//
// aCount / bCount 分别统计"写入 a"和"写入 b"的字节数，可以为 nil。
//
// 按写入侧而不是读取侧计数：读到的字节可能多于实际交付的（对端发完就关），
// 而 UI 想看的是"真正流过多少"。
//
// 代价是包一层后 io.Copy 用不上 net.TCPConn 的 ReadFrom（Linux 上是 splice）。
// 在这个场景里不值得为此绕路：SOCKS5 每跳本来就是用户态拷贝，
// 32KB 缓冲的循环足够跑满千兆。真要零拷贝得直接操作 fd，那和计数天然冲突。
func pipe(a, b net.Conn, aCount, bCount func(int64)) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(countedWriter(a, aCount), b)
		closeWrite(a)
	}()

	go func() {
		defer wg.Done()
		_, _ = io.Copy(countedWriter(b, bCount), a)
		closeWrite(b)
	}()

	wg.Wait()
}

// countedWriter 在写的同时累加字节。count 为 nil 时原样返回，不产生额外分配。
func countedWriter(w io.Writer, count func(int64)) io.Writer {
	if count == nil {
		return w
	}
	return &byteCounter{w: w, count: count}
}

type byteCounter struct {
	w     io.Writer
	count func(int64)
}

func (c *byteCounter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.count(int64(n))
	}
	return n, err
}

func closeWrite(c net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
		return
	}
	// 不是 TCP 连接（比如测试里的 net.Pipe）就直接关闭。
	_ = c.Close()
}
