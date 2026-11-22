//go:build !windows

package main

// 非 Windows 平台没有注册表与 Steam，这里提供同名的接口桩，
// 保证 Linux 上的 purego 编译（CI 用）能通过。

import "net/http"

func handleSteamMode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"error":"该功能仅支持 Windows"}`))
}
