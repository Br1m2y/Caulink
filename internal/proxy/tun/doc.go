// Package tun implements TUN takeover of application traffic.
//
// Owner: B.
//
// Scope: Linux first (sing-tun or equivalent); Windows would need wintun plus
// an elevated service and is explicitly out of the demo path — the Windows
// demo uses the SOCKS5 listener instead (see docs/adr/).
//
// TODO: device creation, route table changes, flow-to-connection muxing, and
// cleanup on exit so a crashed run does not leave a dangling route.
package tun
