package control

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/metrics"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

type fakeSticky struct{ id string }

func (f fakeSticky) StickyID() string { return f.id }

func startStatus(t *testing.T, s *StatusServer) (string, func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Listen = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	return ln.Addr().String(), func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("状态服务未及时退出")
		}
	}
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q，轮询接口不能缓存", cc)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v\n%s", err, body)
	}
	return out
}

func TestStatusReportsPoolAndMetrics(t *testing.T) {
	pool := ippool.New()
	e := contract.IPPoolEntry{
		ID: "eth0@10.0.0.2", Interface: "eth0", SourceIP: "10.0.0.2",
		AuthStatus: contract.AuthOnline, HealthScore: 0.85, RTTMs: 21.5,
		LastSeen: time.Now().UTC(),
	}
	if err := pool.Upsert(e); err != nil {
		t.Fatal(err)
	}

	m := metrics.New()
	m.ConnOpened(e.ID)
	m.AddUp(e.ID, 1500)
	m.AddDown(e.ID, 9000)

	s := &StatusServer{
		Pool:       pool,
		Metrics:    m,
		Scheduler:  fakeSticky{id: e.ID},
		Socks5Addr: "127.0.0.1:1080",
		Version:    "0.1.0-test",
	}

	addr, stop := startStatus(t, s)
	defer stop()

	got := getJSON(t, "http://"+addr+"/api/v1/status")

	if got["version"] != "0.1.0-test" {
		t.Errorf("version = %v", got["version"])
	}
	if got["socks5"] != "127.0.0.1:1080" {
		t.Errorf("socks5 = %v", got["socks5"])
	}
	if up, ok := got["uptime_seconds"].(float64); !ok || up < 0 {
		t.Errorf("uptime_seconds = %v", got["uptime_seconds"])
	}

	ips, ok := got["ips"].([]any)
	if !ok || len(ips) != 1 {
		t.Fatalf("ips 应为长度 1 的数组，实际 %v", got["ips"])
	}
	ip := ips[0].(map[string]any)

	// 内嵌的 contract.IPPoolEntry 必须展开到顶层，否则 C 的解析会全空。
	for k, want := range map[string]any{
		"id":           "eth0@10.0.0.2",
		"interface":    "eth0",
		"source_ip":    "10.0.0.2",
		"auth_status":  "online",
		"health_score": 0.85,
		"rtt_ms":       21.5,
	} {
		if ip[k] != want {
			t.Errorf("%s = %v，期望 %v", k, ip[k], want)
		}
	}

	if ip["schedulable"] != true {
		t.Errorf("schedulable = %v", ip["schedulable"])
	}
	if ip["weight"] != float64(85) {
		t.Errorf("weight = %v，期望 85", ip["weight"])
	}
	if ip["active_conns"] != float64(1) {
		t.Errorf("active_conns = %v", ip["active_conns"])
	}
	if ip["bytes_up"] != float64(1500) || ip["bytes_down"] != float64(9000) {
		t.Errorf("字节 = %v / %v", ip["bytes_up"], ip["bytes_down"])
	}

	tot := got["totals"].(map[string]any)
	if tot["ip_count"] != float64(1) || tot["schedulable_count"] != float64(1) {
		t.Errorf("totals 计数不对: %v", tot)
	}
	if tot["active_conns"] != float64(1) {
		t.Errorf("totals.active_conns = %v", tot["active_conns"])
	}

	sch := got["scheduler"].(map[string]any)
	if sch["strategy"] != "smooth-weighted-round-robin" {
		t.Errorf("strategy = %v", sch["strategy"])
	}
	if sch["sticky_id"] != e.ID {
		t.Errorf("sticky_id = %v", sch["sticky_id"])
	}
	if sch["sticky_ip"] != "10.0.0.2" {
		t.Errorf("sticky_ip = %v，应从 IP 池解出来", sch["sticky_ip"])
	}
}

