package main

// 守护进程的自动启动与收尾。
//
// 为什么需要：桌面端本身只是看板，数据来自 caulinkd 的只读接口。
// 用户双击桌面端时如果守护进程没跑，面板会退回"演示数据"——
// 看起来就像程序坏了。所以这里在启动时探测一次，没有就拉起来。
//
// 三种可配置方式（优先级从高到低）：
//   CAULINK_DAEMON  环境变量指定 caulinkd 可执行文件
//   CAULINK_CONFIG  环境变量指定配置文件
//   否则按 exe 相对位置推断（<repo>/desktop/bin/ → <repo>/bin/caulinkd.exe）

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const statusURL = "http://127.0.0.1:7788/api/v1/status"

// daemonHandle 记录本程序是否启动了守护进程，退出时要负责关掉。
type daemonHandle struct {
	cmd     *exec.Cmd
	started bool
}

// ensureDaemon 确保 caulinkd 正在运行。
//
// 已经在跑就什么都不做；否则启动它并等待状态接口就绪（最多 10 秒）。
// 拉起失败不影响桌面端本身——面板会显示"演示数据"，并在日志里说明原因。
func ensureDaemon() *daemonHandle {
	// 先清理上次异常退出留下的残留（只按记录的 PID 且校验进程名，不会误杀）。
	cleanupStaleDaemon()

	if statusReachable(400 * time.Millisecond) {
		log.Printf("[daemon] 检测到 caulinkd 已在运行（7788 可用）")
		return &daemonHandle{}
	}

	exe := findDaemonExe()
	if exe == "" {
		log.Printf("[daemon] 找不到 caulinkd.exe，面板将显示演示数据")
		return &daemonHandle{}
	}
	cfg := findDaemonConfig()
	args := []string{}
	if cfg != "" {
		args = append(args, "-config", cfg)
	}

	// 不传 --no-auth：账号密码现在在界面上填。
	// 没有凭据时 caulinkd 会以"假设已认证"模式运行，
	// 用户到「校园网账号」里登录即可启用真实认证与静默重连。
	log.Printf("[daemon] 启动 caulinkd: %s %v", exe, args)

	cmd := exec.Command(exe, args...)
	configureHidden(cmd) // 不弹控制台窗口
	cmd.Dir = filepath.Dir(exe)

	// 把守护进程的输出落到文件，出问题时能直接看原因
	// （它是以隐藏窗口启动的，控制台不显示任何东西）。
	if f, err := os.OpenFile(daemonLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		cmd.Stdout = f
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[daemon] 启动失败: %v", err)
		return &daemonHandle{}
	}

	h := &daemonHandle{cmd: cmd, started: true}
	if cmd.Process != nil {
		recordPID(cmd.Process.Pid)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if statusReachable(500 * time.Millisecond) {
			log.Printf("[daemon] caulinkd 已就绪")
			return h
		}
		time.Sleep(300 * time.Millisecond)
	}
	log.Printf("[daemon] caulinkd 启动后 10 秒仍未就绪，面板可能显示演示数据")
	return h
}

// stop 关闭由本程序启动的守护进程。
//
// 只关自己启动的：如果启动前它已经在跑（用户自己开的），保持原样。
func (h *daemonHandle) stop() {
	if h == nil || !h.started || h.cmd == nil || h.cmd.Process == nil {
		return
	}
	log.Printf("[daemon] 关闭由本程序启动的 caulinkd")
	_ = h.cmd.Process.Kill()
	_, _ = h.cmd.Process.Wait()
	clearPID()
}

// statusReachable 判断状态接口是否可用。
func statusReachable(timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(statusURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	return resp.StatusCode == http.StatusOK
}

// findDaemonExe 依次尝试环境变量与相对位置。
func findDaemonExe() string {
	if p := os.Getenv("CAULINK_DAEMON"); p != "" && fileExists(p) {
		return p
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	candidates := []string{
		// <repo>/desktop/bin/caulink-desktop.exe → <repo>/bin/caulinkd.exe
		filepath.Join(dir, "..", "..", "bin", "caulinkd.exe"),
		// 打包分发时的常见布局：与桌面端同目录
		filepath.Join(dir, "caulinkd.exe"),
		filepath.Join(dir, "..", "caulinkd.exe"),
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err == nil && fileExists(abs) {
			return abs
		}
	}
	return ""
}

// findDaemonConfig 优先用本机配置，其次示例配置。
func findDaemonConfig() string {
	if p := os.Getenv("CAULINK_CONFIG"); p != "" && fileExists(p) {
		return p
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	candidates := []string{
		filepath.Join(dir, "..", "..", "configs", "caulinkd.local.json"),
		filepath.Join(dir, "..", "..", "configs", "caulinkd.example.json"),
		filepath.Join(dir, "configs", "caulinkd.local.json"),
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err == nil && fileExists(abs) {
			return abs
		}
	}
	return ""
}

// fileExists 判断路径是否存在且是文件。
// 放在这个跨平台文件里，供 daemon / steammode 共用。
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// configHasCredentials 判断配置文件里是否填了账号密码。
//
// 故意用宽松解析：文件可能带注释或字段缺失，只要能读出两个非空字符串即可。
func configHasCredentials(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var v struct {
		Auth struct {
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return false
	}
	return v.Auth.Username != "" && v.Auth.Password != ""
}

// daemonLogPath 返回守护进程日志路径（与桌面端日志同目录）。
func daemonLogPath() string {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "caulink-desktop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return filepath.Join(os.TempDir(), "caulinkd.log")
	}
	return filepath.Join(dir, "caulinkd.log")
}

// pidFilePath 记录由桌面端启动的守护进程 PID。
func pidFilePath() string {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "caulink-desktop")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "caulinkd.pid")
}

// recordPID 把本次启动的 pid 写下来。
func recordPID(pid int) {
	_ = os.WriteFile(pidFilePath(), []byte(strconv.Itoa(pid)), 0o644)
}

// clearPID 删除 pid 文件（正常退出时调用）。
func clearPID() {
	_ = os.Remove(pidFilePath())
}

// cleanupStaleDaemon 处理上次异常退出留下的守护进程。
//
// 场景：桌面端被任务管理器强杀，defer 不会执行，caulinkd 会一直挂着。
// 这里在启动时按记录的 pid 清理一次；如果那个 pid 已经不是 caulinkd
// （被系统复用），就跳过——宁可留下一个进程，也不能误杀别的程序。
func cleanupStaleDaemon() {
	data, err := os.ReadFile(pidFilePath())
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		_ = os.Remove(pidFilePath())
		return
	}
	// Windows 上按名字校验更稳妥：只杀进程名确实是 caulinkd 的。
	cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH")
	out, err := cmd.Output()
	if err == nil && strings.Contains(strings.ToLower(string(out)), "caulinkd") {
		log.Printf("[daemon] 清理上次残留的 caulinkd（PID %d）", pid)
		if p, e := os.FindProcess(pid); e == nil {
			_ = p.Kill()
			time.Sleep(500 * time.Millisecond)
		}
	}
	_ = os.Remove(pidFilePath())
}
