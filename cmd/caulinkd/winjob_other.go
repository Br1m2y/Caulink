//go:build !windows

package main

import "log/slog"

// 非 Windows 平台没有作业对象/工具快照这套机制，两个函数都退化为空实现。
func ensureProcessJob() (func(), error)    { return func() {}, nil }
func killOrphanAuthProcesses(*slog.Logger) {}
