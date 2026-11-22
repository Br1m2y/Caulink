package socks5

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// UDP ASSOCIATE 的实现要点，先说清楚为什么这么写。
//
// 1) 每个 (客户端, 目标) 组合占用一个**已连接**的 UDP socket。
//
// 已经 connected 的 socket 只会收到该目标发来的包，省掉了自己按来源过滤这一步，
// 也不会把别的目标的包误投给这个客户端。代价是每个目标占一个本地端口，
// 所以必须加上限和老化 —— 一个 QUIC 浏览器会瞬间开出几十个会话。
//
// 2) 同一个目标必须永远走同一个源 IP。
//
// 这一条比 TCP 更硬：UDP 没有连接概念，游戏服务器靠 (源 IP, 源端口) 认玩家。
// 中途换 IP 在服务端看来就是一个新玩家上线、旧玩家掉线，表现为瞬断。
// 会话表天然提供了这个粘性 —— 只要会话还活着就不重新 Pick。
//
// 3) 源 IP 掉线时不静默换线，而是让会话自然死掉。
//
// 换线会让服务端看到新会话，而且掩盖了"这条链路已经不可用"这个事实。
// 让会话超时、由客户端下次发包时重新走 Pick，行为更可解释。
const (
	// udpMaxDatagram 是单个 UDP 数据报的缓冲上限。IPv4 的 UDP 载荷上限是 65507。
	udpMaxDatagram = 65535

	// udpSessionIdle 是会话空闲多久后回收。
	//
	// 60 秒的取舍：游戏的保活包通常 5–20 秒一个，QUIC 的 idle timeout 默认 30 秒。
	// 太短会把刚要复活的会话拆掉（客户端要重新打洞），太长会让端口被僵尸会话占满。
	udpSessionIdle = 60 * time.Second

	// udpMaxSessions 是单个关联的会话上限。
	//
	// 超限时丢弃新会话而不是淘汰最旧的：淘汰最旧的会打断正在进行的会话，
	// 而丢一个新包只是让那条新流慢一拍。
	udpMaxSessions = 512

	// udpDialTimeout 是建立上游 UDP socket 的超时。DNS 解析也走这里。
	udpDialTimeout = 5 * time.Second

	// udpMaxConsecutiveErrors 是容忍的连续读错误次数。
	udpMaxConsecutiveErrors = 5
)

// udpSession 是一条 UDP 流：一个客户端地址对应一个目标地址。
type udpSession struct {
	key       string
	target    string
	client    *net.UDPAddr
	replyAddr *net.UDPAddr // 上游实际地址，回包时填进报文头
	conn      net.Conn
	entry     contract.IPPoolEntry

	lastSeen  atomic.Int64 // UnixNano
	closeOnce sync.Once
	// onClose 在会话首次关闭时执行，用来抵消 ConnOpened 的计数。
	// 挂在 closeOnce 上是唯一能保证"每条会话只减一次"的位置 ——
	// 会话可能被 pumpInbound、janitor、shutdown 三条路径同时关掉。
	onClose func()
}

func (s *udpSession) touch() { s.lastSeen.Store(time.Now().UnixNano()) }

func (s *udpSession) idle() time.Duration {
	return time.Since(time.Unix(0, s.lastSeen.Load()))
}

func (s *udpSession) close() {
	s.closeOnce.Do(func() {
		_ = s.conn.Close()
		if s.onClose != nil {
			s.onClose()
		}
	})
}

// udpRelay 承载一个 UDP ASSOCIATE 关联的生命周期。
type udpRelay struct {
	srv *Server
	pc  *net.UDPConn // 面向客户端的 socket，端口即回给客户端的 BND.PORT

	ctx  context.Context
	stop context.CancelFunc

	// want 是客户端在请求里声明的自身 UDP 地址。非 nil 时严格校验来源，
	// nil 时锁定第一个发包的来源地址。
	want *net.UDPAddr

	mu       sync.Mutex
	sessions map[string]*udpSession
	locked   *net.UDPAddr // want 为 nil 时锁定的客户端地址
	closed   bool

	wg sync.WaitGroup
}

// run 阻塞直到关联结束。
func (r *udpRelay) run() {
	r.wg.Add(1)
	go r.janitor()

	buf := make([]byte, udpMaxDatagram)
	for {
		n, from, err := r.pc.ReadFromUDP(buf)
		if err != nil {
			if r.ctx.Err() == nil {
				// 本地 socket 出错通常意味着配置或权限问题，继续循环只会空转。
				r.srv.logger().Debug("UDP 接收客户端数据失败", "err", err)
			}
			break
		}
		r.handleDatagram(buf[:n], from)
	}

	r.shutdown()
}

