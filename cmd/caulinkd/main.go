package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Br1m2y/caulink/internal/accel"
	"github.com/Br1m2y/caulink/internal/auth"
	"github.com/Br1m2y/caulink/internal/control"
	"github.com/Br1m2y/caulink/internal/health"
	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/metrics"
	"github.com/Br1m2y/caulink/internal/proxy/dialer"
	"github.com/Br1m2y/caulink/internal/proxy/httpproxy"
	"github.com/Br1m2y/caulink/internal/proxy/socks5"
	"github.com/Br1m2y/caulink/internal/scheduler"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// version 会在构建时通过 -ldflags 覆盖，用于让 UI 分辨"跑的是哪一版"。
var version = "0.1.0-dev"

// placeholderScore 是"已认证但还没测过"时的占位健康分。
//
// 不给这个值，启动后的头几秒 health 还没探测完，所有 IP 的 HealthScore 都是 0，
// Schedulable() 返回空，SOCKS5 会直接回"网络不可达"。用户看到的就是"刚启动连不上"。
const placeholderScore = 0.5

func main() {
	var (
		cfgPath = flag.String("config", "configs/caulinkd.json", "配置文件路径")
		listen  = flag.String("listen", "", "覆盖 SOCKS5 监听地址，如 127.0.0.1:1080")
		noAuth  = flag.Bool("no-auth", false, "不启动认证守护，直接把 sources 当作已认证（开发/演示用）")
		verbose = flag.Bool("v", false, "输出调试日志")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}
	if *listen != "" {
		cfg.Socks5.Listen = *listen
	}
	if *noAuth {
		cfg.Auth.Enabled = false
	}

	if err := cfg.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "配置无效: %v\n", err)
		os.Exit(1)
	}

	if err := run(cfg, logger); err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}
}

