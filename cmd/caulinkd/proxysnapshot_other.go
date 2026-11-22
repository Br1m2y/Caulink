//go:build !windows

package main

// 非 Windows 平台没有系统代理配置可快照。
// 字段与 Windows 版本保持一致，避免跨平台编译时结构体不匹配。

// proxySnapshot 是系统代理配置的一份完整副本。
type proxySnapshot struct {
	Valid      bool
	Server     string
	Enable     bool
	Override   string
	AutoConfig string
}

func readProxySnapshot() proxySnapshot { return proxySnapshot{} }

func restoreProxySnapshot(_ proxySnapshot) error { return nil }
