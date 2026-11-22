package httpproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

type fakePicker struct{ err error }

func (f fakePicker) Pick(network, addr string) (contract.IPPoolEntry, error) {
	if f.err != nil {
		return contract.IPPoolEntry{}, f.err
	}
	return contract.IPPoolEntry{ID: "eth0@10.0.0.2"}, nil
}

type fakeDialer struct {
	dial func(ctx context.Context, address string) (net.Conn, error)
}

func (f fakeDialer) DialContext(ctx context.Context, network, address string, entry contract.IPPoolEntry) (net.Conn, error) {
	return f.dial(ctx, address)
}

// echoServer 起一个把收到的字节原样回写的 TCP 服务。
func echoServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func startProxy(t *testing.T, p Picker, d Dialer) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Picker: p, Dialer: d}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	return ln.Addr().String(), func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("代理未及时退出")
		}
	}
}

func TestConnectTunnel(t *testing.T) {
	echo, stopEcho := echoServer(t)
	defer stopEcho()

	dialer := fakeDialer{dial: func(ctx context.Context, address string) (net.Conn, error) {
		// 目标地址应是 CONNECT 里的 host:port，这里忽略地址直连 echo。
		return (&net.Dialer{}).DialContext(ctx, "tcp", echo)
	}}
	proxyAddr, stop := startProxy(t, fakePicker{}, dialer)
	defer stop()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo, echo)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("读 CONNECT 响应失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT 应返回 200，得到 %d", resp.StatusCode)
	}

	// 隧道建立后，写入的字节应被 echo 回来。
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("读隧道数据失败: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("隧道数据应原样返回，得到 %q", string(buf))
	}
}

func TestForwardAbsoluteURI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 目标服务器应当收到 origin-form 的路径，而不是绝对 URI。
		if r.URL.Path != "/hello" {
			t.Errorf("目标收到的路径应为 /hello，得到 %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, "world")
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")
	dialer := fakeDialer{dial: func(ctx context.Context, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", host)
	}}
	proxyAddr, stop := startProxy(t, fakePicker{}, dialer)
	defer stop()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "GET %s/hello HTTP/1.1\r\nHost: %s\r\n\r\n", upstream.URL, host)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("转发应返回 200，得到 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "world" {
		t.Fatalf("响应体不对: %q", body)
	}
}

func TestRejectsNonAbsoluteRequest(t *testing.T) {
	proxyAddr, stop := startProxy(t, fakePicker{}, fakeDialer{dial: func(ctx context.Context, address string) (net.Conn, error) {
		return nil, fmt.Errorf("不应被调用")
	}})
	defer stop()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprint(conn, "GET /relative HTTP/1.1\r\nHost: example.com\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非绝对 URI 应返回 400，得到 %d", resp.StatusCode)
	}
}

func TestDialFailureReturns502(t *testing.T) {
	dialer := fakeDialer{dial: func(ctx context.Context, address string) (net.Conn, error) {
		return nil, fmt.Errorf("模拟建连失败")
	}}
	proxyAddr, stop := startProxy(t, fakePicker{}, dialer)
	defer stop()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprint(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("建连失败应返回 502，得到 %d", resp.StatusCode)
	}
}

func TestValidateRejectsNonLoopback(t *testing.T) {
	s := &Server{ListenAddr: "0.0.0.0:8080", Picker: fakePicker{}, Dialer: fakeDialer{}}
	if err := s.validate(); err == nil {
		t.Fatal("非回环地址必须被拒绝")
	}
	s.ListenAddr = "127.0.0.1:8080"
	if err := s.validate(); err != nil {
		t.Fatalf("回环地址应通过校验: %v", err)
	}
}
