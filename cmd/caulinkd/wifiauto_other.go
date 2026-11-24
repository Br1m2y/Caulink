//go:build !windows

package main

import (
	"context"
	"log/slog"
	"time"
)

// autoConnectWifi 在非 Windows 平台是空实现。
//
// 自动给无线网卡注入校园网配置依赖 Windows 的 netsh，其它平台没有对应机制。
func autoConnectWifi(ctx context.Context, logger *slog.Logger, dataDir, ssid string, exclude []string, wait time.Duration) (connected, tried []string) {
	return nil, nil
}