// 空池必须返回空数组而不是 null，否则前端遍历数组时会抛异常。
func TestStatusEmptyPoolIsEmptyArray(t *testing.T) {
	s := &StatusServer{Pool: ippool.New()}
	addr, stop := startStatus(t, s)
	defer stop()

	resp, err := http.Get("http://" + addr + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["ips"]) != "[]" {
		t.Errorf("ips 应为 []，实际 %s", raw["ips"])
	}
}

// 没有 Metrics 时不应 panic，统计字段补零。
func TestStatusWithoutMetrics(t *testing.T) {
	pool := ippool.New()
	e := contract.IPPoolEntry{
		ID: "eth0@10.0.0.2", Interface: "eth0", SourceIP: "10.0.0.2",
		AuthStatus: contract.AuthOffline, HealthScore: 0,
		LastSeen: time.Now().UTC(),
	}
	_ = pool.Upsert(e)

	s := &StatusServer{Pool: pool}
	addr, stop := startStatus(t, s)
	defer stop()

	got := getJSON(t, "http://"+addr+"/api/v1/status")
	ip := got["ips"].([]any)[0].(map[string]any)

	if ip["schedulable"] != false {
		t.Errorf("离线条目不该可调度: %v", ip["schedulable"])
	}
	if ip["active_conns"] != float64(0) || ip["bytes_up"] != float64(0) {
		t.Errorf("无统计时应补零，得到 %v", ip)
	}
	// 没有 Scheduler 时也要给出策略名，UI 不该显示空字符串。
	if sch := got["scheduler"].(map[string]any); sch["strategy"] == "" {
		t.Error("scheduler.strategy 为空")
	}
}

func TestStatusRejectsNonGet(t *testing.T) {
	s := &StatusServer{Pool: ippool.New()}
	addr, stop := startStatus(t, s)
	defer stop()

	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/v1/status", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("状态码 = %d，期望 405", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q", allow)
	}
}

func TestHealthz(t *testing.T) {
	s := &StatusServer{Pool: ippool.New()}
	addr, stop := startStatus(t, s)
	defer stop()

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("状态码 = %d", resp.StatusCode)
	}
}

// 状态接口会暴露内网地址与会话状态，只能监听回环。
func TestRefusesNonLoopbackListen(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:7788", "192.168.1.5:7788", ":7788"} {
		s := &StatusServer{Listen: addr, Pool: ippool.New()}
		if err := s.validate(); err == nil {
			t.Errorf("监听 %s 应被拒绝", addr)
		}
	}
}

// 桌面端跑在 WebView 里，与 127.0.0.1 不同源。缺了 CORS 头，
// fetch 会被浏览器直接拦掉，而服务端连一条日志都不会有。
func TestCORSHeadersOnStatus(t *testing.T) {
	s := &StatusServer{Pool: ippool.New()}
	addr, stop := startStatus(t, s)
	defer stop()

	resp, err := http.Get("http://" + addr + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q，前端跨源请求会被拦", got)
	}
}

// 预检必须返回 204 而不是 405：浏览器看到预检失败就不会再发真正的 GET。
func TestCORSPreflight(t *testing.T) {
	s := &StatusServer{Pool: ippool.New()}
	addr, stop := startStatus(t, s)
	defer stop()

	req, _ := http.NewRequest(http.MethodOptions, "http://"+addr+"/api/v1/status", nil)
	req.Header.Set("Origin", "http://wails.localhost")
	req.Header.Set("Access-Control-Request-Method", "GET")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("预检状态码 = %d，期望 204", resp.StatusCode)
	}
	if m := resp.Header.Get("Access-Control-Allow-Methods"); m == "" {
		t.Error("预检缺少 Access-Control-Allow-Methods")
	}
}

func TestValidateRequiresPool(t *testing.T) {
	s := &StatusServer{Listen: "127.0.0.1:7788"}
	if err := s.validate(); err == nil {
		t.Error("缺少 Pool 应报错")
	}
}