func run(cfg Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool := ippool.New()

	// 校园网是 DHCP，网卡重连后 IP 会变。用网卡当前的地址校正配置，
	// 否则会留下一个绑不上的源，界面上表现为某个端口永远"认证中"。
	cfg.Sources = resolveSources(cfg.Sources, cfg.ExcludeInterfaces, logger)

	entries := make([]contract.IPPoolEntry, 0, len(cfg.Sources))
	for _, s := range cfg.Sources {
		e := contract.IPPoolEntry{
			// ID 的约定（"<网卡>@<源IP>"）写在 contract 的类型注释里，
			// 契约包刻意不提供构造函数 —— 它只放类型与校验，不放业务辅助方法。
			ID:         s.Interface + "@" + s.SourceIP,
			Interface:  s.Interface,
			SourceIP:   s.SourceIP,
			SourceIP6:  s.SourceIP6,
			AuthStatus: contract.AuthOffline,
		}
		if err := pool.Upsert(e); err != nil {
			return fmt.Errorf("初始化 IP 池: %w", err)
		}
		entries = append(entries, e)
	}

	// ---------- 统计 ----------
	//
	// 采样周期固定 1 秒：速率的分辨率由它决定，再密只是徒增计算量，
	// 人眼在 UI 上看不出差别。EWMA 已经在 metrics 内部做了平滑。
	//
	// 放在认证层之前：认证层的自适应探测要读它，判断"当前有没有人在用网"。
	registry := metrics.New()
	go registry.StartSampler(ctx, time.Second)

	// ---------- 认证层 ----------

	// dialFailure 是代理层 → 认证层的即时信号。默认 nil（认证关闭时不接线）。
	//
	// 为什么放在这里：认证启用时它指向 reauth.Watcher，代理任何一次建连失败
	// 都会变成一次"可能要重认证"的候选；认证关闭时保持 nil，拨号路径零开销。
	var dialFailure func(sourceIP string)

	// 认证控制器：界面上的"账号/密码/登录"由它驱动。
	//
	// 账号来源有三种，优先级从高到低：
	//   1. 用户在本机登录过并勾选了"记住密码"（data/credentials.json）
	//   2. 配置文件里的 auth.username / auth.password
	//   3. 都没有 → 退化成"假设已认证"，仅用于开发演示
	authCtl := newAuthController(ctx, logger, auth.Options{
		BinaryPath:       cfg.Auth.Binary,
		DataDir:          cfg.DataDir,
		GuardianDuration: cfg.Auth.guardian(),
		// 自适应探测：有流量时每 2 秒探一次（掉线用户立刻有感），
		// 空闲时退回 30 秒（没人用，省数据）。探测一次约 600 字节，
		// 有流量时相对下载本身可以忽略，空闲时则要克制。
		ActivityFunc: func() bool {
			var conns int64
			var bps float64
			for _, s := range registry.Snapshot() {
				conns += s.ActiveConns
				bps += s.Rate.UpBps + s.Rate.DownBps
			}
			return conns > 0 || bps > 32*1024
		},
		Account: auth.Account{
			Username: cfg.Auth.Username,
			Password: cfg.Auth.Password,
			Realm:    cfg.Auth.Realm,
			Portal:   cfg.Auth.Portal,
			Port:     cfg.Auth.Port,
		},
		Logger: logger,
	}, entries, pool, cfg.DataDir)
	defer authCtl.Close()

	// 重新扫描：用户在界面上点「重新扫描」时，按网卡现状校正源列表。
	srcCtl := newSourceController(ctx, logger, cfg.Sources, cfg.ExcludeInterfaces, pool, authCtl)

	// 通用"按住系统代理"：让只认系统代理的程序（迅雷等）也走多 IP。
	holdCtl := newProxyHoldController(logger, cfg.HTTPProxy.Listen)
	defer func() { _ = holdCtl.Stop() }()

	authActive := false
	if cfg.Auth.Enabled {
		ok, err := authCtl.Bootstrap()
		if err != nil {
			logger.Error("恢复认证失败，退回演示模式", "err", err)
		}
		authActive = ok
		if ok {
			dialFailure = authCtl.OnDialFailure
			logger.Info("事件驱动重认证已启用：代理层建连失败将立即触发重认证")
		}
	}

	if !authActive {
		// 没有账号密码时，如实标记为"未认证"。
		//
		// 早期这里是"假设已认证"，结果面板把两个 IP 都显示成正常，
		// 其中一个其实根本连不上——用户看到的是假数据。
		// 现在界面已经能登录，就让它真实反映状态：
		// 没登录 = 未认证，登录后由认证层把状态改成在线。
		for _, e := range entries {
			if err := pool.UpdateAuth(e.ID, contract.AuthOffline, time.Now()); err != nil {
				return err
			}
		}
		logger.Warn("尚未登录：请在界面「校园网账号」处填写账号密码（勾选记住密码可自动重连）")
	}

	// ---------- 健康检查 ----------

	prober, err := health.New(pool, health.Options{
		Interval: cfg.Health.interval(),
		Timeout:  cfg.Health.timeout(),
		Target:   cfg.Health.Target,
		Window:   cfg.Health.Window,
		Logger:   logger,
	})
	if err != nil {
		return fmt.Errorf("初始化健康检查: %w", err)
	}
	go prober.Run(ctx)

	// ---------- 调度 ----------

	sched, err := scheduler.New(pool, scheduler.DefaultRules())
	if err != nil {
		return fmt.Errorf("初始化调度器: %w", err)
	}

	// ---------- 代理 ----------

	srv := &socks5.Server{
		ListenAddr: cfg.Socks5.Listen,
		Picker:     sched,
		Dialer:     dialer.Dialer{Timeout: 10 * time.Second, OnFailure: dialFailure},
		Metrics:    registry,
		Logger:     logger,
	}

	srvErr := make(chan error, 2)
	go func() { srvErr <- srv.ListenAndServe(ctx) }()

	// ---------- HTTP 代理（可选） ----------
	//
	// Steam 只支持 HTTP 代理，不支持 SOCKS5；没有这个口，Steam 的多连接下载
	// 就进不了多 IP 调度。默认关闭，配置了 listen 才启动。
	if cfg.HTTPProxy.Listen != "" {
		var upstream *httpproxy.Upstream
		if cfg.HTTPProxy.Upstream != nil {
			upstream = &httpproxy.Upstream{
				Proxy:   cfg.HTTPProxy.Upstream.Proxy,
				Domains: cfg.HTTPProxy.Upstream.Domains,
			}
			logger.Info("HTTP 代理启用上游分流",
				"upstream", upstream.Proxy, "domains", strings.Join(upstream.Domains, ","))
		}
		httpSrv := &httpproxy.Server{
			ListenAddr: cfg.HTTPProxy.Listen,
			Picker:     sched,
			Dialer:     dialer.Dialer{Timeout: 10 * time.Second, OnFailure: dialFailure},
			Metrics:    registry,
			Upstream:   upstream,
			Logger:     logger,
		}
		go func() { srvErr <- httpSrv.ListenAndServe(ctx) }()
	}

	// ---------- 双通道加速下载 ----------
	//
	// 它自己就是一个下载器，因此必须在客户端侧：代理的 CONNECT 隧道里是 TLS
	// 密文，看不到 HTTP 头，也就没法插 Range 把一条下载拆成多段。
	//
	// 复用同一个 dialer（源地址绑定 + 出口网卡绑定），所以"每片落在不同 IP"
	// 和代理走的是同一套选路规则，不会出现"面板显示两条线、下载只走一条"。
	accelMgr, err := accel.NewManager(ctx, accel.Options{
		Pool:    pool,
		Dialer:  dialer.Dialer{Timeout: 15 * time.Second, OnFailure: dialFailure},
		Metrics: registry,
		// 记住用户在界面上选的保存目录：只存一个字段的小文件，
		// 但省掉"每次启动都要重新选"的烦人体验。
		StatePath: filepath.Join(cfg.DataDir, "accel.json"),
		Logger:    logger,
	})
	if err != nil {
		return fmt.Errorf("初始化加速下载: %w", err)
	}

	// ---------- 状态接口 ----------
	//
	// 启动失败不拖垮主流程：端口被占用是常见情况，代理仍然可用，
	// 只是 UI 拿不到数据。这时候正确的做法是让用户看到明确的日志，
	// 而不是让整个进程起不来。
	if cfg.Status.Listen != "" {
		statusSrv := &control.StatusServer{
			Listen:        cfg.Status.Listen,
			Pool:          pool,
			Metrics:       registry,
			Scheduler:     sched,
			Socks5Addr:    cfg.Socks5.Listen,
			HTTPProxyAddr: cfg.HTTPProxy.Listen,
			Steam:         newSteamController(logger, cfg.HTTPProxy.Listen),
			Auth:          authCtl,
			Sources:       srcCtl,
			ProxyHold:     holdCtl,
			Accel:         accelMgr,
			Version:       version,
			Logger:        logger,
		}
		go func() {
			if err := statusSrv.ListenAndServe(ctx); err != nil {
				logger.Error("状态接口启动失败，控制面板将无法读取数据",
					"addr", cfg.Status.Listen, "err", err)
			}
		}()
	}

	logger.Info("caulinkd 已启动",
		"socks5", cfg.Socks5.Listen,
		"status", cfg.Status.Listen,
		"sources", len(entries),
		"auth", cfg.Auth.Enabled,
		"version", version)
	for _, e := range entries {
		logger.Info("源 IP 已加入调度池", "id", e.ID, "iface", e.Interface)
	}

	select {
	case err := <-srvErr:
		if err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		logger.Info("收到退出信号，正在关闭")
		return nil
	}
}

// consumeAuthEvents 把认证层的状态变更写进 IP 池。
//
// 认证层是 AuthStatus 的唯一写入方，这里是唯一的搬运工。
// 用事件而不是轮询：轮询需要定一个周期，而"多久刷一次"和"状态多久变一次"
// 是两个无关的量，硬凑在一起必然有一边不合适。
func consumeAuthEvents(ctx context.Context, mgr auth.Manager, pool *ippool.Pool, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-mgr.Events():
			if !ok {
				return
			}
			if err := pool.UpdateAuth(e.ID, e.Next, e.At); err != nil {
				// IP 可能已被移除，属于正常情况，不值得报错刷屏。
				logger.Debug("回写认证状态失败", "id", e.ID, "err", err)
				continue
			}
			logger.Info("认证状态变化",
				"id", e.ID,
				"from", string(e.Prev),
				"to", string(e.Next),
				"reason", e.Reason)
		}
	}
}
