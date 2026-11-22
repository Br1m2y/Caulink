//go:build windows

package main

// Steam 控制：把系统代理按在 caulinkd 的 HTTP 代理上，然后重启 Steam，
// 让它的下载流量走校园网多源 IP。
//
// 为什么由 caulinkd 执行：桌面端进程在某些机器上被限制写系统代理
// （连它派生的 reg.exe 都被拒），而 caulinkd 是普通进程，可以正常写 HKCU。
//
// 实现刻意不引入第三方依赖：注册表走 syscall 的 RegOpenKeyEx/RegSetValueEx。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/Br1m2y/caulink/internal/control"
)

const (
	// steamHoldDuration 是启动后按住系统代理的时长。
	//
	// Steam 在启动后几十秒内才真正读取代理，而本地 VPN 客户端每 8 秒
	// 会把代理改回自己，所以必须持续压住。实测只设一次会被抢走。
	steamHoldDuration = 90 * time.Second
)

var (
	internetSettingsPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	advapi               = syscall.NewLazyDLL("advapi32.dll")
	regOpenKeyExW        = advapi.NewProc("RegOpenKeyExW")
	regSetValueExW       = advapi.NewProc("RegSetValueExW")
	regCloseKeyProc      = advapi.NewProc("RegCloseKey")
)

// steamController 实现 control.SteamController。
type steamController struct {
	logger *slog.Logger
	proxy  string // 形如 127.0.0.1:8080

	mu       sync.Mutex
	running  bool
	phase    string
	detail   string
	lastErr  string
	holdTill time.Time
	cancel   context.CancelFunc
}

func newSteamController(logger *slog.Logger, proxyAddr string) *steamController {
	return &steamController{logger: logger, proxy: proxyAddr, phase: "idle"}
}

// Status 见 control.SteamController。
func (s *steamController) SteamModeStatus() control.SteamModeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	remain := 0
	if s.running && !s.holdTill.IsZero() {
		if d := time.Until(s.holdTill); d > 0 {
			remain = int(d.Seconds())
		}
	}
	return control.SteamModeStatus{
		Running:       s.running,
		Phase:         s.phase,
		Detail:        s.detail,
		HoldSeconds:   int(steamHoldDuration.Seconds()),
		RemainSeconds: remain,
		Proxy:         s.proxy,
		SteamRunning:  steamRunning(),
		Error:         s.lastErr,
		At:            time.Now(),
	}
}

// StartSteamMode 见 control.SteamController。
func (s *steamController) StartSteamMode() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("已在进行中，请稍候")
	}
	s.running = true
	s.phase = "setting-proxy"
	s.detail = "正在把系统代理指向 " + s.proxy
	s.lastErr = ""
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()

	go s.run(ctx)
	return nil
}

// StopSteamMode 见 control.SteamController。
func (s *steamController) StopSteamMode() error {
	s.mu.Lock()
	cancel := s.cancel
	s.running = false
	s.phase = "idle"
	s.detail = "已关闭 Steam"
	s.holdTill = time.Time{}
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	// 关掉 Steam，让系统代理回到 VPN 的状态不再影响它。
	_ = exec.Command("taskkill", "/F", "/IM", "steam.exe").Run()
	return nil
}

func (s *steamController) set(phase, detail string) {
	s.mu.Lock()
	s.phase = phase
	s.detail = detail
	s.mu.Unlock()
}

func (s *steamController) fail(err error) {
	s.mu.Lock()
	s.running = false
	s.phase = "failed"
	s.lastErr = err.Error()
	s.detail = "失败：" + err.Error()
	s.mu.Unlock()
	s.logger.Error("Steam 控制失败", "err", err)
}

// run 是主流程：写代理 → 关 Steam → 启动 Steam → 按住代理。
func (s *steamController) run(ctx context.Context) {
	if err := setSystemProxy(s.proxy); err != nil {
		s.fail(fmt.Errorf("写入系统代理失败: %w", err))
		return
	}
	s.logger.Info("Steam 控制：系统代理已写入", "proxy", s.proxy)

	exe := findSteamExe()
	if exe == "" {
		s.fail(errors.New("找不到 Steam，请确认已安装"))
		return
	}

	s.set("restarting", "正在关闭 Steam…")
	_ = exec.Command("taskkill", "/F", "/IM", "steam.exe").Run()
	deadline := time.Now().Add(8 * time.Second)
	for steamRunning() && time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
	}

	// 启动前再压一次，缩小被 VPN 抢走的窗口
	_ = setSystemProxy(s.proxy)
	if err := exec.Command(exe).Start(); err != nil {
		s.fail(fmt.Errorf("启动 Steam 失败: %w", err))
		return
	}
	s.logger.Info("Steam 已重启，开始按住系统代理", "hold", steamHoldDuration.String())

	s.mu.Lock()
	s.phase = "holding"
	s.detail = fmt.Sprintf("Steam 已启动，正在保持代理 %d 秒", int(steamHoldDuration.Seconds()))
	s.holdTill = time.Now().Add(steamHoldDuration)
	s.mu.Unlock()

	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()
	limit := time.Now().Add(steamHoldDuration)

	for {
		_ = setSystemProxy(s.proxy) // 单次失败不致命，下一轮再试
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Now().After(limit) {
				s.mu.Lock()
				s.running = false
				s.phase = "done"
				s.detail = "已完成：Steam 已缓存我们的代理，下载将走多 IP"
				s.holdTill = time.Time{}
				s.mu.Unlock()
				s.logger.Info("Steam 控制完成")
				return
			}
		}
	}
}

