package accel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/metrics"
	"github.com/Br1m2y/caulink/internal/proxy/dialer"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// httpClientFor 为一条源 IP 造一个一次性的 HTTP 客户端。
//
// 每条连接都用自己的 Transport：ForceAttemptHTTP2=false 让每个请求独占一条 TCP
// （HTTP/2 会在一条连接上多路复用，等于又回到"一个 IP 一份额度"）；
// DisableKeepAlives 避免分片之间复用连接，否则两个分片可能挤在同一块网卡上。
func httpClientFor(d dialer.Dialer, e contract.IPPoolEntry) *http.Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return d.DialContext(ctx, network, addr, e)
		},
		ForceAttemptHTTP2:  false,
		DisableCompression: true,
		DisableKeepAlives:  true,
		MaxIdleConns:       1,
		IdleConnTimeout:    10 * time.Second,
	}
	return &http.Client{Transport: tr}
}

// defaultPort 按 scheme 给出默认端口。
func defaultPort(scheme string) int {
	if scheme == "https" {
		return 443
	}
	return 80
}

// userAgent 用工具自己的名字。
//
// 有些镜像站按 UA 拦爬虫（缺 UA 直接 403），但伪装成浏览器并不必要：
// 带上一个明确的工具标识既能过，也让对方管理员看得懂这是谁在下载。
const userAgent = "caulink-accel/0.1.0"

// errFatal 标记"重试也没用"的错误（权限、文件在变、站点不支持分段）。
var errFatal = errors.New("accel: 不可重试")

type probeResult struct {
	total     int64
	filename  string
	etag      string
	lastMod   string
	acceptRng bool
}

// download 是任务主体：探测 → 分片 → 并发抓 → 校验。
func (t *task) download(ctx context.Context, opts Options, pool *ippool.Pool) error {
	entries := pool.Schedulable()
	if len(entries) == 0 {
		return errors.New("没有可调度的源 IP：请先在界面上登录校园网，等认证状态变成「在线」")
	}

	u, err := url.Parse(t.url)
	if err != nil {
		return err
	}
	target := entryHostPort(u, defaultPort(u.Scheme))

	probe, err := t.probe(ctx, opts, entries[0], target)
	if err != nil {
		return err
	}
	if probe.total <= 0 {
		return fmt.Errorf("%w: 服务器没有给出文件长度（可能是网页而不是文件直链）", errFatal)
	}

	// 服务器不支持 Range 时退回单流：能下，但不会有加速。
	// 硬拆会拼出坏文件，那比慢严重得多。
	conns := t.conns
	if !probe.acceptRng {
		conns = 1
	}
	// 分片数上限：每个在线源 IP 最多 16 条连接。
	//
	// 依据：不少下载站点按**每连接**限速，而不是每 IP 限速。实测同一台机器上
	// 单连接 1.19 MB/s、同 IP 4 条 1.72 MB/s、8 条 1.92 MB/s。
	// 实测另一处更极端：某游戏客户端 CDN 每连接只有约 190 KB/s，
	// 8 条连接（两块网卡各 4 条）合计才 1.5 MB/s，远低于校园网单 IP 的 6 MB/s 上限。
	// 这些场景下连接数就是唯一的带宽来源，所以上限放到 8 条/IP
	// （两块网卡时等于 16 条），仍受 MaxConnections 与在线 IP 数约束。
	if maxByIP := len(entries) * 16; conns > maxByIP {
		conns = maxByIP
	}

	plans := planChunks(probe.total, conns)
	if len(plans) == 0 {
		return fmt.Errorf("%w: 文件太小或分片参数非法", errFatal)
	}

	// 实际分片数可能与请求不同（在线 IP 少、文件小、或服务器不支持 Range），
	// 记下来让界面能解释，而不是让用户以为"选了 8 片却没生效"。
	t.mu.Lock()
	t.effConns = len(plans)
	t.mu.Unlock()
	if len(plans) != t.conns {
		t.logger.Info("分片数已按在线源与文件大小调整",
			"requested", t.conns, "effective", len(plans), "entries", len(entries))
	}

	path, name, err := prepareFile(t.dir, probe.filename)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("打开 %s 失败: %w", path, err)
	}
	defer f.Close()

	// 预分配：先 Truncate 到总长，写的时候各自 WriteAt 到自己的区间。
	// 不预分配的话，任何一个分片先写完都会把文件长度顶到自己的末尾，
	// 中途崩溃留下的就是"长度像下完了、内容全是空洞"的坏文件。
	if err := f.Truncate(probe.total); err != nil {
		return fmt.Errorf("预分配文件失败（磁盘空间不足？）: %w", err)
	}

	t.mu.Lock()
	t.file, t.path, t.total = name, path, probe.total
	chunks := make([]*chunk, len(plans))
	for i, p := range plans {
		chunks[i] = &chunk{idx: i, start: p.start, end: p.end}
	}
	t.chunks = chunks
	t.lastAt = time.Now()
	t.mu.Unlock()

	stopSpeed := t.trackSpeed()
	defer stopSpeed()

	var wg sync.WaitGroup
	errCh := make(chan error, len(chunks))

	for _, c := range chunks {
		wg.Add(1)
		go func(c *chunk) {
			defer wg.Done()
			if err := t.fetchChunk(ctx, opts, c, probe, target, entries, f); err != nil {
				errCh <- err
			}
		}(c)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return err
		}
	}

	// 长度校验：分片区间是算出来的，理论上一定对得上；
	// 但磁盘写满时 WriteAt 会静默少写，这一行是最后一道闸。
	if st, err := f.Stat(); err != nil {
		return err
	} else if st.Size() != probe.total {
		return fmt.Errorf("文件长度异常：期望 %d，实际 %d", probe.total, st.Size())
	}

	t.mu.Lock()
	t.doneByte = probe.total
	t.mu.Unlock()
	return nil
}

