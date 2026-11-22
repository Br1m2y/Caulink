package accel

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/metrics"
	"github.com/Br1m2y/caulink/internal/proxy/dialer"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

func TestPlanChunks(t *testing.T) {
	cases := []struct {
		total int64
		conns int
		want  int
	}{
		{0, 8, 0},
		{1024, 8, 1},           // 太小，不值得拆
		{8 << 20, 1, 1},        // 用户只要单流
		{8 << 20, 4, 4},        // 正常拆 4 片
		{20<<20 + 12345, 4, 4}, // 不整除
		{100 << 20, 16, 16},    // 大文件拆满
	}

	for _, c := range cases {
		plans := planChunks(c.total, c.conns)
		if len(plans) != c.want {
			t.Errorf("planChunks(%d,%d) 片数 = %d，期望 %d", c.total, c.conns, len(plans), c.want)
			continue
		}
		if c.want == 0 {
			continue
		}
		if plans[0].start != 0 || plans[len(plans)-1].end != c.total-1 {
			t.Errorf("覆盖区间不对: 首 %d 尾 %d，总长 %d",
				plans[0].start, plans[len(plans)-1].end, c.total)
		}
		for i := 1; i < len(plans); i++ {
			if plans[i].start != plans[i-1].end+1 {
				t.Fatalf("分片 %d 与前一片不连续: %d vs %d", i, plans[i].start, plans[i-1].end)
			}
		}
	}
}

