package control

import (
	"encoding/json"
	"net/http"
	"time"
)

// AuthController 是"网页登录"的系统入口：把界面上填的账号密码交给守护进程，
// 由它完成认证、保存凭据、以及后续的静默重连。
//
// 这是控制面里除 Steam 之外的第二个写操作，所以同样要求 X-Caulink-Action 头。
type AuthController interface {
	// Login 校验并启用认证。remember 为 true 时把凭据落到本机。
	Login(username, password string, remember bool) error
	// Logout 停止认证并删除本机保存的凭据。
	Logout() error
	// AuthStatus 返回当前认证运行时状态。
	AuthStatus() AuthRuntimeStatus
}

// AuthRuntimeStatus 是给 UI 的状态快照。
type AuthRuntimeStatus struct {
	// Configured 表示当前有可用的账号（内存或已保存）。
	Configured bool `json:"configured"`
	// Running 表示认证子进程正在运行。
	Running bool `json:"running"`
	// Account 是脱敏后的账号，便于 UI 显示"当前登录的是谁"。
	Account string `json:"account,omitempty"`
	// Remember 表示凭据是否已保存到本机。
	Remember bool `json:"remember"`
	// Message 是一句可直接展示的说明。
	Message string `json:"message,omitempty"`
	// At 是状态时刻。
	At time.Time `json:"at"`
}

// loginRequest 是 POST /api/v1/auth/login 的请求体。
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

// registerAuthRoutes 注册认证相关路由。
func (s *StatusServer) registerAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/auth", s.handleAuthStatus)
	mux.HandleFunc("/api/v1/auth/login", s.handleAuthLogin)
	mux.HandleFunc("/api/v1/auth/logout", s.handleAuthLogout)
}

func (s *StatusServer) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用认证接口")
		return
	}
	writeJSON(w, http.StatusOK, s.Auth.AuthStatus())
}

func (s *StatusServer) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用认证接口")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	if r.Header.Get("X-Caulink-Action") != "1" {
		writeErr(w, http.StatusForbidden, "缺少 X-Caulink-Action 头")
		return
	}

	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "账号或密码为空")
		return
	}

	if err := s.Auth.Login(req.Username, req.Password, req.Remember); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Auth.AuthStatus())
}

func (s *StatusServer) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用认证接口")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	if r.Header.Get("X-Caulink-Action") != "1" {
		writeErr(w, http.StatusForbidden, "缺少 X-Caulink-Action 头")
		return
	}
	if err := s.Auth.Logout(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Auth.AuthStatus())
}