// probe 用一次 Range 请求问出三件事：总长、是否支持分段、文件名与校验标识。
func (t *task) probe(ctx context.Context, opts Options, e contract.IPPoolEntry, target string) (probeResult, error) {
	client := httpClientFor(opts.Dialer, e)
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.url, nil)
	if err != nil {
		return probeResult{}, err
	}
	// 只取第 0 个字节：验证 Range 支持，又不浪费流量。
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return probeResult{}, fmt.Errorf("连接失败: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	out := probeResult{
		filename: filenameFrom(resp, t.url),
		etag:     resp.Header.Get("ETag"),
		lastMod:  resp.Header.Get("Last-Modified"),
	}

	switch resp.StatusCode {
	case http.StatusPartialContent:
		total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range"))
		if !ok {
			return probeResult{}, fmt.Errorf("%w: 服务器返回了 206 但 Content-Range 无法解析", errFatal)
		}
		out.total, out.acceptRng = total, true
		return out, nil

	case http.StatusOK:
		// 服务器忽略了 Range。两种可能：不支持分段，或者这是一个不支持
		// Range 的动态响应。都按单流处理。
		out.total = resp.ContentLength
		out.acceptRng = false
		return out, nil

	case http.StatusRequestedRangeNotSatisfiable:
		return probeResult{}, fmt.Errorf("%w: 服务器拒绝了 Range 请求（416），该地址不支持分段下载", errFatal)

	case http.StatusUnauthorized, http.StatusForbidden:
		return probeResult{}, fmt.Errorf("%w: 站点拒绝访问（HTTP %d）。这类地址通常需要登录态，请换一个直链",
			errFatal, resp.StatusCode)

	default:
		return probeResult{}, fmt.Errorf("%w: 探测失败，HTTP %d", errFatal, resp.StatusCode)
	}
}

