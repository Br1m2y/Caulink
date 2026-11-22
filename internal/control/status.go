// Package control 暴露本地状态接口，供桌面控制面板与运维脚本读取。
//
// 它在整个系统里的位置：
//
//	ippool（链路状态） ┐
//	metrics（流量统计）├→ control 拼装 → GET /api/v1/status → 桌面面板
//	scheduler（策略）  ┘
//
// 只监听回环地址，且不提供任何**写**操作。理由和 SOCKS5 那条一样：
// 对外暴露等于把已认证的校园网会话状态分享出去；而一个能改状态的本地接口
// 等于给任何本机进程提供了绕开调度的入口。UI 要改配置就改配置文件后重启。
//
// 这个包的响应结构就是 B → C 的接口契约（见 docs/status-api.md）。
// 改字段前先看那份文档里的兼容性约定。
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/metrics"
	"github.com/Br1m2y/caulink/internal/scheduler"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// StickyReporter 由 internal/scheduler 实现。
type StickyReporter interface {
	StickyID() string
}

// StatusServer 是状态接口服务端。
type StatusServer struct {
	// Listen 必须是回环地址，例如 127.0.0.1:7788。
	Listen string

	Pool    *ippool.Pool
	Metrics *metrics.Registry
	// Scheduler 可选。为 nil 时 scheduler 段只给出策略名，不给粘性 IP。
	Scheduler StickyReporter

	// Socks5Addr 会原样出现在响应里，方便 UI 显示"代理监听在哪"。
	Socks5Addr string
	// HTTPProxyAddr 是可选 HTTP 代理的监听地址；为空表示未启用。
	HTTPProxyAddr string
	// Version 是构建版本，测试里可以随便填。
	Version string

	// ProxyHold 可选。为 nil 时"按住系统代理"接口返回 501。
	ProxyHold ProxyHoldController

	// Sources 可选。为 nil 时"重新扫描"接口返回 501。
	Sources SourceController

	// Auth 可选。为 nil 时认证接口返回 501。
	Auth AuthController

	// Accel 可选。为 nil 时加速下载接口返回 501。
	//
	// 这是控制面里唯一会写文件的操作：下载目标由用户指定，落盘目录也是。
	// 所以它和登录一样要求 X-Caulink-Action 头（见 accel.go）。
	Accel AccelController

	// Steam 可选。为 nil 时 Steam 控制接口返回 501。
	//
	// 这是控制面里唯一的写操作入口，且只暴露"重启 Steam 以支持多 IP"这一件事，
	// 由 caulinkd 执行（桌面端进程在本机可能被限制写系统代理）。
	Steam SteamController

	Logger *slog.Logger

	mu        sync.Mutex
	startedAt time.Time
}

func (s *StatusServer) logger() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

func (s *StatusServer) validate() error {
	if s.Pool == nil {
		return errors.New("control: Pool 为空")
	}
	host, _, err := net.SplitHostPort(s.Listen)
	if err != nil {
		return fmt.Errorf("control: Listen %q 非法: %w", s.Listen, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("control: 只允许监听回环地址，当前 %q。"+
			"状态接口会暴露内网地址与会话状态，不能对外", s.Listen)
	}
	return nil
}

// markStarted 记录启动时刻，只在第一次生效。
func (s *StatusServer) markStarted() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startedAt.IsZero() {
		s.startedAt = time.Now().UTC()
	}
	return s.startedAt
}

// ListenAndServe 阻塞运行直到 ctx 取消。
func (s *StatusServer) ListenAndServe(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", s.Listen)
	if err != nil {
		return fmt.Errorf("control: 监听 %s: %w", s.Listen, err)
	}
	return s.Serve(ctx, ln)
}

// Serve 在已有 listener 上服务。测试用它拿随机端口。
func (s *StatusServer) Serve(ctx context.Context, ln net.Listener) error {
	if err := s.validate(); err != nil {
		return err
	}
	s.markStarted()

	srv := &http.Server{
		Handler: s.routes(),
		// 只服务本机 UI，不需要长连接管理，但头部读取必须设超时 ——
		// 否则一个连上不发请求的客户端能一直占着连接。
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	s.logger().Info("状态接口已启动", "addr", ln.Addr().String())

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("control: 服务异常: %w", err)
	}
	return nil
}

