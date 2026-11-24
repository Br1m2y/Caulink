// Package accel 把一个大文件拆成多段，分别从不同的源 IP 下载。
//
// 为什么必须在客户端分片（而不是在代理里做）：
// 代理的 CONNECT 隧道里跑的是已加密的 TLS 流，代理看不到 HTTP 头，无法插入
// Range、也无法把一条下载拆成多段。想在代理层拆只能解密，那意味着给用户装根证书
// ——在校园网环境里解开别人的流量，合规上说不过去。所以"让下载变快"这件事，
// 只能由程序自己当下载器。
//
// 一条铁律：**单条 TCP 连接不可能中途换源 IP**（四元组固定）。
// 这里做的是"多段 × 多条连接 × 每条各自钉死在一个 IP 上"，和 IDM / aria2 同一套思路。
//
// 容量来自哪里：《要求.md》§2 的实测结论是限速锚定 IP，不是连接数。
// 所以同一个 IP 上开 8 条连接不会更快（实测 0.98×），多一个 IP 才多一份额度。
// 真正跑出叠加比的是"两条链路各自扛一部分"。
package accel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/metrics"
	"github.com/Br1m2y/caulink/internal/proxy/dialer"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// 状态取值。字符串而不是 int，是因为它要出现在 JSON 与 UI 上。
const (
	StatusQueued   = "queued"
	StatusRunning  = "running"
	StatusDone     = "done"
	StatusFailed   = "failed"
	StatusCanceled = "canceled"
)

const (
	// DefaultConnections 是默认并发分片数。
	//
	// 8 而不是 16 的理由：分片数超过 IP 数的 2 倍没有意义（同一 IP 上的分片只会
	// 抢同一份额度），而 8 在两条链路下正好是 2×4，实测能跑满两条。
	DefaultConnections = 8

	// MaxConnections 是上限，防止把并发开到把校园网门户惹毛。
	MaxConnections = 32

	// minChunkSize 是单片下限。低于这个尺寸时，"多开一条连接"的握手与 TLS
	// 开销会吃掉全部收益，不如老老实实单流。
	minChunkSize = 2 << 20

	// maxAttempts 是单片最多尝试次数。每次重试都会换一个条目，
	// 所以"换 IP 再试"是默认行为，不是特例。
	maxAttempts = 3
)

// ErrNotFound 表示任务 ID 不存在。
var ErrNotFound = errors.New("accel: 任务不存在")

// Request 是一次下载请求（来自控制接口）。
type Request struct {
	// URL 必须是 http/https 直链。网页、网盘分享页不行 —— 那些拿到的是 HTML。
	URL string `json:"url"`
	// Dir 是保存目录。为空时用 Options.DefaultDir。
	Dir string `json:"dir,omitempty"`
	// Connections 是并发分片数。0 表示用默认值。
	Connections int `json:"connections,omitempty"`
}

// Options 是 Manager 的依赖。
type Options struct {
	// Pool 提供可调度的源 IP。不能为空。
	Pool *ippool.Pool
	// Dialer 是做了源地址绑定 + 出口网卡绑定的拨号器。
	Dialer dialer.Dialer
	// Metrics 可选。接上之后加速流量会进面板的总速率与每端口曲线。
	Metrics *metrics.Registry
	// DefaultDir 是默认保存目录。
	DefaultDir string
	// StatePath 是记住用户所选目录的 JSON 文件路径。为空表示不持久化。
	StatePath string
	// Logger 可选。
	Logger *slog.Logger
}

// Manager 管理下载任务队列。
//
// 同一时刻只跑一个任务：多个任务并发只会互相抢同一份额度，
// 而用户看到的"总速度"并不会变快，只会让进度条都走得很慢。
// 后来的任务排队，状态里如实显示 queued。
type Manager struct {
	opts Options

	mu     sync.Mutex
	tasks  map[string]*task
	order  []string
	active *task
	kick   chan struct{}

	// dir 是当前默认保存目录。可由用户改，改完落到 opts.StatePath。
	dir string
}

// NewManager 创建管理器并启动调度循环。
func NewManager(ctx context.Context, opts Options) (*Manager, error) {
	if opts.Pool == nil {
		return nil, errors.New("accel: Pool 不能为空")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.DefaultDir == "" {
		opts.DefaultDir = defaultDir()
	}

	m := &Manager{
		opts:  opts,
		tasks: make(map[string]*task),
		kick:  make(chan struct{}, 1),
		dir:   opts.DefaultDir,
	}
	m.loadState()

	go m.loop(ctx)
	return m, nil
}

// DefaultDir 返回当前保存目录，供 UI 预填。
func (m *Manager) DefaultDir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dir
}

