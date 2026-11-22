package control

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Br1m2y/caulink/internal/accel"
)

// AccelController 是"双通道加速下载"的控制入口。
//
// 这是控制面里第三个写操作（前两个是登录与 Steam 重启），所以同样要求
// X-Caulink-Action 头 —— 否则本机任何一个网页都能偷偷往你的磁盘里塞文件。
//
// *accel.Manager 直接实现这个接口，不另做适配层：它的方法名与语义
// 就是这里要的，多一层转换只会多一份要同步维护的字段。
type AccelController interface {
	Start(req accel.Request) (accel.TaskView, error)
	Snapshot() []accel.TaskView
	Cancel(id string) error
	DefaultDir() string
	MaxConnections() int
	// SetDefaultDir 校验并记住保存目录，返回规范化后的路径。
	SetDefaultDir(dir string) (string, error)
}

// AccelDefaults 给 UI 预填表单用。
type AccelDefaults struct {
	// Dir 是默认保存目录。
	Dir string `json:"dir"`
	// Connections 是默认并发分片数。
	Connections int `json:"connections"`
	// MaxConnections 是上限，UI 用它限制输入。
	MaxConnections int `json:"max_connections"`
}

// accelTasksResponse 是 GET /api/v1/accel/tasks 的响应。
type accelTasksResponse struct {
	Defaults AccelDefaults    `json:"defaults"`
	Tasks    []accel.TaskView `json:"tasks"`
}

// dirRequest 是 POST /api/v1/accel/settings 的请求体。
type dirRequest struct {
	Dir string `json:"dir"`
}

// cancelRequest 是 POST /api/v1/accel/tasks/cancel 的请求体。
type cancelRequest struct {
	ID string `json:"id"`
}

func (s *StatusServer) registerAccelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/accel/tasks", s.handleAccelTasks)
	mux.HandleFunc("/api/v1/accel/tasks/cancel", s.handleAccelCancel)
	mux.HandleFunc("/api/v1/accel/settings", s.handleAccelSettings)
}

func (s *StatusServer) handleAccelTasks(w http.ResponseWriter, r *http.Request) {
	if s.Accel == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用加速下载")
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		// UI 每秒轮询，任何中间缓存都会让它显示过期进度。
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, accelTasksResponse{
			Defaults: AccelDefaults{
				Dir:            s.Accel.DefaultDir(),
				Connections:    accel.DefaultConnections,
				MaxConnections: s.Accel.MaxConnections(),
			},
			Tasks: s.Accel.Snapshot(),
		})

	case http.MethodPost:
		if r.Header.Get("X-Caulink-Action") != "1" {
			writeErr(w, http.StatusForbidden, "缺少 X-Caulink-Action 头")
			return
		}
		var req accel.Request
		// 1 MiB 上限：这里只接受一个 URL 和几个参数，正常请求不到 1 KB。
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
			return
		}
		view, err := s.Accel.Start(req)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, view)

	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		writeErr(w, http.StatusMethodNotAllowed, "只支持 GET / POST")
	}
}

// handleAccelSettings 修改保存目录。
//
// 单独一个端点，而不是让"每次提交任务都顺手改默认值"：用户在界面上改路径
// 是一个独立动作，改完应该立刻生效并记住，而不是等他下一次点开始。
func (s *StatusServer) handleAccelSettings(w http.ResponseWriter, r *http.Request) {
	if s.Accel == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用加速下载")
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

	var req dirRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	dir, err := s.Accel.SetDefaultDir(req.Dir)
	if err != nil {
		// 路径不可用属于用户输入问题，用 400 让界面直接把原因显示出来。
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir})
}

func (s *StatusServer) handleAccelCancel(w http.ResponseWriter, r *http.Request) {
	if s.Accel == nil {
		writeErr(w, http.StatusNotImplemented, "该构建未启用加速下载")
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

	var req cancelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		writeErr(w, http.StatusBadRequest, "缺少任务 id")
		return
	}
	if err := s.Accel.Cancel(req.ID); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"at": time.Now().UTC(),
	})
}