// fetchChunk 抓一个区间，失败时换源 IP 重试。
func (t *task) fetchChunk(ctx context.Context, opts Options, c *chunk, probe probeResult,
	target string, entries []contract.IPPoolEntry, f *os.File) error {

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		// 每次尝试都换下一个条目：这就是"换 IP 再试"，
		// 而不是在同一个已经出问题的线路上死磕。
		e := entries[(c.idx+attempt)%len(entries)]
		c.setEntry(e)

		err := t.fetchOnce(ctx, opts, c, probe, e, f)
		if err == nil {
			c.mu.Lock()
			c.done = true
			c.mu.Unlock()
			return nil
		}
		if errors.Is(err, errFatal) {
			c.fail(err)
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = err
		c.fail(err)
		t.logger.Warn("分片下载失败，换源重试",
			"task", t.id, "chunk", c.idx, "entry", e.ID, "attempt", attempt+1, "err", err)
	}
	return fmt.Errorf("分片 %d（字节 %d-%d）重试 %d 次仍失败: %w",
		c.idx, c.start, c.end, maxAttempts, lastErr)
}

// fetchOnce 从指定条目抓一次，从"已经写好的位置"继续，避免重试时重复计数。
func (t *task) fetchOnce(ctx context.Context, opts Options, c *chunk, probe probeResult,
	e contract.IPPoolEntry, f *os.File) error {

	from := c.start + atomic.LoadInt64(&c.bytes)
	if from > c.end {
		return nil // 已经抓完了（上一次其实成功但返回了错误）
	}

	client := httpClientFor(opts.Dialer, e)
	defer client.CloseIdleConnections()

	if opts.Metrics != nil {
		opts.Metrics.ConnOpened(e.ID)
		defer opts.Metrics.ConnClosed(e.ID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, c.end))
	req.Header.Set("User-Agent", userAgent)
	// If-Range 是正确性的关键：文件在下载期间被替换时，服务器会回 200 整份文件，
	// 而不是把新内容的一段拼到旧内容的后面 —— 那种坏文件没有任何校验能发现。
	if rng := ifRange(probe); rng != "" {
		req.Header.Set("If-Range", rng)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("建连失败: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		// 只有在"整份文件就是一个分片"时 200 才合法；
		// 否则说明服务器忽略了 Range，硬拼会得到重复内容。
		if size := c.end - c.start + 1; from != c.start || size != probe.total {
			return fmt.Errorf("%w: 服务器忽略了 Range（返回 200），无法分段", errFatal)
		}
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: 站点拒绝访问（HTTP %d）", errFatal, resp.StatusCode)
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// 写盘用 OffsetWriter：多个 goroutine 各写各的区间，不需要锁文件。
	w := io.NewOffsetWriter(f, from)
	n, err := io.Copy(w, &meter{r: resp.Body, c: c, t: t, id: e.ID, m: opts.Metrics})
	if err != nil {
		return err
	}
	if from+int64(n) != c.end+1 {
		return fmt.Errorf("数据不完整：收到 %d 字节，期望 %d", n, c.end-from+1)
	}
	return nil
}

// meter 边读边记：既算分片与任务进度，也喂给面板的流量统计。
type meter struct {
	r  io.Reader
	c  *chunk
	t  *task
	id string
	m  *metrics.Registry
}

func (m *meter) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	if n > 0 {
		atomic.AddInt64(&m.c.bytes, int64(n))
		m.t.mu.Lock()
		m.t.doneByte += int64(n)
		m.t.mu.Unlock()
		if m.m != nil {
			m.m.AddDown(m.id, int64(n))
		}
	}
	return n, err
}

// trackSpeed 每 500ms 采一次进度，算瞬时速度（EWMA 平滑）。
func (t *task) trackSpeed() func() {
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(500 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-tk.C:
				t.mu.Lock()
				delta := t.doneByte - t.lastByte
				dt := now.Sub(t.lastAt).Seconds()
				if dt > 0 {
					inst := float64(delta) / dt
					// 0.4 的系数：单次抖动不该让数字乱跳，但掉速要立刻看得出来。
					t.speed = t.speed*0.6 + inst*0.4
				}
				t.lastByte, t.lastAt = t.doneByte, now
				// 排队时间不算进用时：用户关心的是"下载花了多久"。
				if !t.startedAt.IsZero() {
					t.elapsed = now.Sub(t.startedAt).Seconds()
				}
				t.mu.Unlock()
			}
		}
	}()
	return func() { close(done) }
}