// MaxConnections 返回并发上限，供 UI 限制输入。
func (m *Manager) MaxConnections() int { return MaxConnections }

// Start 提交一个任务，返回入队后的快照。
func (m *Manager) Start(req Request) (TaskView, error) {
	u, err := normalizeURL(req.URL)
	if err != nil {
		return TaskView{}, err
	}

	conns := req.Connections
	if conns <= 0 {
		conns = DefaultConnections
	}
	if conns > MaxConnections {
		conns = MaxConnections
	}

	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		dir = m.DefaultDir()
	} else {
		// 用户这次显式指定了目录：校验后记住它，下次打开界面就是这里。
		norm, nerr := NormalizeDir(dir)
		if nerr != nil {
			return TaskView{}, nerr
		}
		dir = norm
		m.mu.Lock()
		m.dir = dir
		m.mu.Unlock()
		m.saveState(dir)
	}

	t := &task{
		id:      newID(),
		url:     u.String(),
		dir:     dir,
		conns:   conns,
		status:  StatusQueued,
		created: time.Now(),
		logger:  m.opts.Logger,
		done:    make(chan struct{}),
	}
	t.ctx, t.cancel = context.WithCancel(context.Background())

	m.mu.Lock()
	m.tasks[t.id] = t
	m.order = append(m.order, t.id)
	m.mu.Unlock()

	select {
	case m.kick <- struct{}{}:
	default:
	}
	return t.view(), nil
}

// Snapshot 返回全部任务的当前快照，新的在前。
func (m *Manager) Snapshot() []TaskView {
	m.mu.Lock()
	ids := make([]string, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		ids = append(ids, m.order[i])
	}
	tasks := make([]*task, 0, len(ids))
	for _, id := range ids {
		if t, ok := m.tasks[id]; ok {
			tasks = append(tasks, t)
		}
	}
	m.mu.Unlock()

	out := make([]TaskView, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.view())
	}
	return out
}

// Cancel 取消一个任务。已完成的任务取消无效（返回 nil，不改状态）。
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	t, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	t.cancel()
	return nil
}

// loop 是单任务调度器。
func (m *Manager) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.kick:
		}

		for {
			next := m.nextQueued()
			if next == nil {
				break
			}
			m.mu.Lock()
			m.active = next
			m.mu.Unlock()

			next.run(ctx, m.opts, m.opts.Pool)

			m.mu.Lock()
			m.active = nil
			m.mu.Unlock()
		}
	}
}

