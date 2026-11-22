package httpproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestUpstreamMatches(t *testing.T) {
	up := Upstream{
		Proxy: "http://127.0.0.1:17891",
		Domains: []string{
			"steamcommunity.com",
			"*.steampowered.com",
			".steamstatic.com",
		},
	}

	cases := []struct {
		host string
		want bool
	}{
		{"steamcommunity.com", true},
		{"www.steamcommunity.com", false}, // 精确规则不覆盖子域
		{"store.steampowered.com", true},  // *.steampowered.com
		{"steampowered.com", false},       // *. 不匹配自身
		{"cdn.steamstatic.com", true},     // .steamstatic.com 含子域
		{"steamstatic.com", true},         // . 也匹配自身
		{"dl.steam.clngaa.com", false},    // 下载 CDN 不该走上游
		{"example.com", false},
	}
	for _, c := range cases {
		if got := up.Matches(c.host); got != c.want {
			t.Errorf("Matches(%q) = %v，期望 %v", c.host, got, c.want)
		}
	}
}

func TestUpstreamMatchesEmptyDomainsMeansAll(t *testing.T) {
	up := Upstream{Proxy: "http://127.0.0.1:1"}
	if !up.Matches("anything.example") {
		t.Error("Domains 为空时应匹配全部")
	}
	if (Upstream{}).Matches("anything.example") {
		t.Error("没有配置 Proxy 时不应匹配")
	}
}

func TestUpstreamValidate(t *testing.T) {
	if err := (Upstream{}).Validate(); err == nil {
		t.Error("空 proxy 应报错")
	}
	if err := (Upstream{Proxy: "http://127.0.0.1:17891"}).Validate(); err != nil {
		t.Errorf("合法配置不应报错: %v", err)
	}
}

// fakeUpstreamProxy 起一个只会处理 CONNECT 的上游代理，转发到 target。
func fakeUpstreamProxy(t *testing.T, target string) (addr string, stop func(), hits *int32Counter) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hits = &int32Counter{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				hits.add(1)
				up, err := net.Dial("tcp", target)
				if err != nil {
					fmt.Fprint(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer up.Close()
				fmt.Fprint(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
				go func() { _, _ = io.Copy(up, br) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }, hits
}

type int32Counter struct{ v int }

func (c *int32Counter) add(n int) { c.v += n }
func (c *int32Counter) get() int  { return c.v }

// 命中规则的 CONNECT 必须走上游，且**不**经过本地 dialer（不走校园网 IP）。
func TestConnectUsesUpstreamWhenMatched(t *testing.T) {
	echo, stopEcho := echoServer(t)
	defer stopEcho()

	upAddr, stopUp, hits := fakeUpstreamProxy(t, echo)
	defer stopUp()

	dialerCalled := false
	dialer := fakeDialer{dial: func(ctx context.Context, address string) (net.Conn, error) {
		dialerCalled = true
		return (&net.Dialer{}).DialContext(ctx, "tcp", echo)
	}}

	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Picker:   fakePicker{},
		Dialer:   dialer,
		Upstream: &Upstream{Proxy: "http://" + upAddr, Domains: []string{"blocked.test"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Serve(ctx, proxyLn) }()

	conn, err := net.DialTimeout("tcp", proxyLn.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT blocked.test:443 HTTP/1.1\r\nHost: blocked.test:443\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("读 CONNECT 响应失败: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT 应返回 200，得到 %d", resp.StatusCode)
	}

	// 隧道通了：发一段数据应被 echo 回来。
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("隧道数据回显失败: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("回显内容不对: %q", buf)
	}

	if hits.get() != 1 {
		t.Errorf("上游应被调用 1 次，实际 %d", hits.get())
	}
	if dialerCalled {
		t.Error("命中上游的请求不应该经过本地源 IP 拨号")
	}
}

// 未命中规则的 CONNECT 仍走多源 IP（不能因为配了上游就全走 VPN）。
func TestConnectUsesLocalDialerWhenNotMatched(t *testing.T) {
	echo, stopEcho := echoServer(t)
	defer stopEcho()

	used := false
	dialer := fakeDialer{dial: func(ctx context.Context, address string) (net.Conn, error) {
		used = true
		return (&net.Dialer{}).DialContext(ctx, "tcp", echo)
	}}

	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Picker:   fakePicker{},
		Dialer:   dialer,
		Upstream: &Upstream{Proxy: "http://127.0.0.1:1", Domains: []string{"blocked.test"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Serve(ctx, proxyLn) }()

	conn, err := net.DialTimeout("tcp", proxyLn.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT cdn.test:443 HTTP/1.1\r\nHost: cdn.test:443\r\n\r\n")
	br := bufio.NewReader(conn)
	if _, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect}); err != nil {
		t.Fatalf("读 CONNECT 响应失败: %v", err)
	}
	if !used {
		t.Error("未命中规则的请求应走本地源 IP 拨号")
	}
}
