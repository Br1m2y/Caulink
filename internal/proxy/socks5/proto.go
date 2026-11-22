package socks5

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

// SOCKS5 协议常量（RFC 1928）。
const (
	version5 = 0x05

	methodNone         = 0x00
	methodNoAcceptable = 0xFF

	cmdConnect      = 0x01
	cmdBind         = 0x02
	cmdUDPAssociate = 0x03

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSuccess             = 0x00
	repGeneralFailure      = 0x01
	repNotAllowed          = 0x02
	repNetworkUnreachable  = 0x03
	repHostUnreachable     = 0x04
	repConnectionRefused   = 0x05
	repTTLExpired          = 0x06
	repCommandNotSupported = 0x07
	repAddressNotSupported = 0x08
)

// ErrBadVersion 表示对端不是 SOCKS5。
var ErrBadVersion = errors.New("socks5: 版本号不是 5")

// request 是一条已解析的连接请求。
type request struct {
	cmd  byte
	host string
	port uint16
}

// addr 返回 "host:port" 形式的目标地址。
func (r request) addr() string {
	return net.JoinHostPort(r.host, strconv.Itoa(int(r.port)))
}

// readRequest 读取并解析客户端的请求报文。
//
// 报文结构：
//
//	+----+-----+-------+------+----------+----------+
//	|VER | CMD |  RSV  | ATYP | DST.ADDR | DST.PORT |
//	+----+-----+-------+------+----------+----------+
//	 1     1     1       1      变长        2
func readRequest(r io.Reader) (request, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return request{}, fmt.Errorf("读取请求头: %w", err)
	}
	if head[0] != version5 {
		return request{}, ErrBadVersion
	}

	req := request{cmd: head[1]}

	host, port, err := readHostPort(r, head[3])
	if err != nil {
		return request{}, err
	}
	req.host = host
	req.port = port

	return req, nil
}

// readHostPort 按 ATYP 读取地址与端口。
//
// TCP 请求报文和 UDP 数据报头用的是同一套地址编码，抽出来共用，
// 避免两处实现各自漂移 —— 地址类型这种字节级细节最容易写错第二次。
func readHostPort(r io.Reader, atyp byte) (string, uint16, error) {
	var host string

	switch atyp {
	case atypIPv4:
		var b [4]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", 0, fmt.Errorf("读取 IPv4 地址: %w", err)
		}
		host = net.IP(b[:]).String()

	case atypIPv6:
		var b [16]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", 0, fmt.Errorf("读取 IPv6 地址: %w", err)
		}
		host = net.IP(b[:]).String()

	case atypDomain:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return "", 0, fmt.Errorf("读取域名长度: %w", err)
		}
		if l[0] == 0 {
			return "", 0, errors.New("socks5: 域名长度为 0")
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(r, b); err != nil {
			return "", 0, fmt.Errorf("读取域名: %w", err)
		}
		host = string(b)

	default:
		return "", 0, fmt.Errorf("socks5: 不支持的地址类型 0x%02x", atyp)
	}

	var portBuf [2]byte
	if _, err := io.ReadFull(r, portBuf[:]); err != nil {
		return "", 0, fmt.Errorf("读取端口: %w", err)
	}
	return host, binary.BigEndian.Uint16(portBuf[:]), nil
}

// writeReply 发送响应报文，BND 地址回零值。
//
// 只用于 CONNECT 的失败分支和 BIND 的拒绝分支：这些场景客户端不关心 BND。
// UDP ASSOCIATE 的**成功**响应必须用 writeReplyAddr 回真实地址，否则客户端
// 会往 0.0.0.0 发包 —— 那样连本地回环都出不去，表现为"UDP 就是不通"。
func writeReply(w io.Writer, rep byte) error {
	return writeReplyAddr(w, rep, &net.UDPAddr{IP: net.IPv4zero})
}

