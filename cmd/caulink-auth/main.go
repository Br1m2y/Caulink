// Command caulink-auth 是农大校园网（Dr.COM ePortal）的认证守护，由 internal/auth
// 以独立子进程方式拉起，每个源 IP 一个实例。
//
// 职责分两层：
//   - 稳态守护：周期检查在线状态，掉线自动重登（默认 30s，见 guardian_seconds）；
//   - 即时重认证：守护进程在代理层观察到上游失败时，会**直接**用同一份协议实现
//     （internal/drcom）登录，不必等这里的轮询。所以本进程的轮询只是兜底。
//
// 协议细节与踩过的坑见 internal/drcom 的包注释与 docs/adr/0004-drcom-auth.md。
// 本程序不打印密码，日志里账号脱敏。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Br1m2y/caulink/internal/drcom"
)

// config 与 internal/auth 生成的 auth.json 对齐（字段名即 JSON 契约）。
type config struct {
	Portal       string `json:"portal"`
	Port         int    `json:"port"`
	Username     string `json:"username"`
	Realm        string `json:"realm"`
	Password     string `json:"password"`
	SourceIP     string `json:"source_ip"`
	CheckSeconds int    `json:"check_seconds"`
	LogDir       string `json:"log_dir"`
}

func (c config) client() drcom.Client {
	return drcom.Client{
		Portal:   c.Portal,
		Port:     c.Port,
		Username: c.Username,
		Realm:    c.Realm,
		Password: c.Password,
		SourceIP: c.SourceIP,
	}
}

func main() {
	cfgPath := flag.String("config", "auth.json", "实例配置文件")
	iface := flag.String("interface", "", "网卡名（仅记录，实际绑定用配置里的 source_ip）")
	once := flag.Bool("once", false, "只执行一次「检查→必要时登录」后退出")
	logout := flag.Bool("logout", false, "注销该 IP 后退出")
	debug := flag.Bool("debug", false, "打印更多细节")
	flag.Parse()

	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		fatal("读配置失败: %v", err)
	}
	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fatal("解析配置失败: %v", err)
	}
	if cfg.Portal == "" || cfg.Username == "" || cfg.Password == "" {
		fatal("配置缺少 portal / username / password")
	}
	if net.ParseIP(cfg.SourceIP) == nil {
		fatal("配置里的 source_ip 非法: %q", cfg.SourceIP)
	}

	client := cfg.client()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logf("启动：账号=%s 源IP=%s 网卡=%s 门户=%s:%d",
		maskAccount(client.Account()), cfg.SourceIP, emptyDash(*iface), cfg.Portal, cfg.Port)

	switch {
	case *logout:
		if err := client.Logout(ctx); err != nil {
			fatal("注销失败: %v", err)
		}
		logf("已注销 %s", cfg.SourceIP)
	case *once:
		online, err := client.Probe(ctx)
		logf("当前状态：%s", boolToState(online, err))
		if !online {
			if err := client.Login(ctx); err != nil {
				fatal("登录失败: %v", err)
			}
			logf("登录成功")
		}
	default:
		runLoop(ctx, client, cfg, *debug)
	}
}

// runLoop 是兜底守护：周期检查，掉线重登。
//
// 主要的恢复路径是**事件驱动**（代理层发现上游失败 → 守护进程立刻重认证），
// 这里的周期检查用于兜住"没有流量、因而代理层看不到失败"的沉默掉线。
func runLoop(ctx context.Context, client drcom.Client, cfg config, debug bool) {
	interval := time.Duration(cfg.CheckSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	// 抖动：多个 IP 实例同时探测会给门户造成同步尖峰。
	jitter := interval / 5
	logf("兜底守护启动：检查周期 %s（抖动 %s）", interval, jitter)

	failures := 0
	for {
		online, err := client.Probe(ctx)
		if err != nil && debug {
			logf("在线检查异常: %v", err)
		}
		if !online {
			logf("兜底检测到离线（%s），开始重认证", boolToState(online, err))
			if err := client.Login(ctx); err != nil {
				failures++
				if failures > 5 {
					failures = 5
				}
				wait := time.Second * time.Duration(1<<uint(failures-1))
				if wait > 60*time.Second {
					wait = 60 * time.Second
				}
				logf("登录失败（第 %d 次）: %v；%s 后重试", failures, err, wait)
				if sleepCtx(ctx, wait) {
					return
				}
				continue
			}
			failures = 0
			logf("重认证成功，%s 已恢复在线", cfg.SourceIP)
		} else if debug {
			logf("在线，%s 后再次检查", interval)
		}

		wait := interval
		if jitter > 0 {
			wait += time.Duration(time.Now().UnixNano()%int64(jitter)) * time.Nanosecond
		}
		if sleepCtx(ctx, wait) {
			logf("收到退出信号，停止守护")
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}

func boolToState(online bool, err error) string {
	if err != nil {
		return "未知(" + err.Error() + ")"
	}
	if online {
		return "在线"
	}
	return "离线"
}

// maskAccount 只保留账号前 4 位，避免日志里出现完整学号。
func maskAccount(s string) string {
	if len(s) <= 4 {
		return "***"
	}
	return s[:4] + "***"
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s caulink-auth: %s\n",
		time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func fatal(format string, args ...any) {
	logf(format, args...)
	os.Exit(1)
}
