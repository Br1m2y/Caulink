//go:build windows

package socks5

import (
	"errors"
	"syscall"
)

func isConnRefused(err error) bool {
	// WSAECONNREFUSED (10061) 是 Windows 上对应 ECONNREFUSED 的错误码。
	// syscall 包在 Windows 下不一定导出 ECONNREFUSED，所以显式用 WSA 常量。
	return errors.Is(err, syscall.Errno(10061))
}
