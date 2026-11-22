package main

// 这个文件实现设置页里的"下载游戏时打开：重启 Steam 以使其支持多 IP 并行下载"。
//
// 为什么需要重启 Steam：
//   Steam 只在启动阶段读取一次 Windows 系统代理，之后就一直用缓存的那个。
//   而本地 VPN 客户端（0dcloud）每 8 秒会把系统代理改回自己，
//   所以必须在"把代理按在 127.0.0.1:8080"的这段时间里启动 Steam，
//   它才会把下载流量交给 caulinkd，从而走校园网多源 IP 聚合。
//
// 交互：
//   POST /api/steam-mode          开始：抢注代理 → 结束 Steam → 重启 Steam → 按住 N 秒
//   POST /api/steam-mode/cancel   取消按住（不再抢注）
//   GET  /api/steam-mode          查询当前进度
//
// 只允许本机同源页面调用：要求带 X-Caulink-Action 头，避免其它本地程序误触。

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const (
	// steamProxyAddr 是 caulinkd 的 HTTP 代理地址。
	steamProxyAddr = "127.0.0.1:8080"
	// steamHoldSeconds 是启动后按住代理的时长。
	//
	// 实测：Steam 在启动后几十秒内才真正读取代理，单次设置必被 VPN 抢走。
	// 90 秒足够覆盖它的初始化。
	steamHoldSeconds = 90
	// steamActionHeader 是调用这两个接口必须带的头，防止本地其它程序误触。
	steamActionHeader = "X-Caulink-Action"
)

// steamModeState 是开关的运行时状态。
type steamModeState struct {
	mu         sync.Mutex
	running    bool
	phase      string // idle / setting-proxy / restarting / holding / done / failed
	detail     string
	started    time.Time
	holdTill   time.Time
	cancel     chan struct{}
	lastErr    string
	scriptPath string
}

var steamState = &steamModeState{phase: "idle"}