func TestParseContentRangeTotal(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"bytes 0-0/12345", 12345, true},
		{"bytes 1024-2047/4096", 4096, true},
		{"bytes 0-0/*", 0, false},
		{"", 0, false},
		{"items 0-1/10", 0, false},
		{"bytes 0-0/0", 0, false},
	}
	for _, c := range cases {
		got, ok := parseContentRangeTotal(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseContentRangeTotal(%q) = (%d,%v)，期望 (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		`a/b:c*d?e"f<g>h|i.bin`: "a_b_c_d_e_f_g_h_i.bin",
		"  ../etc/passwd  ":     ".._etc_passwd",
		"":                      "download.bin",
		"...":                   "download.bin",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestPrepareFileDedupe(t *testing.T) {
	dir := t.TempDir()

	p1, n1, err := prepareFile(dir, "a.iso")
	if err != nil || n1 != "a.iso" {
		t.Fatalf("首次落盘异常: %v / %s", err, n1)
	}
	if err := os.WriteFile(p1, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, n2, err := prepareFile(dir, "a.iso")
	if err != nil {
		t.Fatal(err)
	}
	if n2 != "a (1).iso" {
		t.Fatalf("重名时应加序号，实际 %q", n2)
	}
}

// 一个支持 Range 的测试服务器，行为尽量贴近真实镜像站。
func rangeServer(t *testing.T, data []byte, acceptRange bool) *httptest.Server {
	t.Helper()
	mod := time.Now().Add(-time.Hour)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptRange {
			// 模拟"忽略 Range"的服务器：直接整份返回。
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			_, _ = w.Write(data)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "file.bin", mod, bytes.NewReader(data))
	}))
}

func testPool(t *testing.T, n int) *ippool.Pool {
	t.Helper()
	pool := ippool.New()
	for i := 0; i < n; i++ {
		e := contract.IPPoolEntry{
			// ID 里带序号：同一块网卡也可以有多条源地址条目，
			// 测试里用它模拟"两条链路"，不必真去插网卡。
			ID:          fmt.Sprintf("lo%d@127.0.0.1", i),
			Interface:   "lo",
			SourceIP:    "127.0.0.1",
			AuthStatus:  contract.AuthOnline,
			HealthScore: 1,
			LastSeen:    time.Now().UTC(),
		}
		if err := pool.Upsert(e); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func waitTask(t *testing.T, m *Manager, id string) TaskView {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, v := range m.Snapshot() {
			if v.ID != id {
				continue
			}
			if v.Status == StatusDone || v.Status == StatusFailed || v.Status == StatusCanceled {
				return v
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("任务 %s 超时未结束", id)
	return TaskView{}
}

// 端到端：4 MiB 分片下载，拼接结果必须与源文件逐字节相同。
func TestDownloadParallel(t *testing.T) {
	data := make([]byte, 4<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	srv := rangeServer(t, data, true)
	defer srv.Close()

	dir := t.TempDir()
	m, err := NewManager(context.Background(), Options{
		Pool:       testPool(t, 2), // 两条"链路" → 最多 4 片
		Dialer:     dialer.Dialer{Timeout: 5 * time.Second},
		Metrics:    metrics.New(),
		DefaultDir: dir,
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatal(err)
	}

	v, err := m.Start(Request{URL: srv.URL + "/file.bin", Connections: 4})
	if err != nil {
		t.Fatal(err)
	}
	done := waitTask(t, m, v.ID)
	if done.Status != StatusDone {
		t.Fatalf("任务失败: %s / %s", done.Status, done.Error)
	}
	if done.DoneBytes != int64(len(data)) {
		t.Fatalf("完成字节 %d，期望 %d", done.DoneBytes, len(data))
	}

	got, err := os.ReadFile(done.SavePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("下载内容与源文件不一致")
	}

	// 分片必须真的铺开在两条链路上，否则"多 IP"只是名义上的。
	if len(done.Channels) < 2 {
		t.Fatalf("只开了 %d 个分片", len(done.Channels))
	}
	used := map[string]int64{}
	for _, c := range done.Channels {
		if c.Bytes == 0 {
			t.Errorf("分片 %d（%s）一个字节都没搬", c.Index, c.ID)
		}
		used[c.ID] += c.Bytes
		if !c.Done {
			t.Errorf("分片 %d 未完成", c.Index)
		}
	}
	if len(used) < 2 {
		t.Fatalf("分片没有落到不同条目上: %v", used)
	}
}

// 服务器忽略 Range 时必须退回单流，而不是硬拆出坏文件。
func TestDownloadNoRangeSupport(t *testing.T) {
	data := make([]byte, 3<<20)
	for i := range data {
		data[i] = byte(i % 251)
	}
	srv := rangeServer(t, data, false)
	defer srv.Close()

	m, err := NewManager(context.Background(), Options{
		Pool:       testPool(t, 2),
		Dialer:     dialer.Dialer{Timeout: 5 * time.Second},
		DefaultDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	v, err := m.Start(Request{URL: srv.URL + "/file.bin", Connections: 8})
	if err != nil {
		t.Fatal(err)
	}
	done := waitTask(t, m, v.ID)
	if done.Status != StatusDone {
		t.Fatalf("任务失败: %s / %s", done.Status, done.Error)
	}
	if len(done.Channels) != 1 {
		t.Fatalf("不支持 Range 时应单流，实际 %d 片", len(done.Channels))
	}
	got, err := os.ReadFile(done.SavePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("单流下载内容不一致")
	}
}

// 取消后必须停手，并且不留下一个"长度像下完、内容有空洞"的半成品。
func TestCancelRemovesPartial(t *testing.T) {
	data := make([]byte, 8<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
			w.WriteHeader(http.StatusPartialContent)
		}
		// 慢速吐数据：每 20ms 才给 32 KiB，留出取消的窗口。
		// 客户端一断开，下一次 Write 就会失败，这里自然收尾。
		for off := 0; off < len(data); off += 32 << 10 {
			end := off + (32 << 10)
			if end > len(data) {
				end = len(data)
			}
			if _, err := w.Write(data[off:end]); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	m, err := NewManager(context.Background(), Options{
		Pool:       testPool(t, 1),
		Dialer:     dialer.Dialer{Timeout: 5 * time.Second},
		DefaultDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	v, err := m.Start(Request{URL: srv.URL + "/slow.bin"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := m.Cancel(v.ID); err != nil {
		t.Fatal(err)
	}
	done := waitTask(t, m, v.ID)
	if done.Status != StatusCanceled {
		t.Fatalf("状态应为 canceled，实际 %s（%s）", done.Status, done.Error)
	}
	if _, err := os.Stat(done.SavePath); !os.IsNotExist(err) {
		t.Fatalf("取消后不应留下文件: %v", err)
	}
}

func TestStartRejectsBadURL(t *testing.T) {
	m, err := NewManager(context.Background(), Options{
		Pool:   testPool(t, 1),
		Dialer: dialer.Dialer{Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "ftp://x/y", "not a url", "https://"} {
		if _, err := m.Start(Request{URL: bad}); err == nil {
			t.Errorf("URL %q 应被拒绝", bad)
		}
	}
}
