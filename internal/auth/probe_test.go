package auth

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeOnline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Microsoft Connect Test"))
	}))
	defer srv.Close()

	r := probeOnce(context.Background(), net.ParseIP("127.0.0.1"), srv.URL, 3*time.Second)
	if r.Kind != ProbeOnline {
		t.Fatalf("期望 online，得到 %s（%s）", r.Kind, r.Reason)
	}
}

func TestProbeDetectsPortalPage(t *testing.T) {
	// 校园网门户的典型行为：任意请求返回 200 + 一整页 HTML 登录页。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>请先登录校园网</body></html>"))
	}))
	defer srv.Close()

	r := probeOnce(context.Background(), net.ParseIP("127.0.0.1"), srv.URL, 3*time.Second)
	if r.Kind != ProbeUnauthenticated {
		t.Fatalf("期望 unauthenticated，得到 %s（%s）", r.Kind, r.Reason)
	}
}

func TestProbeDetectsRedirectHijack(t *testing.T) {
	// 门户也可能把请求 302 到另一个主机的登录地址。
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer portal.Close()

	// httptest 都监听 127.0.0.1，用 localhost 制造出一个"不同主机"，
	// 否则按主机名比较永远相等，测不到跨主机劫持的分支。
	portalURL := strings.Replace(portal.URL, "127.0.0.1", "localhost", 1)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, portalURL, http.StatusFound)
	}))
	defer redirector.Close()

	r := probeOnce(context.Background(), net.ParseIP("127.0.0.1"), redirector.URL, 3*time.Second)
	if r.Kind != ProbeUnauthenticated {
		t.Fatalf("期望 unauthenticated，得到 %s（%s）", r.Kind, r.Reason)
	}
}

// 同主机内的重定向（比如路径跳转）不算劫持，否则会把正常站点误判成未认证。
func TestProbeAllowsSameHostRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("data"))
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer srv.Close()

	r := probeOnce(context.Background(), net.ParseIP("127.0.0.1"), srv.URL, 3*time.Second)
	if r.Kind != ProbeOnline {
		t.Fatalf("同主机重定向应判为 online，得到 %s（%s）", r.Kind, r.Reason)
	}
}

func TestProbeNetworkErrorWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // 关掉，连接必然失败

	r := probeOnce(context.Background(), net.ParseIP("127.0.0.1"), url, 2*time.Second)
	if r.Kind != ProbeNetworkError {
		t.Fatalf("期望 network_error，得到 %s（%s）", r.Kind, r.Reason)
	}
}

// 链路错误和认证失效必须分开：前者重认证没用，后者才需要重登。
func TestProbeDistinguishesKinds(t *testing.T) {
	if ProbeOnline.String() == ProbeUnauthenticated.String() {
		t.Fatal("三种探测结果必须有可区分的名称")
	}
	if ProbeNetworkError.String() == ProbeUnauthenticated.String() {
		t.Fatal("network_error 不能和 unauthenticated 混为一谈")
	}
}
