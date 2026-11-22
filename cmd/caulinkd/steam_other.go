//go:build !windows

package main

// 非 Windows 平台没有注册表与 Steam，返回 nil 控制器，
// 控制面会以 501 响应 Steam 相关接口。

import (
	"log/slog"

	"github.com/Br1m2y/caulink/internal/control"
)

func newSteamController(_ *slog.Logger, _ string) control.SteamController {
	return nil
}
