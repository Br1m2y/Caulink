//go:build !windows

package main

// 非 Windows 平台没有"系统代理"这套注册表机制，租约相关函数全部空实现，
// 让调用方不必到处写 build tag。

func writeProxyLease()   {}
func restoreProxyLease() {}
