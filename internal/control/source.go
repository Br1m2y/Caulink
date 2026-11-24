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
	// AutoConnected 是本轮扫描时由程序自动连上校园网的无线网卡。
	//
	// 新电脑插上新的 USB 无线网卡时，Windows 没有 CAU 的配置文件，网卡不会自己连；
	// 扫描会先替用户连一次，连上后再把它加进调度池。界面据此告诉用户
	// "刚才那块网卡是程序帮你连上的"。
	AutoConnected []string `json:"auto_connected,omitempty"`
	// Skipped 列出本机存在、但这次没有接入调度的网卡及原因。
	//
	// 用户插了网卡却"扫不到"时，多数不是程序没发现，而是那块网卡当时
	// 还没有可路由的 IPv4（例如 DHCP 未完成、只有 169.254 链路本地地址）。
	// 把原因带出去，界面才能解释"为什么少了一块"，而不是只显示一个数字。
	Skipped []SkippedInterface `json:"skipped,omitempty"`
}

// SkippedInterface 是一块存在但未参与调度的网卡。
type SkippedInterface struct {
	// Interface 是网卡名（Windows 上即连接名，如 "WLAN 3"）。
	Interface string `json:"interface"`
	// Address 是这块网卡当前的地址快照，可能为空。
	Address string `json:"address,omitempty"`
	// Reason 是可直接展示给用户的原因。
	Reason string `json:"reason"`
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
