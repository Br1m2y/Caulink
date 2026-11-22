package socks5

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestReadRequestIPv4(t *testing.T) {
	raw := []byte{5, cmdConnect, 0, atypIPv4, 127, 0, 0, 1, 0x01, 0xBB}
	req, err := readRequest(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if req.cmd != cmdConnect {
		t.Errorf("cmd 应为 CONNECT，实际 %d", req.cmd)
	}
	if req.host != "127.0.0.1" {
		t.Errorf("host 应为 127.0.0.1，实际 %s", req.host)
	}
	if req.port != 443 {
		t.Errorf("port 应为 443，实际 %d", req.port)
	}
}

func TestReadRequestDomain(t *testing.T) {
	domain := []byte("example.com")
	raw := []byte{5, cmdConnect, 0, atypDomain, byte(len(domain))}
	raw = append(raw, domain...)
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, 8080)
	raw = append(raw, port...)

	req, err := readRequest(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if req.host != "example.com" {
		t.Errorf("host 应为 example.com，实际 %s", req.host)
	}
	if req.port != 8080 {
		t.Errorf("port 应为 8080，实际 %d", req.port)
	}
}

func TestReadRequestIPv6(t *testing.T) {
	ip := []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	raw := []byte{5, cmdConnect, 0, atypIPv6}
	raw = append(raw, ip...)
	raw = append(raw, 0x00, 0x50) // 80

	req, err := readRequest(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if req.host != "2001:db8::1" {
		t.Errorf("host 应为 2001:db8::1，实际 %s", req.host)
	}
	if req.port != 80 {
		t.Errorf("port 应为 80，实际 %d", req.port)
	}
}

func TestReadRequestRejectsBadInput(t *testing.T) {
	cases := map[string][]byte{
		"版本不是 5":    {4, cmdConnect, 0, atypIPv4, 127, 0, 0, 1, 0, 80},
		"不支持的 ATYP": {5, cmdConnect, 0, 0x02, 127, 0, 0, 1, 0, 80},
		"域名长度为 0":   {5, cmdConnect, 0, atypDomain, 0, 0, 80},
		"报文被截断":     {5, cmdConnect, 0, atypIPv4, 127},
	}
	for name, raw := range cases {
		if _, err := readRequest(bytes.NewReader(raw)); err == nil {
			t.Errorf("%s：应报错", name)
		}
	}
}

func TestAddrJoinsHostAndPort(t *testing.T) {
	r := request{host: "example.com", port: 8443}
	if got := r.addr(); got != "example.com:8443" {
		t.Errorf("addr() = %s，期望 example.com:8443", got)
	}
}
