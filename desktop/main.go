// CAU WIFI HELPER 桌面端（Wails v3 + WebView2）
//
// 前端是零构建的静态页面（frontend/app 内是原生 HTML/CSS/JS），
// 直接被 go:embed 打进 exe，运行时不需要 Node / npm。
//
// 数据来源：本机 caulinkd 守护进程的只读接口
//
//	GET http://127.0.0.1:7788/api/v1/status
//
// 探不到时前端自动退回内置演示数据（标题栏会显示「演示数据」）。
package main

import (
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/app
var assets embed.FS

func main() {
	// 发布构建是 -H windowsgui，没有控制台，崩溃信息会凭空消失。
	// 所以先把标准日志重定向到文件，再给致命错误加一个弹窗：
	// 这两件事必须在 application.New 之前完成，否则 WebView2 的报错就丢了。
	logPath := setupLogging()
	log.Printf("=== 启动 caulink-desktop ===")
	log.Printf("日志文件: %s", logPath)

	// 守护进程没跑就拉起来，否则面板只能显示演示数据。
	// 退出时只关自己启动的那个，用户手动开的不动。
	daemon := ensureDaemon()
	defer daemon.stop()

	// 退出兜底：用户开着开关直接关窗口时，在这里还原系统代理，
	// 避免把机器留在"代理指向 8080 但没人监听"的状态。
	defer restoreProxyLease()

	// 开发期调试开关：设置 CAU_DEBUG_PORT=9222 后 WebView2 会开放 CDP 端口，
	// 便于用 DevTools 或脚本检查页面状态。不设置时完全关闭，行为与生产一致。
	windowsOpts := application.WindowsOptions{}
	if port := os.Getenv("CAU_DEBUG_PORT"); port != "" {
		windowsOpts.AdditionalBrowserArgs = []string{"--remote-debugging-port=" + port}
	}

	// WebView2 需要一个可写目录放缓存。默认是 %APPDATA%\<exe名>，
	// 但在受限环境（沙箱、只读配置、漫游配置异常）里写不进去，
	// 表现是 CreateCoreWebView2Controller 返回 8000ffff——错误码毫无提示性。
	// 这里显式挑一个能写的目录，并在失败时回退，避免"双击没反应"。
	windowsOpts.WebviewUserDataPath = pickWritableDir(
		"webview2",
		filepath.Join(os.Getenv("LOCALAPPDATA"), "caulink-desktop", "webview2"),
		filepath.Join(os.Getenv("APPDATA"), "caulink-desktop", "webview2"),
	)
	log.Printf("WebView2 用户数据目录: %s", windowsOpts.WebviewUserDataPath)

	// 静态资源之外挂两个本地接口：设置页的"重启 Steam 以支持多 IP"开关要用。
	mux := http.NewServeMux()
	mux.HandleFunc("/api/steam-mode", handleSteamMode)
	mux.HandleFunc("/api/steam-mode/cancel", handleSteamMode)
	mux.Handle("/", application.BundledAssetFileServer(assets))

	app := application.New(application.Options{
		Name:        "CAU WIFI HELPER",
		Description: "校园网多路径接入状态面板",
		Windows:     windowsOpts,
		// 退出时还原系统代理。
		//
		// defer 在 os.Exit / 强制结束时不执行，所以必须在应用自己的
		// shutdown 回调里再做一次——这是"关掉软件就上不了网"的第二道保险。
		OnShutdown: restoreProxyLease,
		Assets: application.AssetOptions{
			// 用 Bundled 版本：除了静态文件，还会在 /wails/runtime.js 提供
			// 编译好的运行时（前端 shell.js 依赖它的 window.wails.Window）。
			Handler: mux,
		},
	})

	// 无系统标题栏，标题栏由前端自绘（56px，见 styles.css 的 .titlebar）。
	// 拖动依赖前端元素上的 CSS 变量 --wails-draggable: drag。
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "CAU WIFI HELPER",
		Width:            1440,
		Height:           900,
		MinWidth:         1024,
		MinHeight:        700,
		Frameless:        true,
		BackgroundColour: application.NewRGB(10, 12, 18),
		URL:              "/",
	})

	if err := app.Run(); err != nil {
		log.Printf("致命错误: %v", err)
		showError("启动失败", fmt.Sprintf("%v\n\n日志：%s", err, logPath))
		os.Exit(1)
	}
	log.Printf("=== 正常退出 ===")
}

// setupLogging 把标准日志写到可写的目录，返回最终使用的路径。
//
// 为什么需要它：Wails / WebView2 的报错都走标准 log 包，而 windowsgui 构建
// 没有控制台可以看。没有这个文件的话，"双击没反应"就只能靠猜。
func setupLogging() string {
	for _, dir := range candidateDirs() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		p := filepath.Join(dir, "desktop.log")
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			continue
		}
		log.SetOutput(f)
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		return p
	}
	return "(没有可写目录，日志已放弃)"
}

// candidateDirs 按优先级列出可写目录：用户数据目录 → exe 所在目录 → 临时目录。
func candidateDirs() []string {
	var dirs []string
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		dirs = append(dirs, filepath.Join(v, "caulink-desktop"))
	}
	if v := os.Getenv("APPDATA"); v != "" {
		dirs = append(dirs, filepath.Join(v, "caulink-desktop"))
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if v := os.Getenv("TEMP"); v != "" {
		dirs = append(dirs, filepath.Join(v, "caulink-desktop"))
	}
	return dirs
}

// pickWritableDir 返回第一个能真正写入的目录；都写不了时返回空串
// （交给 Wails 用它自己的默认值）。
func pickWritableDir(name string, preferred ...string) string {
	candidates := append(preferred, candidateDirs()...)
	for _, base := range candidates {
		if base == "" {
			continue
		}
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		// MkdirAll 成功不代表能写：受限环境里目录可能已存在但不可写，
		// 所以真写一个探针文件确认。
		probe := filepath.Join(dir, ".write-test")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
			continue
		}
		_ = os.Remove(probe)
		return dir
	}
	return ""
}