// shutdown 关闭所有会话并等待后台协程退出。可重复调用。
func (r *udpRelay) shutdown() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	open := make([]*udpSession, 0, len(r.sessions))
	for _, s := range r.sessions {
		open = append(open, s)
	}
	r.sessions = make(map[string]*udpSession)
	r.mu.Unlock()

	if len(open) > 0 {
		r.srv.logger().Debug("UDP 关联结束，回收会话", "count", len(open))
	}
	for _, s := range open {
		s.close()
	}
	r.stop()
	r.wg.Wait()
}

// allow 判断一个来源地址是否属于本关联。
//
// 目的不是防外部攻击（端口只在回环上），而是防止本机其他进程往这个端口
// 灌数据 —— 那种情况下代理会替它建会话，把它变成一条免费出口。
func (r *udpRelay) allow(from *net.UDPAddr) bool {
	if r.want != nil {
		return r.want.IP.Equal(from.IP) && r.want.Port == from.Port
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.locked == nil {
		r.locked = from
		return true
	}
	return r.locked.String() == from.String()
}

func (r *udpRelay) handleDatagram(b []byte, from *net.UDPAddr) {
	if !r.allow(from) {
		r.srv.logger().Debug("丢弃非关联客户端的 UDP 包", "from", from.String())
		return
	}

	dg, err := parseUDPDatagram(b)
	if err != nil {
		if err != ErrUDPFragment {
			r.srv.logger().Debug("UDP 报文解析失败", "from", from.String(), "err", err)
		}
		return
	}

	target := dg.addr()
	key := from.String() + "|" + target

	r.mu.Lock()
	sess := r.sessions[key]
	over := len(r.sessions) >= udpMaxSessions
	r.mu.Unlock()

	if sess != nil {
		r.send(sess, dg.payload)
		return
	}
	if over {
		r.srv.logger().Warn("UDP 会话数已达上限，丢弃新会话",
			"target", target, "limit", udpMaxSessions)
		return
	}

	sess, err = r.open(from, target)
	if err != nil {
		r.srv.logger().Debug("UDP 会话建立失败", "target", target, "err", err)
		return
	}
	r.send(sess, dg.payload)
}

func (r *udpRelay) open(from *net.UDPAddr, target string) (*udpSession, error) {
	entry, err := r.srv.Picker.Pick("udp", target)
	if err != nil {
		return nil, fmt.Errorf("没有可调度的源 IP: %w", err)
	}

	ctx, cancel := context.WithTimeout(r.ctx, udpDialTimeout)
	defer cancel()

	conn, err := r.srv.Dialer.DialContext(ctx, "udp", target, entry)
	if err != nil {
		return nil, err
	}

	replyAddr, ok := conn.RemoteAddr().(*net.UDPAddr)
	if !ok {
		// 目标写的是域名时，RemoteAddr 给的是解析后的地址，正是回包头里该填的。
		_ = conn.Close()
		return nil, fmt.Errorf("上游地址类型异常: %T", conn.RemoteAddr())
	}

	sess := &udpSession{
		key:       from.String() + "|" + target,
		target:    target,
		client:    from,
		replyAddr: replyAddr,
		conn:      conn,
		entry:     entry,
	}
	sess.touch()

	// 在插入会话表之前就把计数与回调配对好：这样后面任何一条失败路径
	// 都能用 sess.close() 干净地回滚，不会留下一个只增不减的活跃连接数。
	if m := r.srv.Metrics; m != nil {
		m.ConnOpened(entry.ID)
		sess.onClose = func() { m.ConnClosed(entry.ID) }
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		sess.close()
		return nil, fmt.Errorf("关联已关闭")
	}
	r.sessions[sess.key] = sess
	r.mu.Unlock()

	r.srv.logger().Debug("UDP 会话已建立",
		"target", target, "source_ip", entry.SourceIP, "iface", entry.Interface)

	r.wg.Add(1)
	go r.pumpInbound(sess)

	return sess, nil
}

func (r *udpRelay) send(s *udpSession, payload []byte) {
	s.touch()
	n, err := s.conn.Write(payload)
	if n > 0 && r.srv.Metrics != nil {
		r.srv.Metrics.AddUp(s.entry.ID, int64(n))
	}
	if err != nil {
		// 写失败不立刻拆会话：connected UDP socket 在 ICMP 不可达之后
		// 会把错误返回一次，下一次写通常能成功。
		r.srv.logger().Debug("UDP 上行发送失败", "target", s.target, "err", err)
	}
}

// pumpInbound 把上游回来的包封装成 SOCKS5 报文发给客户端。
func (r *udpRelay) pumpInbound(s *udpSession) {
	defer r.wg.Done()
	defer r.remove(s)

	buf := make([]byte, udpMaxDatagram)
	errCount := 0

	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(udpSessionIdle))

		n, err := s.conn.Read(buf)
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			if isTimeout(err) {
				// 空闲超时，交给 janitor 统一回收，这里直接结束协程即可。
				return
			}
			// Windows 上对一个刚发出 ICMP 端口不可达的目标读，会得到
			// WSAECONNRESET。UDP 无连接，这只是一次瞬时错误。
			errCount++
			if errCount >= udpMaxConsecutiveErrors {
				return
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		errCount = 0
		s.touch()

		pkt := buildUDPDatagram(s.replyAddr, buf[:n])
		if _, err := r.pc.WriteToUDP(pkt, s.client); err != nil {
			if r.ctx.Err() != nil {
				return
			}
			r.srv.logger().Debug("UDP 回包发送失败",
				"client", s.client.String(), "err", err)
			continue
		}

		if m := r.srv.Metrics; m != nil {
			// 统计载荷长度而不是含 SOCKS5 头的整包长度：
			// UI 上的速率要和"实际数据量"对得上，头部的 10/22 字节是协议开销。
			m.AddDown(s.entry.ID, int64(n))
		}
	}
}

