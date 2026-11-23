// 启动器：双击后校正配置、尽力腾出 8080，再拉起界面。
//
// 三件事：
//  1. 源 IP 跟随网卡：caulinkd 的 sources 是"网卡名 + IP"，换机或 DHCP
//     换地址就失效，而界面上的「重新扫描」只改内存、不回写配置。
//  2. 8080 冲突：HTTP 代理固定用 127.0.0.1:8080（桌面端把地址写死了），
//     被占用时 caulinkd 会当成致命错误直接退出。本机常见元凶是 NI 的
//     Web 服务（NIWebServiceContainer），制图软件装完就会常驻。
//     这里探测到就试着停掉它；停不掉就关掉 HTTP 代理，其余功能照常。
//  3. 拉起界面：通过 CAULINK_DAEMON / CAULINK_CONFIG 指定绝对路径，
//     这样二进制可以放在 app\ 子目录里，根目录只留一个可点的 exe。
//
// 编译：go build -ldflags "-w -s -H windowsgui" -o "CAU WIFI HELPER.exe" .
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// virtualIface 匹配应被排除的虚拟网卡（VPN、容器、虚拟交换机等）。
var virtualIface = regexp.MustCompile(`(?i)radmin|zerotier|tailscale|wireguard|openvpn|tap|tun|vmware|virtualbox|hyper-v|vethernet|loopback|docker|wsl`)

// niServiceNames 是占用 8080 的常见 NI 服务，按可能性排序。
var niServiceNames = []string{"NIApplicationWebServer", "NIApplicationWebServer64", "NISystemWebServer"}

const proxyAddr = "127.0.0.1:8080"

type sourceConfig struct {
	Interface string `json:"interface"`
	SourceIP  string `json:"source_ip"`
}

func main() {
	dir, err := exeDir()
	if err != nil {
		fail("无法定位程序目录", err)
	}

	uiExe := filepath.Join(dir, "app", "caulink-ui.exe")
	if _, err := os.Stat(uiExe); err != nil {
		fail("安装不完整：找不到 app\\caulink-ui.exe", err)
	}

	cfgPath := filepath.Join(dir, "configs", "caulinkd.local.json")

	// 先把 8080 处理掉，配置才能决定 HTTP 代理开不开。
	proxyOK := true
	if !canBind(proxyAddr) {
		proxyOK = tryFree8080()
		if !proxyOK {
			writeLog(dir, "8080 仍被占用，本次关闭 HTTP 代理（Steam 多 IP 不可用）")
		}
	}

	if err := repairConfig(cfgPath, proxyOK); err != nil {
		writeLog(dir, "配置校正失败: "+err.Error())
		info("配置校正失败，界面可能显示「未连接」：\r\n\r\n" + err.Error())
	} else {
		writeLog(dir, "配置已校正: "+cfgPath)
	}

	cmd := exec.Command(uiExe)
	cmd.Dir = filepath.Join(dir, "app")
	cmd.Env = append(os.Environ(),
		"CAULINK_DAEMON="+filepath.Join(dir, "app", "caulinkd.exe"),
		"CAULINK_CONFIG="+cfgPath,
	)
	if err := cmd.Start(); err != nil {
		fail("启动界面失败", err)
	}
	// 不 Wait：启动器退出后界面继续运行。
}

// ---------------- 8080 冲突处理 ----------------

// tryFree8080 尝试腾出 8080。返回 true 表示端口已经可用。
func tryFree8080() bool {
	pid, name := ownerOf(proxyAddr)

	if isNIProcess(name) {
		msg := "检测到 NI 的应用服务正占用 127.0.0.1:8080。\r\n\r\n" +
			"占用进程：" + name + "（PID " + strconv.Itoa(pid) + "）\r\n\r\n" +
			"Steam 多 IP 下载需要这个端口。是否现在停止该服务，并设为「手动启动」？\r\n" +
			"（会弹出管理员授权；只影响 NI 的 Web 服务，下次打开 LabVIEW 等软件时它会自己起来）"
		if askYesNo("CAU WIFI HELPER · 端口被占用", msg) && elevatedStopNI() {
			// sc stop 是异步的，服务要时间释放端口。
			for i := 0; i < 40; i++ {
				if canBind(proxyAddr) {
					return true
				}
				time.Sleep(500 * time.Millisecond)
			}
		}
	} else if name != "" {
		info(fmt.Sprintf("127.0.0.1:8080 被 %s（PID %d）占用。\r\n\r\n"+
			"本次会关闭「Steam 多 IP」功能，SOCKS5、调度、认证不受影响。\r\n"+
			"需要该功能时，请先退出那个程序再重新启动本程序。", name, pid))
	} else {
		info("127.0.0.1:8080 被占用，但查不到占用进程。\r\n\r\n" +
			"本次会关闭「Steam 多 IP」功能，其余功能不受影响。")
	}

	return canBind(proxyAddr)
}