// steamStatusPayload 是返回给前端的 JSON。
type steamStatusPayload struct {
	Running     bool   `json:"running"`
	Phase       string `json:"phase"`
	Detail      string `json:"detail"`
	HoldSeconds int    `json:"hold_seconds"`
	RemainSec   int    `json:"remain_seconds"`
	Proxy       string `json:"proxy"`
	Script      string `json:"script,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (s *steamModeState) snapshot() steamStatusPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	remain := 0
	if s.running && !s.holdTill.IsZero() {
		if d := time.Until(s.holdTill); d > 0 {
			remain = int(d.Seconds())
		}
	}
	return steamStatusPayload{
		Running:     s.running,
		Phase:       s.phase,
		Detail:      s.detail,
		HoldSeconds: steamHoldSeconds,
		RemainSec:   remain,
		Proxy:       steamProxyAddr,
		Script:      s.scriptPath,
		Error:       s.lastErr,
	}
}

// handleSteamMode 处理 /api/steam-mode 的 GET/POST。
func handleSteamMode(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(steamActionHeader) != "1" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "缺少 " + steamActionHeader + " 头"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, steamState.snapshot())
	case http.MethodPost:
		if r.URL.Path == "/api/steam-mode/cancel" {
			steamState.stopAndKillSteam()
			writeJSON(w, http.StatusOK, steamState.snapshot())
			return
		}
		if err := steamState.start(); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, steamState.snapshot())
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只支持 GET/POST"})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// start 启动一次"重启 Steam 并按住代理"的流程。
func (s *steamModeState) start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("已在进行中，请稍候")
	}
	s.running = true
	s.phase = "setting-proxy"
	s.detail = "正在把系统代理指向 " + steamProxyAddr
	s.started = time.Now()
	s.lastErr = ""
	cancel := make(chan struct{})
	s.cancel = cancel
	s.mu.Unlock()

	go s.run(cancel)
	return nil
}

func (s *steamModeState) set(phase, detail string) {
	s.mu.Lock()
	s.phase = phase
	s.detail = detail
	s.mu.Unlock()
}

func (s *steamModeState) fail(err error) {
	s.mu.Lock()
	s.running = false
	s.phase = "failed"
	s.detail = "失败：" + err.Error()
	s.lastErr = err.Error()
	s.mu.Unlock()
}

// run 是主流程：设代理 → 结束 Steam → 启动 Steam → 按住代理。
func (s *steamModeState) run(cancel chan struct{}) {
	// 1) 代理指向我们。
	//
	// 如果进程被限制写注册表（某些安全软件会拦截"未知程序修改代理设置"，
	// 报 Access is denied），退回到调用仓库里的 PowerShell 脚本：
	// PowerShell 是系统信任的程序，通常不会被拦；而且它有可见窗口，
	// 用户能看到进度，比一个静默失败好得多。
	log.Printf("[steam-mode] 开始，准备写系统代理 %s", steamProxyAddr)
	if err := setSystemProxy(steamProxyAddr); err != nil {
		// 本进程被安全策略限制修改系统代理，且它派生的任何子进程都会继承
		// 同样的限制。实测过并全部失败：
		//   - 直接 RegSetValueEx            → Access is denied
		//   - 派生 reg.exe / powershell     → Access is denied
		//   - 让 explorer 打开 .bat         → 没有窗口，脚本没执行
		//   - ShellExecute "runas" 提权     → 提权后再让 explorer 交接 Steam
		//                                     报 0xc0000142（explorer 初始化失败）
		//
		// 而由用户自己双击的快捷方式（Explorer → bat → PowerShell）是可行的，
		// 因为那条链的发起者是 Explorer，令牌正常。
		//
		// 所以这里不再假装能自动完成，直接给出可执行的指引。
		log.Printf("[steam-mode] 写系统代理失败: %v，转手动指引", err)

		hint := ""
		if script, serr := findHelperScript(); serr == nil {
			hint = script
		}

		s.mu.Lock()
		s.running = false
		s.phase = "manual"
		s.lastErr = err.Error()
		s.detail = "本机安全策略不允许程序修改系统代理。请双击桌面上的「Steam 多IP 启动」快捷方式，" +
			"它会在约 " + fmt.Sprint(steamHoldSeconds) + " 秒内完成全部操作。"
		s.scriptPath = hint
		s.mu.Unlock()
		return
	}
	log.Printf("[steam-mode] 系统代理已写入")

	// 2) 记录 Steam 路径后结束它
	steamExe := findSteamExe()
	log.Printf("[steam-mode] 解析到 Steam 路径: %q", steamExe)
	if steamExe == "" {
		s.fail(errors.New("找不到 Steam，请确认已安装（可在脚本里用 -SteamExe 指定）"))
		return
	}
	s.set("restarting", "正在关闭 Steam…")
	_ = exec.Command("taskkill", "/F", "/IM", "steam.exe").Run()
	time.Sleep(5 * time.Second)

	// 3) 启动前再压一次，缩小被 VPN 抢走的窗口
	_ = setSystemProxy(steamProxyAddr)
	if err := exec.Command(steamExe).Start(); err != nil {
		log.Printf("[steam-mode] 启动 Steam 失败: %v", err)
		s.fail(fmt.Errorf("启动 Steam 失败: %w", err))
		return
	}
	log.Printf("[steam-mode] Steam 已启动，开始按住代理 %d 秒", steamHoldSeconds)

	// 4) 持续按住，直到超时或取消
	s.set("holding", fmt.Sprintf("Steam 已启动，正在保持代理 %d 秒", steamHoldSeconds))
	s.mu.Lock()
	s.holdTill = time.Now().Add(steamHoldSeconds * time.Second)
	s.mu.Unlock()

	deadline := time.Now().Add(steamHoldSeconds * time.Second)
	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()

	for {
		if err := setSystemProxy(steamProxyAddr); err != nil {
			// 单次写失败不致命（可能被占用），继续重试
			s.mu.Lock()
			s.lastErr = err.Error()
			s.mu.Unlock()
		}
		select {
		case <-cancel:
			s.mu.Lock()
			s.running = false
			s.phase = "idle"
			s.detail = "已取消按住"
			s.holdTill = time.Time{}
			s.mu.Unlock()
			return
		case <-ticker.C:
			if time.Now().After(deadline) {
				s.mu.Lock()
				s.running = false
				s.phase = "done"
				s.detail = "已完成：Steam 已重启并缓存了我们的代理"
				s.holdTill = time.Time{}
				s.mu.Unlock()
				return
			}
		}
	}
}

// stopAndKillSteam 关闭界面开关时调用：停止按住，并结束 Steam。
func (s *steamModeState) stopAndKillSteam() {
	s.cancelHold()
	s.mu.Lock()
	s.running = false
	s.phase = "idle"
	s.detail = "已关闭 Steam"
	s.holdTill = time.Time{}
	s.mu.Unlock()
	if err := exec.Command("taskkill", "/F", "/IM", "steam.exe").Run(); err == nil {
		log.Printf("[steam-mode] 用户关闭开关，已结束 Steam")
		return
	}
	// 本进程起不了子进程时，走 Explorer 执行关闭脚本。
	if bat, err := findCloseScript(); err == nil {
		if lerr := exec.Command("explorer.exe", bat).Start(); lerr == nil {
			log.Printf("[steam-mode] 已交由辅助脚本关闭 Steam: %s", bat)
			return
		}
	}
	log.Printf("[steam-mode] 关闭 Steam 失败")
}

func (s *steamModeState) cancelHold() {
	s.mu.Lock()
	ch := s.cancel
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// setSystemProxy 把当前用户的系统代理写成 addr 并启用。
func setSystemProxy(addr string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if err := k.SetStringValue("ProxyServer", addr); err != nil {
		return err
	}
	return k.SetDWordValue("ProxyEnable", 1)
}

// findSteamExe 依次尝试常见安装位置与注册表，返回可用的 steam.exe 路径。
func findSteamExe() string {
	if p := os.Getenv("CAULINK_STEAM_EXE"); p != "" && fileExists(p) {
		return p
	}
	candidates := []string{
		`D:\Program Files (x86)\Steam\steam.exe`,
		`C:\Program Files (x86)\Steam\steam.exe`,
		`C:\Program Files\Steam\steam.exe`,
		`E:\Steam\steam.exe`,
	}
	if p := steamExeFromRegistry(); p != "" {
		candidates = append([]string{p}, candidates...)
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// steamExeFromRegistry 读 HKCU\Software\Valve\Steam\SteamExe。
func steamExeFromRegistry() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("SteamExe")
	if err != nil || v == "" {
		return ""
	}
	return filepath.FromSlash(v)
}

// findHelperScript 在 exe 附近找仓库里的辅助启动器（.bat）。
//
// exe 位于 <repo>\desktop\bin\caulink-desktop.exe，脚本位于
// <repo>\scripts\start-steam-with-caulink.bat，所以从 exe 往上两级再进 scripts。
func findHelperScript() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	candidates := []string{
		filepath.Join(dir, "..", "..", "scripts", "start-steam-with-caulink.bat"),
		filepath.Join(dir, "..", "scripts", "start-steam-with-caulink.bat"),
		filepath.Join(dir, "scripts", "start-steam-with-caulink.bat"),
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if fileExists(abs) {
			return abs, nil
		}
	}
	return "", fmt.Errorf("在 %s 附近没有找到 start-steam-with-caulink.bat", dir)
}

// findCloseScript 找 scripts\close-steam.bat（关闭开关时用）。
func findCloseScript() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	for _, c := range []string{
		filepath.Join(dir, "..", "..", "scripts", "close-steam.bat"),
		filepath.Join(dir, "..", "scripts", "close-steam.bat"),
		filepath.Join(dir, "scripts", "close-steam.bat"),
	} {
		if abs, err := filepath.Abs(c); err == nil && fileExists(abs) {
			return abs, nil
		}
	}
	return "", fmt.Errorf("找不到 close-steam.bat")
}

// shellExecuteRunAs 用 ShellExecuteW 以 "runas" 动词启动脚本（触发 UAC）。
//
// 只接受 .bat/.cmd/.ps1 这类脚本；返回值 nil 表示"已请求启动"，
// 用户点取消时 ShellExecute 会失败（ERROR_CANCELLED）。
func shellExecuteRunAs(file string) error {
	const (
		swShowNormal = 1
	)
	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("ShellExecuteW")

	verb, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	filePtr, err := syscall.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	dirPtr, err := syscall.UTF16PtrFromString(filepath.Dir(file))
	if err != nil {
		return err
	}

	ret, _, _ := proc.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(filePtr)),
		0,
		uintptr(unsafe.Pointer(dirPtr)),
		uintptr(swShowNormal),
	)
	// ShellExecute 返回值 > 32 表示成功。
	if ret <= 32 {
		return fmt.Errorf("ShellExecuteW 返回 %d（用户可能取消了 UAC）", ret)
	}
	return nil
}
