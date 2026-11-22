package socks5

import (
	"bytes"
	"errors"
	"net"
	"testing"
)

func TestParseUDPDatagramIPv4(t *testing.T) {
	raw := []byte{0, 0, 0, atypIPv4, 192, 168, 1, 7, 0x1F, 0x90, 'h', 'i'}
	dg, err := parseUDPDatagram(raw)
	if err != nil {
		t.Fatal(err)
	}
	if dg.host != "192.168.1.7" {
		t.Errorf("host = %q", dg.host)
	}
	if dg.port != 8080 {
		t.Errorf("port = %d", dg.port)
	}
	if string(dg.payload) != "hi" {
		t.Errorf("payload = %q", dg.payload)
	}
}

func TestParseUDPDatagramDomain(t *testing.T) {
	// 域名长度为 1 字节，紧接域名，再跟 2 字节端口
	raw := []byte{0, 0, 0, atypDomain, 9}
	raw = append(raw, []byte("localhost")...)
	raw = append(raw, 0x00, 0x35)
	raw = append(raw, 1, 2, 3)

	dg, err := parseUDPDatagram(raw)
	if err != nil {
		t.Fatal(err)
	}
	if dg.host != "localhost" || dg.port != 53 {
		t.Fatalf("解析结果 %q:%d", dg.host, dg.port)
	}
	if !bytes.Equal(dg.payload, []byte{1, 2, 3}) {
		t.Errorf("payload = %v", dg.payload)
	}
}

func TestParseUDPDatagramIPv6(t *testing.T) {
	ip := net.ParseIP("2001:db8::1").To16()
	raw := []byte{0, 0, 0, atypIPv6}
	raw = append(raw, ip...)
	raw = append(raw, 0x01, 0xBB, 'x')

	dg, err := parseUDPDatagram(raw)
	if err != nil {
		t.Fatal(err)
	}
	if dg.host != "2001:db8::1" || dg.port != 443 {
		t.Fatalf("解析结果 %q:%d", dg.host, dg.port)
	}
	if string(dg.payload) != "x" {
		t.Errorf("payload = %q", dg.payload)
	}
}

func TestParseUDPDatagramRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want error
	}{
		{"过短", []byte{0, 0, 0}, nil},
		{"RSV 非零", []byte{1, 0, 0, atypIPv4, 1, 2, 3, 4, 0, 80}, nil},
		{"分片", []byte{0, 0, 1, atypIPv4, 1, 2, 3, 4, 0, 80}, ErrUDPFragment},
		{"未知地址类型", []byte{0, 0, 0, 0x09, 1, 2, 3, 4, 0, 80}, nil},
		{"地址截断", []byte{0, 0, 0, atypIPv4, 1, 2}, nil},
	}

	for _, c := range cases {
		_, err := parseUDPDatagram(c.raw)
		if err == nil {
			t.Errorf("%s: 应报错", c.name)
			continue
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: 期望 %v，实际 %v", c.name, c.want, err)
		}
	}
}

// 回包封装后再解析必须还原成同一个地址，否则客户端匹配不上会话。
func TestBuildUDPDatagramRoundTrip(t *testing.T) {
	for _, s := range []string{"10.0.0.1:53", "[2001:db8::1]:443"} {
		addr, err := net.ResolveUDPAddr("udp", s)
		if err != nil {
			t.Fatal(err)
		}
		pkt := buildUDPDatagram(addr, []byte("payload"))

		dg, err := parseUDPDatagram(pkt)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if dg.addr() != s {
			t.Errorf("往返后地址变了: %s -> %s", s, dg.addr())
		}
		if string(dg.payload) != "payload" {
			t.Errorf("%s: payload = %q", s, dg.payload)
		}
	}
}

// UDP ASSOCIATE 的成功响应必须带真实 BND 地址，不能是 0.0.0.0。
func TestWriteReplyAddrCarriesBindAddress(t *testing.T) {
	var buf bytes.Buffer
	bnd := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 34567}
	if err := writeReplyAddr(&buf, repSuccess, bnd); err != nil {
		t.Fatal(err)
	}

	raw := buf.Bytes()
	if len(raw) != 10 {
		t.Fatalf("IPv4 响应应为 10 字节，实际 %d", len(raw))
	}
	if raw[1] != repSuccess {
		t.Errorf("响应码 = 0x%02x", raw[1])
	}
	if raw[3] != atypIPv4 {
		t.Errorf("ATYP = 0x%02x", raw[3])
	}
	if !net.IP(raw[4:8]).Equal(bnd.IP) {
		t.Errorf("BND.ADDR = %v", net.IP(raw[4:8]))
	}
	if port := int(raw[8])<<8 | int(raw[9]); port != 34567 {
		t.Errorf("BND.PORT = %d", port)
	}
}