// writeReplyAddr 发送带真实 BND 地址的响应报文。
func writeReplyAddr(w io.Writer, rep byte, bnd *net.UDPAddr) error {
	buf := []byte{version5, rep, 0x00}

	ip := bnd.IP
	if ip4 := ip.To4(); ip4 != nil {
		buf = append(buf, atypIPv4)
		buf = append(buf, ip4...)
	} else if ip16 := ip.To16(); ip16 != nil {
		buf = append(buf, atypIPv6)
		buf = append(buf, ip16...)
	} else {
		// 地址不可用（比如客户端只给了域名），退回零值而不是发一条畸形报文。
		buf = append(buf, atypIPv4, 0, 0, 0, 0)
		buf = append(buf, 0, 0)
		_, err := w.Write(buf)
		return err
	}

	buf = binary.BigEndian.AppendUint16(buf, uint16(bnd.Port))
	_, err := w.Write(buf)
	return err
}

// ErrUDPFragment 表示收到分片的 UDP 数据报。
//
// RFC 1928 允许实现不支持分片：FRAG 非 0 时直接丢弃即可。
// 真实的 UDP 应用极少让中间层分片（IP 层会自己分片，客户端不会用这个字段），
// 所以这里明确丢弃并计数，而不是假装支持。
var ErrUDPFragment = errors.New("socks5: 不支持分片的 UDP 数据报")

// udpDatagram 是一条已解析的 UDP 请求。
type udpDatagram struct {
	host    string
	port    uint16
	payload []byte
}

func (d udpDatagram) addr() string {
	return net.JoinHostPort(d.host, strconv.Itoa(int(d.port)))
}

// parseUDPDatagram 解析 SOCKS5 UDP 请求报文。
//
// 报文结构：
//
//	+-----+------+------+----------+----------+----------+
//	| RSV | FRAG | ATYP | DST.ADDR | DST.PORT |   DATA   |
//	+-----+------+------+----------+----------+----------+
//	  2     1      1      变长        2         变长
func parseUDPDatagram(b []byte) (udpDatagram, error) {
	if len(b) < 4 {
		return udpDatagram{}, errors.New("socks5: UDP 报文过短")
	}
	if b[0] != 0 || b[1] != 0 {
		return udpDatagram{}, errors.New("socks5: UDP 报文 RSV 必须为 0")
	}
	if b[2] != 0 {
		return udpDatagram{}, ErrUDPFragment
	}

	r := bytes.NewReader(b[4:])
	host, port, err := readHostPort(r, b[3])
	if err != nil {
		return udpDatagram{}, err
	}

	return udpDatagram{
		host:    host,
		port:    port,
		payload: b[len(b)-r.Len():],
	}, nil
}

// buildUDPDatagram 把上游返回的数据封装成发往客户端的 UDP 报文。
//
// DST 字段填**实际上游地址**而不是客户端原始请求里的地址：如果原始请求是域名，
// 这里会变成解析后的 IP。RFC 1928 对此没有强制要求，但填真实地址能让客户端
// 按来源地址匹配会话的实现在 NAT 环境下更稳。
func buildUDPDatagram(from *net.UDPAddr, payload []byte) []byte {
	buf := make([]byte, 0, 22+len(payload))
	buf = append(buf, 0, 0, 0) // RSV + FRAG

	if ip4 := from.IP.To4(); ip4 != nil {
		buf = append(buf, atypIPv4)
		buf = append(buf, ip4...)
	} else {
		buf = append(buf, atypIPv6)
		buf = append(buf, from.IP.To16()...)
	}

	buf = binary.BigEndian.AppendUint16(buf, uint16(from.Port))
	return append(buf, payload...)
}

// handshake 完成方法协商。
//
// 只接受"无认证"，因为服务端**只监听 127.0.0.1**，连接方必然是本机进程。
// 如果将来要对外监听，必须先加上 RFC 1929 用户名密码认证 —— 否则任何
// 能访问该端口的人都能借着已认证的校园网会话上网。
func handshake(c net.Conn) error {
	var head [2]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return fmt.Errorf("读取握手头: %w", err)
	}
	if head[0] != version5 {
		return ErrBadVersion
	}

	n := int(head[1])
	if n == 0 {
		return errors.New("socks5: 客户端没有提供任何认证方法")
	}

	methods := make([]byte, n)
	if _, err := io.ReadFull(c, methods); err != nil {
		return fmt.Errorf("读取认证方法: %w", err)
	}

	for _, m := range methods {
		if m == methodNone {
			_, err := c.Write([]byte{version5, methodNone})
			return err
		}
	}

	_, _ = c.Write([]byte{version5, methodNoAcceptable})
	return errors.New("socks5: 客户端不支持无认证方式")
}