// setSystemProxy 写入系统代理。
//
// 两条路径，先快后稳：
//  1. 直接 syscall 写注册表（快，但某些机器的安全策略会拦截未签名程序写代理设置，
//     报 Access is denied）；
//  2. 回退到 PowerShell（微软签名的程序，通常被放行）。
//
// 实测：同一台机器上，本程序的 RegSetValueEx 被拒，而 powershell 的
// Set-ItemProperty 成功——所以回退不是兜底，而是在这类机器上的主路径。
func setSystemProxy(addr string) error {
	if err := setSystemProxyNative(addr); err == nil {
		return nil
	} else {
		_ = err
	}
	return setSystemProxyPowerShell(addr)
}

// setSystemProxyPowerShell 通过 PowerShell 写注册表并刷新 WinINET。
func setSystemProxyPowerShell(addr string) error {
	script := `$k='HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings';` +
		`Set-ItemProperty -Path $k -Name ProxyServer -Value '` + addr + `' -ErrorAction Stop;` +
		`Set-ItemProperty -Path $k -Name ProxyEnable -Value 1 -ErrorAction Stop;` +
		`Add-Type -TypeDefinition 'using System;using System.Runtime.InteropServices;` +
		`public class C{[DllImport("wininet.dll",SetLastError=true)]public static extern bool InternetSetOption(IntPtr h,int o,IntPtr b,int l);}' -ErrorAction SilentlyContinue;` +
		`[C]::InternetSetOption([IntPtr]::Zero,39,[IntPtr]::Zero,0)|Out-Null;` +
		`[C]::InternetSetOption([IntPtr]::Zero,37,[IntPtr]::Zero,0)|Out-Null`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("powershell 写入失败: %v / %s", err, string(out))
	}
	return nil
}

// setSystemProxyNative 用 syscall 直接写 HKCU 的代理设置（不引入第三方依赖）。
func setSystemProxyNative(addr string) error {
	path, err := syscall.UTF16PtrFromString(internetSettingsPath)
	if err != nil {
		return err
	}

	var key syscall.Handle
	ret, _, _ := regOpenKeyExW.Call(
		uintptr(syscall.HKEY_CURRENT_USER),
		uintptr(unsafe.Pointer(path)),
		0,
		uintptr(syscall.KEY_SET_VALUE),
		uintptr(unsafe.Pointer(&key)),
	)
	if ret != 0 {
		return syscall.Errno(ret)
	}
	defer regCloseKeyProc.Call(uintptr(key))

	// ProxyServer（REG_SZ）
	namePtr, err := syscall.UTF16PtrFromString("ProxyServer")
	if err != nil {
		return err
	}
	val, err := syscall.UTF16FromString(addr)
	if err != nil {
		return err
	}
	ret, _, _ = regSetValueExW.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(namePtr)),
		0,
		uintptr(syscall.REG_SZ),
		uintptr(unsafe.Pointer(&val[0])),
		uintptr(len(val)*2),
	)
	if ret != 0 {
		return syscall.Errno(ret)
	}

	// ProxyEnable（REG_DWORD）= 1
	enableName, _ := syscall.UTF16PtrFromString("ProxyEnable")
	var enable uint32 = 1
	ret, _, _ = regSetValueExW.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(enableName)),
		0,
		uintptr(syscall.REG_DWORD),
		uintptr(unsafe.Pointer(&enable)),
		4,
	)
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}

// steamRunning 判断 Steam 主进程是否在运行。
func steamRunning() bool {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq steam.exe", "/NH").Output()
	if err != nil {
		return false
	}
	return len(out) > 0 && (containsFold(string(out), "steam.exe"))
}

func containsFold(haystack, needle string) bool {
	h := []rune(haystack)
	n := []rune(needle)
	if len(n) == 0 || len(h) < len(n) {
		return false
	}
	eq := func(a, b rune) bool {
		if a >= 'A' && a <= 'Z' {
			a += 'a' - 'A'
		}
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		return a == b
	}
	for i := 0; i+len(n) <= len(h); i++ {
		ok := true
		for j := 0; j < len(n); j++ {
			if !eq(h[i+j], n[j]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// findSteamExe 依次从环境变量、注册表、常见安装位置解析 Steam 路径。
func findSteamExe() string {
	if p := os.Getenv("CAULINK_STEAM_EXE"); p != "" && fileExists(p) {
		return p
	}
	candidates := []string{}
	if p := steamExeFromRegistry(); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates,
		`D:\Program Files (x86)\Steam\steam.exe`,
		`C:\Program Files (x86)\Steam\steam.exe`,
		`C:\Program Files\Steam\steam.exe`,
		`E:\Steam\steam.exe`,
	)
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

func steamExeFromRegistry() string {
	path, err := syscall.UTF16PtrFromString(`Software\Valve\Steam`)
	if err != nil {
		return ""
	}
	var key syscall.Handle
	ret, _, _ := regOpenKeyExW.Call(
		uintptr(syscall.HKEY_CURRENT_USER),
		uintptr(unsafe.Pointer(path)),
		0,
		uintptr(syscall.KEY_QUERY_VALUE),
		uintptr(unsafe.Pointer(&key)),
	)
	if ret != 0 {
		return ""
	}
	defer regCloseKeyProc.Call(uintptr(key))

	namePtr, _ := syscall.UTF16PtrFromString("SteamExe")
	var typ uint32
	var buf [512]uint16
	size := uint32(len(buf) * 2)
	regQueryValueExW := advapi.NewProc("RegQueryValueExW")
	ret, _, _ = regQueryValueExW.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(namePtr)),
		0,
		uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 {
		return ""
	}
	p := syscall.UTF16ToString(buf[:])
	return filepath.FromSlash(p)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
