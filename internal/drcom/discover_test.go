package drcom

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Br1m2y/caulink/internal/portal"
)

// 门户按网段分流：登录发给错误节点时，门户照样返回 3.htm，但流量仍被劫持。
// 本用例模拟该场景：错误节点只回 3.htm，正确节点才会把"已认证"标志置真。
func TestLoginWithDiscoveryFallsBackToInterceptingNode(t *testing.T) {
	var authed atomic.Bool

	// 正确节点：登录后把 authed 置真。
	right := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "a=Login") {
			authed.Store(true)
			w.Header().Set("Location", "/3.htm")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer right.Close()

	// 错误节点：同样回 3.htm，但不改变 authed。
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "a=Login") {
			w.Header().Set("Location", "/3.htm")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer wrong.Close()

	// 探测目标：未认证时返回门户劫持页（200 + JS 跳转），认证后返回 204。
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authed.Load() {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<html><head><script>location.href=%q</script></head></html>", right.URL)
	}))
	defer probe.Close()

	host, port := splitHostPort(t, wrong.URL)
	c := Client{
		Portal:     host,
		Port:       port,
		Username:   "20240000000",
		Password:   "x",
		SourceIP:   "10.0.0.1",
		ProbeURL:   probe.URL,
		Timeout:    3e9,
		HTTPClient: &http.Client{},
	}

	cache := portal.Load("")
	if err := c.LoginWithDiscovery(context.Background(), cache); err != nil {
		t.Fatalf("应回退到正确节点并成功，实际: %v", err)
	}
	if !authed.Load() {
		t.Fatal("正确节点没有收到登录请求")
	}
	if got := cache.Get("10.0.0.1"); !strings.Contains(got, hostPortOfURL(t, right.URL)) {
		t.Fatalf("缓存应记录正确节点，实际 %q", got)
	}
}

// 只有错误节点时，必须报"登录未生效"，而不是假成功。
func TestLoginAndVerifyRejectsIneffectiveLogin(t *testing.T) {
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "a=Login") {
			w.Header().Set("Location", "/3.htm")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer wrong.Close()

	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>portal login</html>"))
	}))
	defer probe.Close()

	host, port := splitHostPort(t, wrong.URL)
	c := Client{
		Portal:     host,
		Port:       port,
		Username:   "20240000000",
		Password:   "x",
		SourceIP:   "10.0.0.2",
		ProbeURL:   probe.URL,
		Timeout:    3e9,
		HTTPClient: &http.Client{},
	}
	err := c.LoginAndVerify(context.Background())
	if err == nil {
		t.Fatal("登录返回 3.htm 但探测未通过时必须报错")
	}
	if !strings.Contains(err.Error(), "仍被门户劫持") {
		t.Fatalf("错误信息应说明登录未生效，实际: %v", err)
	}
}

// splitHostPort 把 httptest 的 URL 拆成 host 与 port。
func splitHostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	host, port, err := portal.SplitHostPort(strings.TrimPrefix(raw, "http://"), 801)
	if err != nil {
		t.Fatalf("解析 %q 失败: %v", raw, err)
	}
	return host, port
}

// hostPortOfURL 返回 URL 的 host:port（测试断言用）。
func hostPortOfURL(t *testing.T, raw string) string {
	t.Helper()
	return strings.TrimPrefix(raw, "http://")
}

// DiscoverPortal 应能从 200+JS 跳转的劫持页里取出节点地址。
func TestDiscoverPortalFromJSRedirect(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><script type="text/javascript">location.href="http://10.3.38.7"</script></head></html>`))
	}))
	defer probe.Close()

	c := Client{ProbeURL: probe.URL, SourceIP: "10.0.0.3", Timeout: 3e9, HTTPClient: &http.Client{}}
	if got := c.DiscoverPortal(context.Background()); got != "10.3.38.7:80" {
		t.Fatalf("期望 10.3.38.7:80，实际 %q", got)
	}
}

// 已经在线（204）时不应"发现"任何门户。
func TestDiscoverPortalEmptyWhenOnline(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer probe.Close()

	c := Client{ProbeURL: probe.URL, SourceIP: "10.0.0.4", Timeout: 3e9, HTTPClient: &http.Client{}}
	if got := c.DiscoverPortal(context.Background()); got != "" {
		t.Fatalf("在线时不应发现门户，实际 %q", got)
	}
}

// 现场抓到的真实劫持页格式（带 type 属性、JS 跳转），必须能被识别。
func TestDiscoverPortalFromRealHijackPage(t *testing.T) {
	const realPage = `<html><head><script type="text/javascript">location.href="http://10.3.38.7"</script></head><body><a href="http://10.3.38.7"></a></body></html>`
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(realPage))
	}))
	defer probe.Close()

	c := Client{ProbeURL: probe.URL, SourceIP: "10.0.0.5", Timeout: 3e9, HTTPClient: &http.Client{}}
	if got := c.DiscoverPortal(context.Background()); got != "10.3.38.7:80" {
		t.Fatalf("真实劫持页应解析出 10.3.38.7:80，实际 %q", got)
	}
}
