package control

import (
	"net/http"
	"time"
)

// SourceController 负责"重新扫描网卡"这类动作。
//
// 只做一件事：把配置里的源列表和网卡现状重新核对一次。
// 典型用途是 DHCP 换了地址、插拔了网线之后，不必重启程序。
type SourceController interface {
	RescanSources() (RescanResult, error)
}

// RescanResult 是重新扫描的结果，直接给界面显示。
type RescanResult struct {
	// Added 是本轮新加入调度池的源（"<网卡>@<IP>"）。
	Added []string `json:"added"`
	// Removed 是本轮被移除的源（地址已不存在）。
	Removed []string `json:"removed"`
	// Unchanged 是保持不变的源。
	Unchanged []string `json:"unchanged"`
	// Message 是一句可直接展示的说明。
	Message string `json:"message"`
	// At 是扫描时刻。
	At time.Time `json:"at"`
}

// registerSourceRoutes 注册网卡扫描相关路由。
func (s *StatusServer) registerSourceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/sources/rescan", s.handleRescan)
}

func (s *StatusServer) handleRescan(w http.ResponseWriter, r *http.Request) {
	if s.Sources == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用扫描接口")
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

	res, err := s.Sources.RescanSources()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
