package control

import (
	"encoding/json"
	"net/http"
)

// ProxyHoldController 是「把系统代理按在 caulinkd 上」的入口。
//
// 用途：只认系统代理的程序（例如迅雷的"使用系统代理服务器"）也能走多 IP。
// 与 Steam 模式的区别是不重启任何进程，纯粹按住注册表一段时间。
type ProxyHoldController interface {
	StartProxyHold(seconds int) error
	StopProxyHold() error
	ProxyHoldStatus() ProxyHoldStatus
}

// ProxyHoldStatus 是给 UI 的状态快照。
type ProxyHoldStatus struct {
	Running       bool   `json:"running"`
	Proxy         string `json:"proxy,omitempty"`
	RemainSeconds int    `json:"remain_seconds"`
	PreviousProxy string `json:"previous_proxy,omitempty"`
	Error         string `json:"error,omitempty"`
}

type proxyHoldRequest struct {
	Seconds int `json:"seconds"`
}

func (s *StatusServer) registerProxyHoldRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/proxyhold", s.handleProxyHoldStatus)
	mux.HandleFunc("/api/v1/proxyhold/start", s.handleProxyHoldStart)
	mux.HandleFunc("/api/v1/proxyhold/stop", s.handleProxyHoldStop)
}

func (s *StatusServer) handleProxyHoldStatus(w http.ResponseWriter, r *http.Request) {
	if s.ProxyHold == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用系统代理按住")
		return
	}
	writeJSON(w, http.StatusOK, s.ProxyHold.ProxyHoldStatus())
}

func (s *StatusServer) handleProxyHoldStart(w http.ResponseWriter, r *http.Request) {
	if s.ProxyHold == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用系统代理按住")
		return
	}
	if !steamWriteGuard(w, r) {
		return
	}
	var req proxyHoldRequest
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req)
	if err := s.ProxyHold.StartProxyHold(req.Seconds); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.ProxyHold.ProxyHoldStatus())
}

func (s *StatusServer) handleProxyHoldStop(w http.ResponseWriter, r *http.Request) {
	if s.ProxyHold == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用系统代理按住")
		return
	}
	if !steamWriteGuard(w, r) {
		return
	}
	if err := s.ProxyHold.StopProxyHold(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.ProxyHold.ProxyHoldStatus())
}
