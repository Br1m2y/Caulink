// Package proxy takes over application traffic and dials it through a chosen
// source IP.
//
// Owner: B.
//
// Subpackages:
//   - socks5: a SOCKS5 listener on 127.0.0.1 only;
//   - tun: TUN takeover for the Linux path (Windows demo uses SOCKS5);
//   - dialer: binds an outbound connection to a specific source IP.
//
// Nothing here may listen on a non-loopback address: the project never offers
// a proxy service to the network (see docs/compliance.md).
//
// TODO(phase 1): implement dialer first, then socks5, TUN last.
package proxy
