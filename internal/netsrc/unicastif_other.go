//go:build !windows

package netsrc

import (
	"net"
	"syscall"
)

// DialControl 在非 Windows 平台返回 nil。
//
// Linux/macOS/BSD 的内核会按源地址反查路由：LocalAddr 绑对了，包就一定从该地址
// 所属的网卡出去，不需要额外的 socket 选项。Windows 没有这个行为，见
// unicastif_windows.go。
//
// 保留同一签名是为了让调用方代码不带平台分支 —— 平台差异只存在于本包。
func DialControl(net.IP) func(network, address string, c syscall.RawConn) error {
	return nil
}