func (r *udpRelay) remove(s *udpSession) {
	r.mu.Lock()
	if cur, ok := r.sessions[s.key]; ok && cur == s {
		delete(r.sessions, s.key)
	}
	r.mu.Unlock()
	s.close()
}

func (r *udpRelay) janitor() {
	defer r.wg.Done()

	t := time.NewTicker(udpSessionIdle / 2)
	defer t.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
			var expired []*udpSession

			r.mu.Lock()
			for k, s := range r.sessions {
				if s.idle() > udpSessionIdle {
					delete(r.sessions, k)
					expired = append(expired, s)
				}
			}
			r.mu.Unlock()

			for _, s := range expired {
				s.close()
			}
		}
	}
}

// handleUDPAssociate 处理 UDP ASSOCIATE 请求。
//
// 流程遵循 RFC 1928：开一个 UDP socket，把它的地址放进 BND 告诉客户端，
// 然后在 TCP 控制连接的整个生命周期内转发数据。
// 控制连接一断，关联立即终止 —— 这是协议规定的语义，也是防止
// 客户端崩溃后残留一个永久免费的 UDP 出口。
func (s *Server) handleUDPAssociate(ctx context.Context, ctrl net.Conn, req request) {
	bindIP := s.udpBindIP()

	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: bindIP, Port: 0})
	if err != nil {
		s.logger().Warn("UDP 端口分配失败", "bind", bindIP.String(), "err", err)
		_ = writeReply(ctrl, repGeneralFailure)
		return
	}
	defer pc.Close()

	local, ok := pc.LocalAddr().(*net.UDPAddr)
	if !ok {
		_ = writeReply(ctrl, repGeneralFailure)
		return
	}

	if err := writeReplyAddr(ctrl, repSuccess, local); err != nil {
		return
	}

	// 握手阶段设的 deadline 是为短请求准备的，UDP 关联可能持续几小时。
	_ = ctrl.SetDeadline(time.Time{})

	// 客户端声明的自身 UDP 地址。绝大多数客户端给 0.0.0.0:0，表示"等下告诉你"。
	var want *net.UDPAddr
	if ip := net.ParseIP(req.host); ip != nil && !ip.IsUnspecified() && req.port != 0 {
		want = &net.UDPAddr{IP: ip, Port: int(req.port)}
	}

	relayCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	relay := &udpRelay{
		srv:      s,
		pc:       pc,
		ctx:      relayCtx,
		stop:     cancel,
		want:     want,
		sessions: make(map[string]*udpSession),
	}

	// TCP 控制连接被关闭时终止关联。io.Copy 会一直阻塞到 EOF 或出错。
	go func() {
		_, _ = io.Copy(io.Discard, ctrl)
		cancel()
	}()

	// 关联被取消时（客户端断开、或服务整体退出）必须关闭 UDP socket，
	// 否则 relay.run 会永远阻塞在 ReadFromUDP 上 —— 服务停止时进程退不掉。
	done := make(chan struct{})
	go func() {
		select {
		case <-relayCtx.Done():
			_ = pc.Close()
		case <-done:
		}
	}()

	s.logger().Debug("UDP 关联已建立", "bind", local.String(), "client_want", req.addr())

	relay.run()
	close(done)
}

// udpBindIP 从监听地址推导 UDP socket 应该绑定的地址。
//
// 必须和 TCP 监听地址同 IP：客户端会拿 BND 里的地址去发包，
// 如果 UDP 绑在另一个地址上，回环都不一定通。
func (s *Server) udpBindIP() net.IP {
	host, _, err := net.SplitHostPort(s.ListenAddr)
	if err != nil {
		return net.IPv4(127, 0, 0, 1)
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	return net.IPv4(127, 0, 0, 1)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
