package dialer

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

func localEntry(ip string) contract.IPPoolEntry {
	return contract.IPPoolEntry{
		ID:        "lo@" + ip,
		Interface: "lo",
		SourceIP:  ip,
	}
}

func TestDialBindsSourceIP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// 服务端把对端地址回传，用于确认连接确实来自绑定的源 IP。
	gotRemote := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		gotRemote <- c.RemoteAddr().String()
		_, _ = io.WriteString(c, "ok")
	}()

	d := Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", ln.Addr().String(), localEntry("127.0.0.1"))
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer conn.Close()

	select {
	case remote := <-gotRemote:
		host, _, _ := net.SplitHostPort(remote)
		if host != "127.0.0.1" {
			t.Fatalf("对端看到的源 IP 应为 127.0.0.1，实际 %s", host)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("服务端没有收到连接")
	}
}

// 绑定失败必须报错，不能悄悄回退到默认路由。
// 一旦回退，多 IP 调度就静默失效，而现象只是"速度上不去"。
func TestDialFailsWhenSourceUnavailable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	d := Dialer{Timeout: 300 * time.Millisecond}
	// 192.0.2.0/24 是 TEST-NET-1，保证本机不会持有这个地址。
	_, err = d.DialContext(context.Background(), "tcp", ln.Addr().String(), localEntry("192.0.2.77"))
	if err == nil {
		t.Fatal("源 IP 不属于本机时必须报错，不能回退到默认路由")
	}
}

func TestDialRejectsBadInput(t *testing.T) {
	d := Dialer{Timeout: 200 * time.Millisecond}

	if _, err := d.DialContext(context.Background(), "tcp", "127.0.0.1:1", localEntry("not-an-ip")); err == nil {
		t.Error("非法源 IP 应报错")
	}
	if _, err := d.DialContext(context.Background(), "icmp", "127.0.0.1:1", localEntry("127.0.0.1")); err == nil {
		t.Error("不支持的 network 应报错")
	}
}

func TestDialContextRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d := Dialer{Timeout: 2 * time.Second}
	// 目标故意不可达，靠 ctx 提前退出，避免测试等满超时。
	_, err := d.DialContext(ctx, "tcp", "192.0.2.1:9", localEntry("127.0.0.1"))
	if err == nil {
		t.Fatal("已取消的 ctx 不应成功建连")
	}
}
