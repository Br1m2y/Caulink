package drcom

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// withTargets 临时替换探测目标列表，测试结束后恢复。
func withTargets(t *testing.T, targets ...string) {
	t.Helper()
	old := probeTargets
	probeTargets = targets
	t.Cleanup(func() { probeTargets = old })
}

// 一个目标不可达、下一个可用时，必须判"在线"。
// 现场就是这个形态：connect.rom.miui.com 从某个源 IP 整段超时，
// 而 msftconnecttest 正常。
func TestProbeFallsBackToNextTarget(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer good.Close()

	// 用一个已关闭的端口模拟不可达目标（连接被拒绝，快速失败）。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + ln.Addr().String() + "/generate_204"
	_ = ln.Close()

	withTargets(t, dead, good.URL)
	c := Client{SourceIP: "10.0.0.1", Timeout: 2 * time.Second, HTTPClient: &http.Client{}}
	online, err := c.Probe(context.Background())
	if err != nil || !online {
		t.Fatalf("应回退到可用目标并判在线，实际 online=%v err=%v", online, err)
	}
}

// 所有目标都不可达时必须返回错误（"未知"），不能返回 (false, nil)——
// 后者会让调用方以为"确定离线"而反复重登录。
func TestProbeAllUnreachableReturnsError(t *testing.T) {
	mk := func() string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		return "http://" + addr + "/generate_204"
	}
	withTargets(t, mk(), mk())
	c := Client{SourceIP: "10.0.0.1", Timeout: time.Second, HTTPClient: &http.Client{}}
	online, err := c.Probe(context.Background())
	if online {
		t.Fatal("不应判在线")
	}
	if err == nil {
		t.Fatal("全部不可达时必须返回错误，用于区分「未知」与「离线」")
	}
}

// 门户劫持页（200 + HTML）必须判"确定离线"。
func TestProbeHTMLMeansHijacked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><script>location.href='http://portal'</script></html>"))
	}))
	defer srv.Close()

	withTargets(t, srv.URL)
	c := Client{SourceIP: "10.0.0.1", Timeout: 2 * time.Second, HTTPClient: &http.Client{}}
	online, err := c.Probe(context.Background())
	if online || err != nil {
		t.Fatalf("HTML 劫持页应判离线且无错误，实际 online=%v err=%v", online, err)
	}
}

// 200 + 纯文本（msftconnecttest 的形态）且带预期文案时判在线。
func TestProbePlainTextWithExpectIsOnline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Microsoft Connect Test"))
	}))
	defer srv.Close()

	withTargets(t, srv.URL)
	c := Client{SourceIP: "10.0.0.1", Timeout: 2 * time.Second, HTTPClient: &http.Client{}}
	online, err := c.Probe(context.Background())
	if !online || err != nil {
		t.Fatalf("expected online, got online=%v err=%v", online, err)
	}
}