// isNIProcess 判断占用端口的进程是不是 NI 的 Web 服务宿主。
//
// 只认具体名字：早年写成 strings.Contains(name, "ni") 会误伤 wininit.exe
// 这类名字里恰好带 "ni" 的进程，弹出的"要不要停服务"就成了误导。
func isNIProcess(name string) bool {
	n := strings.ToLower(strings.TrimSuffix(name, ".exe"))
	return n == "niwebservicecontainer" || n == "niwebserver" || n == "nisvcloc"
}

// ownerOf 返回监听该地址的进程 PID 与进程名，查不到时返回 0 和空串。
func ownerOf(addr string) (int, string) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, ""
	}
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return 0, ""
	}
	suffix := ":" + port
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		// 协议 本地地址 外部地址 状态 PID
		if len(f) < 5 || !strings.EqualFold(f[3], "LISTENING") {
			continue
		}
		if strings.HasSuffix(f[1], suffix) {
			if p, err := strconv.Atoi(f[4]); err == nil && p > 0 {
				pid = p
				break
			}
		}
	}
	if pid == 0 {
		return 0, ""
	}
	tl, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return pid, ""
	}
	parts := strings.Split(string(tl), ",")
	if len(parts) == 0 {
		return pid, ""
	}
	return pid, strings.Trim(parts[0], "\" \r\n")
}

// elevatedStopNI 以管理员权限停止 NI Web 服务，并设为手动启动。
//
// 用 runas 拉起：启动器本身是 asInvoker，不动它的清单，
// 只在需要时提权一次，避免每次启动都弹 UAC。
func elevatedStopNI() bool {
	var script strings.Builder
	for _, name := range niServiceNames {
		script.WriteString(`sc stop "` + name + `" >nul 2>&1 & `)
		script.WriteString(`sc config "` + name + `" start= demand >nul 2>&1 & `)
	}
	script.WriteString("exit")
	return shellExec("runas", "cmd.exe", "/c "+script.String())
}

// ---------------- 配置校正 ----------------

func repairConfig(path string, proxyOK bool) error {
	cfg, err := readConfig(path)
	if err != nil {
		return err
	}

	local := localSources()
	if len(local) == 0 {
		return errors.New("没有找到可用的物理网卡（可能未联网）")
	}

	changed := false

	if !currentlyValid(cfg["sources"], local) {
		// 写 sources 时遵守 exclude_interfaces：VPN 虚拟网卡在这里就必须剔掉，
		// 否则它会被当成校园网源，认证器拿它反复登录门户（必然失败）。
		excluded := map[string]bool{}
		if raw, ok := cfg["exclude_interfaces"].([]any); ok {
			for _, item := range raw {
				if name, ok := item.(string); ok && name != "" {
					excluded[name] = true
				}
			}
		}
		arr := make([]any, 0, len(local))
		for _, s := range local {
			if excluded[s.Interface] {
				writeLog("", "跳过被排除的网卡: "+s.Interface)
				continue
			}
			arr = append(arr, map[string]any{
				"interface": s.Interface,
				"source_ip": s.SourceIP,
			})
		}
		cfg["sources"] = arr
		changed = true
	}

	if applyProxySetting(cfg, proxyOK) {
		changed = true
	}

	// 数据目录必须是安装根下的绝对路径。
	//
	// 守护进程的工作目录是 <安装根>\app：若配置里写相对 "data"，
	// 运行数据（含明文凭据）会落到 app\data，打包发布时极易被一起塞进去。
	// 这里每次都强制改写成绝对路径，老配置也能自动纠正。
	dataDir := filepath.Join(filepath.Dir(filepath.Dir(path)), "data")
	if cur, _ := cfg["data_dir"].(string); cur != dataDir {
		cfg["data_dir"] = dataDir
		changed = true
	}

	if !changed {
		return nil
	}
	return writeConfig(path, cfg)
}

// applyProxySetting 根据端口是否可用，打开或关闭 HTTP 代理段。
//
// 关掉而不是换个端口：桌面端把 127.0.0.1:8080 写死在 steamProxyAddr 里，
// 换端口会让"按住系统代理"指向一个不存在的代理，症状比直接没有这个功能更坏。
func applyProxySetting(cfg map[string]any, proxyOK bool) bool {
	hp, ok := cfg["http_proxy"].(map[string]any)
	if !ok {
		return false
	}
	addr, _ := hp["listen"].(string)
	switch {
	case !proxyOK && addr != "":
		hp["listen"] = ""
		return true
	case proxyOK && addr == "":
		// 上次因为端口被占用关掉了，这次腾出来了，恢复它。
		hp["listen"] = proxyAddr
		return true
	}
	return false
}

func readConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		var cfg map[string]any
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("解析 %s: %w", path, err)
		}
		return cfg, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	// 首次运行：给一份能直接跑的默认配置。账号密码留空，用户在界面里填。
	//
	// data_dir 必须写**安装根下的绝对路径**：守护进程的工作目录是 <安装根>\app，
	// 若这里写相对 "data"，运行数据（含明文凭据）会落到 app\data，
	// 打包发布时很容易被一起塞进发布包。踩过这个坑。
	return map[string]any{
		"data_dir": filepath.Join(filepath.Dir(filepath.Dir(path)), "data"),
		"auth": map[string]any{
			"enabled":          true,
			"binary":           "caulink-auth.exe",
			"username":         "",
			"password":         "",
			"realm":            "cau",
			"portal":           "10.3.191.9",
			"port":             801,
			"guardian_seconds": 30,
		},
		"health": map[string]any{
			"interval_seconds": 10,
			"timeout_seconds":  3,
			"target":           "www.msftconnecttest.com:80",
			"window":           10,
		},
		"socks5":     map[string]any{"listen": "127.0.0.1:1080"},
		"http_proxy": map[string]any{"listen": proxyAddr},
		"status":     map[string]any{"listen": "127.0.0.1:7788"},
	}, nil
}

func writeConfig(path string, cfg map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// currentlyValid 判断配置里的源是否仍存在于本机。
func currentlyValid(raw any, local []sourceConfig) bool {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return false
	}
	have := make(map[string]bool, len(local))
	for _, s := range local {
		have[s.SourceIP] = true
	}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return false
		}
		ip, _ := m["source_ip"].(string)
		if !have[ip] {
			return false
		}
	}
	return true
}

// localSources 枚举本机物理网卡上的 IPv4 地址。
func localSources() []sourceConfig {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []sourceConfig
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if virtualIface.MatchString(ifi.Name) || virtualIface.MatchString(ifi.HardwareAddr.String()) {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, sourceConfig{Interface: ifi.Name, SourceIP: ip4.String()})
			break // 每块网卡取第一个 IPv4
		}
	}
	return out
}

func canBind(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// ---------------- 系统弹窗与日志 ----------------

func exeDir() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(p), nil
}

func writeLog(dir, msg string) {
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(logDir, "launcher.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(time.Now().Format("2006-01-02 15:04:05 ") + strings.TrimSpace(msg) + "\n")
}

var (
	user32       = syscall.NewLazyDLL("user32.dll")
	shell32      = syscall.NewLazyDLL("shell32.dll")
	procMessage  = user32.NewProc("MessageBoxW")
	procShellExe = shell32.NewProc("ShellExecuteW")
)

const (
	mbOK       = 0x0
	mbYesNo    = 0x4
	mbIconInfo = 0x40
	mbIconWarn = 0x30
	mbTopMost  = 0x40000
	idYes      = 6
	swHide     = 0
)

// askYesNo 弹一个是/否对话框，选"是"返回 true。
func askYesNo(title, text string) bool {
	r := messageBox(text, title, mbYesNo|mbIconWarn|mbTopMost)
	return r == idYes
}

// info 弹一个提示框。
func info(text string) {
	messageBox(text, "CAU WIFI HELPER", mbOK|mbIconInfo|mbTopMost)
}

// fail 弹出致命错误再退出。
func fail(title string, err error) {
	messageBox(title+"\r\n\r\n"+err.Error(), "CAU WIFI HELPER", mbOK|mbIconWarn|mbTopMost)
	os.Exit(1)
}

func messageBox(text, title string, flags uintptr) int {
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString(title)
	r, _, _ := procMessage.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), flags)
	return int(r)
}

// shellExec 调 ShellExecuteW。verb 传 "runas" 时会触发 UAC。
func shellExec(verb, file, args string) bool {
	v, _ := syscall.UTF16PtrFromString(verb)
	f, _ := syscall.UTF16PtrFromString(file)
	a, _ := syscall.UTF16PtrFromString(args)
	r, _, _ := procShellExe.Call(0, uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(f)),
		uintptr(unsafe.Pointer(a)), 0, swHide)
	return r > 32 // ShellExecute 的约定：>32 表示成功
}