func (m *Manager) nextQueued() *task {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.order {
		t, ok := m.tasks[id]
		if !ok {
			continue
		}
		if t.getStatus() == StatusQueued {
			return t
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 任务
// ---------------------------------------------------------------------------

type chunk struct {
	idx   int
	start int64
	end   int64 // 含
	// entryID / ip 是当前分片钉在哪个源上。运行期会变（重试换 IP），
	// 所以要有锁；bytes 用 atomic，读的时候不需要拿锁。
	mu      sync.Mutex
	entryID string
	ip      string
	bytes   int64
	done    bool
	err     string
}

func (c *chunk) setEntry(e contract.IPPoolEntry) {
	c.mu.Lock()
	c.entryID, c.ip = e.ID, e.SourceIP
	c.mu.Unlock()
}

func (c *chunk) view() ChannelView {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ChannelView{
		Index: c.idx,
		ID:    c.entryID,
		IP:    c.ip,
		Start: c.start,
		End:   c.end,
		Bytes: atomic.LoadInt64(&c.bytes),
		Done:  c.done,
		Error: c.err,
	}
}

type task struct {
	id    string
	url   string
	dir   string
	conns int
	// effConns 是实际分片数（探测与 IP 数决定），在 download() 里写入。
	effConns  int
	created   time.Time
	startedAt time.Time
	logger    *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // 任务进入终态时关闭

	mu       sync.Mutex
	status   string
	file     string
	path     string
	total    int64
	doneByte int64
	speed    float64
	elapsed  float64
	errMsg   string
	chunks   []*chunk
	lastAt   time.Time
	lastByte int64
}

func (t *task) getStatus() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

func (t *task) setStatus(s string) {
	t.mu.Lock()
	t.status = s
	t.mu.Unlock()
}

// view 生成一份可直接 JSON 化的快照。
func (t *task) view() TaskView {
	t.mu.Lock()
	v := TaskView{
		ID:                   t.id,
		URL:                  t.url,
		File:                 t.file,
		SavePath:             t.path,
		Status:               t.status,
		TotalBytes:           t.total,
		DoneBytes:            t.doneByte,
		SpeedBps:             t.speed,
		ElapsedSecs:          t.elapsed,
		Error:                t.errMsg,
		Connections:          t.conns,
		EffectiveConnections: t.effConns,
		CreatedAt:            t.created,
	}
	chunks := t.chunks
	t.mu.Unlock()

	if len(chunks) > 0 {
		v.Channels = make([]ChannelView, 0, len(chunks))
		for _, c := range chunks {
			v.Channels = append(v.Channels, c.view())
		}
	}
	return v
}

// run 执行一次下载，直到终态。
func (t *task) run(parent context.Context, opts Options, pool *ippool.Pool) {
	// 父 context 取消（进程退出）时终止任务。
	stop := context.AfterFunc(parent, t.cancel)
	defer stop()
	defer close(t.done)

	started := time.Now()
	t.mu.Lock()
	t.status = StatusRunning
	t.startedAt = started
	t.mu.Unlock()

	err := t.download(t.ctx, opts, pool)
	if err == nil {
		t.finish(StatusDone, "", time.Since(started))
		return
	}
	switch {
	case errors.Is(err, context.Canceled):
		t.finish(StatusCanceled, "已取消", time.Since(started))
		// 半成品留着会让人以为下载成功了：文件长度对、内容是空洞。
		if p := t.savePath(); p != "" {
			_ = os.Remove(p)
		}
	default:
		t.finish(StatusFailed, err.Error(), time.Since(started))
		if p := t.savePath(); p != "" {
			_ = os.Remove(p)
		}
	}
}

func (t *task) savePath() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.path
}

func (t *task) finish(status, msg string, elapsed time.Duration) {
	t.mu.Lock()
	t.status = status
	t.errMsg = msg
	t.elapsed = elapsed.Seconds()
	t.speed = 0
	if status == StatusDone && t.total > 0 {
		t.doneByte = t.total
	}
	t.mu.Unlock()

	lvl := slog.LevelInfo
	if status == StatusFailed {
		lvl = slog.LevelWarn
	}
	t.logger.Log(context.Background(), lvl, "加速下载结束",
		"id", t.id, "status", status, "file", t.file,
		"bytes", t.doneByte, "secs", fmt.Sprintf("%.2f", elapsed.Seconds()), "err", msg)
}

// ---------------------------------------------------------------------------
// 对外视图
// ---------------------------------------------------------------------------

// ChannelView 是一条链路（一个分片）的贡献。UI 用它画"每 IP 各扛多少"。
type ChannelView struct {
	Index int    `json:"index"`
	ID    string `json:"id"`
	IP    string `json:"ip"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Bytes int64  `json:"bytes"`
	Done  bool   `json:"done"`
	Error string `json:"error,omitempty"`
}

// TaskView 是单任务快照。
type TaskView struct {
	ID          string  `json:"id"`
	URL         string  `json:"url"`
	File        string  `json:"file,omitempty"`
	SavePath    string  `json:"save_path,omitempty"`
	Status      string  `json:"status"`
	TotalBytes  int64   `json:"total_bytes"`
	DoneBytes   int64   `json:"done_bytes"`
	SpeedBps    float64 `json:"speed_bps"`
	ElapsedSecs float64 `json:"elapsed_seconds"`
	Connections int     `json:"connections"`
	// EffectiveConnections 是这次真正建立的分片数。
	//
	// 受"在线源 IP 数 × 每 IP 允许的分片数"与 MaxConnections 限制，可能小于
	// 请求的 Connections。界面靠它解释"选了 8 片，为什么只有 4 条在跑"。
	EffectiveConnections int           `json:"effective_connections"`
	Channels             []ChannelView `json:"channels,omitempty"`
	Error                string        `json:"error,omitempty"`
	CreatedAt            time.Time     `json:"created_at"`
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func normalizeURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("请填写下载地址")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("地址格式不对: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("只支持 http / https 直链，当前是 %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("地址里没有主机名")
	}
	return u, nil
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func defaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	// 优先用系统的"下载"目录；没有就自己建一个，免得用户找不到文件。
	for _, name := range []string{"Downloads", "下载"} {
		p := filepath.Join(home, name)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return filepath.Join(p, "caulink")
		}
	}
	return filepath.Join(home, "caulink-downloads")
}

// entryHostPort 把 URL 变成 dialer 要的 "host:port"。
func entryHostPort(u *url.URL, defaultPort int) string {
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = fmt.Sprint(defaultPort)
	}
	return net.JoinHostPort(host, port)
}
