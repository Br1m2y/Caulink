package control

import (
	"encoding/json"
	"net/http"
	"time"
)

// SteamController 是"重启 Steam 以支持多 IP 下载"的系统操作入口。
//
// 为什么由 caulinkd 做而不是桌面端自己做：
// 桌面端进程在这个环境里被限制写系统代理（连派生的 reg.exe 都被拒），
// 而 caulinkd 是普通进程，能正常写 HKCU。让能做的进程去做。
type SteamController interface {
	// StartSteamMode 依次：写系统代理 → 结束 Steam → 重启 Steam → 按住代理一段时间。
	// 异步执行，立刻返回。
	StartSteamMode() error
	// StopSteamMode 取消按住并关闭 Steam。
	StopSteamMode() error
	// SteamModeStatus 返回当前进度。
	SteamModeStatus() SteamModeStatus
}

// SteamModeStatus 是给 UI 的状态快照。
type SteamModeStatus struct {
	// Running 表示动作正在执行中。
	Running bool `json:"running"`
	// Phase 是阶段：idle / setting-proxy / restarting / holding / done / failed
	Phase string `json:"phase"`
	// Detail 是可直接展示的一句话。
	Detail string `json:"detail,omitempty"`
	// HoldSeconds 是按住代理的总时长。
	HoldSeconds int `json:"hold_seconds"`
	// RemainSeconds 是剩余按住时间。
	RemainSeconds int `json:"remain_seconds"`
	// Proxy 是写入的系统代理地址。
	Proxy string `json:"proxy,omitempty"`
	// SteamRunning 表示 Steam 当前是否在运行。
	SteamRunning bool `json:"steam_running"`
	// Error 是最近一次失败原因。
	Error string `json:"error,omitempty"`
	// At 是状态更新时刻。
	At time.Time `json:"at"`
}

// registerSteamRoutes 注册 Steam 相关路由。
func (s *StatusServer) registerSteamRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/steam", s.handleSteam)
	mux.HandleFunc("/api/v1/steam/on", s.handleSteamOn)
	mux.HandleFunc("/api/v1/steam/off", s.handleSteamOff)
}

// steamWriteGuard 只允许带特定头的本地请求执行写操作。
//
// 控制面本来是只读的；这条是唯一的例外，所以把门槛写死在这里：
// 必须带 X-Caulink-Action: 1，避免浏览器里的任意页面误触发"杀 Steam"。
func steamWriteGuard(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只支持 POST")
		return false
	}
	if r.Header.Get("X-Caulink-Action") != "1" {
		writeErr(w, http.StatusForbidden, "缺少 X-Caulink-Action 头")
		return false
	}
	if s := r.Header.Get("Origin"); s != "" && !isLocalOrigin(s) {
		writeErr(w, http.StatusForbidden, "只允许本地页面调用")
		return false
	}
	return true
}

func isLocalOrigin(origin string) bool {
	return origin == "http://wails.localhost" ||
		origin == "http://localhost" ||
		len(origin) > 16 && (origin[:17] == "http://127.0.0.1:" || origin[:16] == "http://localhost:")
}

func (s *StatusServer) handleSteam(w http.ResponseWriter, r *http.Request) {
	if s.Steam == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用 Steam 控制")
		return
	}
	writeJSON(w, http.StatusOK, s.Steam.SteamModeStatus())
}

func (s *StatusServer) handleSteamOn(w http.ResponseWriter, r *http.Request) {
	if s.Steam == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用 Steam 控制")
		return
	}
	if !steamWriteGuard(w, r) {
		return
	}
	if err := s.Steam.StartSteamMode(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Steam.SteamModeStatus())
}

func (s *StatusServer) handleSteamOff(w http.ResponseWriter, r *http.Request) {
	if s.Steam == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用 Steam 控制")
		return
	}
	if !steamWriteGuard(w, r) {
		return
	}
	if err := s.Steam.StopSteamMode(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Steam.SteamModeStatus())
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