func (c *chunk) fail(err error) {
	c.mu.Lock()
	c.err = err.Error()
	c.mu.Unlock()
}

// ---------------------------------------------------------------------------
// 分片规划与文件名
// ---------------------------------------------------------------------------

type plan struct {
	start int64
	end   int64
}

// planChunks 把 [0,total) 切成最多 conns 段，返回的区间首尾相接、不重叠。
func planChunks(total int64, conns int) []plan {
	if total <= 0 || conns <= 0 {
		return nil
	}
	if conns == 1 || total <= minChunkSize {
		return []plan{{0, total - 1}}
	}
	size := (total + int64(conns) - 1) / int64(conns)
	if size < minChunkSize {
		size = minChunkSize
	}

	var out []plan
	for start := int64(0); start < total; start += size {
		end := start + size - 1
		if end >= total {
			end = total - 1
		}
		out = append(out, plan{start, end})
		if len(out) == conns {
			// 收尾：把剩下的都塞进最后一片，保证覆盖完整。
			if end < total-1 {
				out[len(out)-1].end = total - 1
			}
			break
		}
	}
	return out
}

var (
	// 文件名里这些字符在 Windows 上非法，替换掉而不是报错。
	illegalName = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)
	rangeRe     = regexp.MustCompile(`^bytes\s+(\d+)-(\d+)/(\d+|\*)$`)
)

func parseContentRangeTotal(v string) (int64, bool) {
	m := rangeRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, false
	}
	total := m[3]
	if total == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(total, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// ifRange 选一个稳定的校验标识。
//
// 优先 ETag：Last-Modified 只有秒级精度，同一秒内替换的文件会被误判成没变。
func ifRange(p probeResult) string {
	if p.etag != "" && !strings.HasPrefix(p.etag, "W/") {
		return p.etag
	}
	return p.lastMod
}

func filenameFrom(resp *http.Response, rawURL string) string {
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if v := params["filename"]; v != "" {
				return sanitizeName(v)
			}
		}
	}
	if u, err := url.Parse(rawURL); err == nil {
		base := pathBase(u.Path)
		if base != "" {
			return sanitizeName(base)
		}
	}
	return "download.bin"
}

func pathBase(p string) string {
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	return p
}

func sanitizeName(s string) string {
	s = illegalName.ReplaceAllString(s, "_")
	s = strings.TrimSpace(strings.Trim(s, "."))
	if s == "" || s == "_" {
		return "download.bin"
	}
	// 太长的文件名在 Windows 上会被截断成同名文件，留点余量。
	if len(s) > 120 {
		ext := filepath.Ext(s)
		if len(ext) > 12 {
			ext = ""
		}
		s = s[:120-len(ext)] + ext
	}
	return s
}

// prepareFile 决定落盘路径，已存在时自动加序号，绝不覆盖用户的文件。
func prepareFile(dir, name string) (path, finalName string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("创建目录 %s 失败: %w", dir, err)
	}
	base := sanitizeName(name)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	candidate := filepath.Join(dir, base)
	for i := 1; i < 1000; i++ {
		if _, statErr := os.Stat(candidate); os.IsNotExist(statErr) {
			return candidate, filepath.Base(candidate), nil
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
	return "", "", fmt.Errorf("目录 %s 里同名文件太多", dir)
}
