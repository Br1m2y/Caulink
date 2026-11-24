//go:build !windows

package main

import "log/slog"

// healProxyLease 在非 Windows 平台是空实现：没有注册表代理这回事。
func healProxyLease(logger *slog.Logger) {}

// 同样为空实现，保持调用方不写 build tag。
func writeProxyLease(snap proxySnapshot) {}
func removeProxyLease()                  {}
