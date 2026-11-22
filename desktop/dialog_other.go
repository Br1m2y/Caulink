//go:build !windows

package main

// showError 在非 Windows 平台上只写日志：CI 用 Linux 跑 purego 编译，
// 这里不能依赖 syscall.NewLazyDLL（Windows 专有）。
func showError(title, text string) {
	logf := func(s string) { println(s) }
	logf(title + ": " + text)
}