func (s *StatusServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", s.handleStatus)
	mux.HandleFunc("/healthz", s.handleHealthz)
	s.registerSteamRoutes(mux)
	s.registerAuthRoutes(mux)
	s.registerSourceRoutes(mux)
	s.registerProxyHoldRoutes(mux)
	s.registerAccelRoutes(mux)
	return withCORS(mux)
}

// withCORS 允许跨域读取。
//
// 桌面端跑在 Wails 的 WebView 里，页面来自自定义 scheme 或本地开发服务器，
// 与 127.0.0.1:7788 不同源。不返回这些头，浏览器的 fetch 会直接拦掉请求 ——
// 而服务端连一条日志都不会有，现象是"接口明明能用 curl 访问，界面上就是空数据"。
//
// 允许 `*` 的代价：本机任何网页都能读到这些数据（源 IP、速率、连接数）。
// 权衡后接受，因为：接口只读、不带凭据、只监听回环。
// 如果将来要收紧，把 `*` 换成 origin 白名单（wails://、http://wails.localhost）。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// 预检请求不进入业务处理，否则会被 allowGet 判成 405，
		// 浏览器看到预检失败就不会再发真正的 GET。
		if r.Method == http.MethodOptions {
			// POST 只用于两个写接口（认证登录、Steam 控制），
			// 它们都要求 X-Caulink-Action 头，所以这里必须放行。
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Caulink-Action")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *StatusServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if !allowGet(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *StatusServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !allowGet(w, r) {
		return
	}

	resp := s.buildStatus()

	body, err := json.Marshal(resp)
	if err != nil {
		s.logger().Error("序列化状态失败", "err", err)
		http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// UI 每秒轮询，任何中间缓存都会让它显示过期数据。
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func allowGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	return false
}

// buildStatus 把三个数据源拼成一份响应。
//
// 池是权威来源，统计与调度只提供附加字段：某个 ID 在 metrics 里没有记录时
// 补零而不是丢条目 —— UI 少一行比整块空白更让人困惑。
func (s *StatusServer) buildStatus() StatusResponse {
	entries := s.Pool.Snapshot()

	measured := make(map[string]metrics.Sample, len(entries))
	if s.Metrics != nil {
		for _, m := range s.Metrics.Snapshot() {
			measured[m.ID] = m
		}
	}

	started := s.markStarted()
	resp := StatusResponse{
		Version:       s.Version,
		StartedAt:     started,
		UptimeSeconds: time.Since(started).Seconds(),
		Socks5:        s.Socks5Addr,
		HTTPProxy:     s.HTTPProxyAddr,
		IPs:           make([]IPStatus, 0, len(entries)),
	}

	var tot Totals
	tot.IPCount = len(entries)

	for _, e := range entries {
		m := measured[e.ID]

		st := IPStatus{
			IPPoolEntry: e,
			InPool:      e.Schedulable(),
			Weight:      scheduler.WeightOf(e),
			ActiveConns: m.ActiveConns,
			TotalConns:  m.TotalConns,
			BytesUp:     m.BytesUp,
			BytesDown:   m.BytesDown,
			RateUpBps:   m.Rate.UpBps,
			RateDownBps: m.Rate.DownBps,
			Errors:      m.Errors,
		}
		if st.InPool {
			tot.SchedulableCount++
		}

		tot.ActiveConns += st.ActiveConns
		tot.TotalConns += st.TotalConns
		tot.BytesUp += st.BytesUp
		tot.BytesDown += st.BytesDown
		tot.RateUpBps += st.RateUpBps
		tot.RateDownBps += st.RateDownBps
		tot.Errors += st.Errors

		resp.IPs = append(resp.IPs, st)
	}

	resp.Totals = tot
	resp.Scheduler = s.buildScheduler(entries, tot)
	return resp
}

func (s *StatusServer) buildScheduler(entries []contract.IPPoolEntry, tot Totals) SchedulerStatus {
	out := SchedulerStatus{
		Strategy:   "smooth-weighted-round-robin",
		Candidates: tot.SchedulableCount,
	}
	if s.Scheduler == nil {
		return out
	}

	out.StickyID = s.Scheduler.StickyID()
	if out.StickyID == "" {
		return out
	}
	// 顺手把粘性 IP 解出来，UI 不必再拿 ID 去 IPs 里查一遍。
	for _, e := range entries {
		if e.ID == out.StickyID {
			out.StickyIP = e.SourceIP
			break
		}
	}
	return out
}
