package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const toolVersion = "0.1.0"

func main() {
	var (
		cfgPath  = flag.String("config", "", "配置文件路径（JSON）。留空使用内置默认值")
		target   = flag.String("target", "", "覆盖测速目标 URL")
		conns    = flag.Int("conns", 0, "每个源 IP 的并发连接数（覆盖配置）")
		duration = flag.Int("duration", 0, "稳态测量时长，秒（覆盖配置）")
		rounds   = flag.Int("rounds", 0, "每个场景的重复轮数（覆盖配置）")
		outDir   = flag.String("out", "", "结果输出目录（覆盖配置）")
		dryRun   = flag.Bool("dry-run", false, "只做源 IP 枚举与连通性预检，不测速")
		showVer  = flag.Bool("version", false, "打印版本后退出")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("phase0probe", toolVersion)
		return
	}

	if err := run(*cfgPath, *target, *conns, *duration, *rounds, *outDir, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "\n错误: %v\n", err)
		os.Exit(1)
	}
}

func run(cfgPath, targetOverride string, conns, duration, rounds int, outDir string, dryRun bool) error {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	if targetOverride != "" {
		cfg.Targets = []string{targetOverride}
	}
	if conns > 0 {
		cfg.ConnectionsPerIP = conns
	}
	if duration > 0 {
		cfg.DurationSeconds = duration
	}
	if rounds > 0 {
		cfg.Rounds = rounds
	}
	if outDir != "" {
		cfg.OutputDir = outDir
	}
	if err := cfg.validate(); err != nil {
		return fmt.Errorf("配置无效: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startedAt := time.Now()

	// ---------- 阶段一：枚举源 IP ----------

	fmt.Println("== 阶段一：枚举本机源 IPv4 ==")
	all, err := listSources(cfg)
	if err != nil {
		return err
	}
	for _, s := range all {
		fmt.Printf("  %s\n", s)
	}
	fmt.Printf("共 %d 个候选源地址\n\n", len(all))

	// ---------- 阶段二：选目标 + 连通性预检 ----------

	fmt.Println("== 阶段二：连通性预检 ==")
	target, pre, usable, err := pickTarget(ctx, cfg, all)
	if err != nil {
		// 失败时也要把每个组合的预检结果打出来，否则用户只知道"不可用"，
		// 不知道是没认证、被墙、还是 URL 挂了。
		if len(pre) > 0 {
			fmt.Println("\n各源 IP 的预检结果：")
			printPreflight(pre)
		}
		return err
	}
	fmt.Printf("选定测速目标: %s\n\n", target)
	printPreflight(pre)
	fmt.Printf("\n%d 个源 IP 通过预检\n\n", len(usable))

	if len(usable) < 2 {
		fmt.Println("警告：可用源 IP 少于 2 个。没有对比对象，无法判定限速锚点。")
		fmt.Println("      常见原因：另一个 IP 未认证、或该网卡没有独立出口。")
		fmt.Println("      请先对每个 IP 各自完成校园网认证，再重跑本工具。")
		fmt.Println()
	}

	if dryRun {
		fmt.Println("--dry-run：预检完成，未执行测速。")
		return nil
	}
	if len(usable) == 0 {
		return fmt.Errorf("没有任何可用的源 IP，无法测速")
	}

	if len(usable) > cfg.MaxSourceIPs {
		fmt.Printf("只取前 %d 个源 IP 进行测速（max_source_ips）\n\n", cfg.MaxSourceIPs)
		usable = usable[:cfg.MaxSourceIPs]
	}

	// ---------- 阶段三：逐 IP 单独测（基线） ----------

	fmt.Println("== 阶段三：逐 IP 单独测速（基线）==")
	fmt.Printf("   每个 IP %d 条并发连接，预热 %ds，测量 %ds，共 %d 轮\n\n",
		cfg.ConnectionsPerIP, cfg.WarmupSeconds, cfg.DurationSeconds, cfg.Rounds)

	var baselines []BaselineRecord
	for _, s := range usable {
		rec := BaselineRecord{Iface: s.Iface, IP: s.IP.String()}
		for r := 0; r < cfg.Rounds; r++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Printf("  [%s] 第 %d/%d 轮 ... ", s.IP, r+1, cfg.Rounds)
			m := runMeasure(ctx, "baseline", []Source{s}, cfg.ConnectionsPerIP, cfg.warmup(), cfg.duration(), target)
			rec.RoundsMBps = append(rec.RoundsMBps, m.AvgMBps)
			rec.Detail = m
			fmt.Printf("%.2f MB/s\n", m.AvgMBps)
			if r < cfg.Rounds-1 {
				sleepCtx(ctx, cfg.cooldown())
			}
		}
		rec.MedianMBps = median(rec.RoundsMBps)
		fmt.Printf("  -> %s 中位数 %.2f MB/s\n\n", s.IP, rec.MedianMBps)
		baselines = append(baselines, rec)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sleepCtx(ctx, cfg.cooldown())
	}

	// ---------- 阶段四：全部 IP 并发测 ----------

	fmt.Println("== 阶段四：全部 IP 并发测速 ==")
	fmt.Printf("   %d 个 IP × %d 条连接 = %d 条并发 TCP\n\n",
		len(usable), cfg.ConnectionsPerIP, len(usable)*cfg.ConnectionsPerIP)

	var concRounds []float64
	var concDetail Measurement
	for r := 0; r < cfg.Rounds; r++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Printf("  第 %d/%d 轮 ... ", r+1, cfg.Rounds)
		m := runMeasure(ctx, "concurrent", usable, cfg.ConnectionsPerIP, cfg.warmup(), cfg.duration(), target)
		concRounds = append(concRounds, m.AvgMBps)
		concDetail = m
		fmt.Printf("%.2f MB/s\n", m.AvgMBps)
		if r < cfg.Rounds-1 {
			sleepCtx(ctx, cfg.cooldown())
		}
	}
	conc := ConcurrentRecord{
		RoundsMBps: concRounds,
		MedianMBps: median(concRounds),
		Detail:     concDetail,
	}
	fmt.Printf("  -> 并发中位数 %.2f MB/s\n\n", conc.MedianMBps)

	// 判定用中位数口径：把中位数塞回 Measurement，避免 judge 里再算一遍。
	for i := range baselines {
		baselines[i].Detail.AvgMBps = baselines[i].MedianMBps
	}
	concMeas := conc.Detail
	concMeas.AvgMBps = conc.MedianMBps

	// ---------- 阶段五：判定与输出 ----------

	v := judge(toMeasurements(baselines), concMeas)

	var preRecords []PreflightRecord
	for _, p := range pre {
		preRecords = append(preRecords, PreflightRecord{
			Iface:   p.Source.Iface,
			IP:      p.Source.IP.String(),
			Verdict: p.Verdict,
			Status:  p.Status,
			Note:    p.Note,
		})
	}

	rec := RunRecord{
		Tool:       "phase0probe",
		Version:    toolVersion,
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
		Host:       hostInfo(),
		TargetURL:  target,
		Params: Params{
			ConnectionsPerIP: cfg.ConnectionsPerIP,
			WarmupSeconds:    cfg.WarmupSeconds,
			DurationSeconds:  cfg.DurationSeconds,
			Rounds:           cfg.Rounds,
		},
		Preflight:  preRecords,
		Baseline:   baselines,
		Concurrent: conc,
		Verdict:    v,
	}

	jsonPath, mdPath, err := writeOutputs(rec, cfg.OutputDir)
	if err != nil {
		return err
	}

	fmt.Println("== 结论 ==")
	fmt.Println(v.Sentence())
	for _, r := range v.Reasons {
		fmt.Println("  -", r)
	}
	fmt.Println()
	fmt.Println("JSON  :", jsonPath)
	fmt.Println("REPORT:", mdPath)
	fmt.Println()
	fmt.Println("下一步：把生成的 md 补完 TODO 后提交到 docs/reports/，")
	fmt.Println("        然后按结论决定是走「多 IP 聚合」还是「冗余稳定性」主线。")

	return nil
}

// pickTarget 在所有候选目标里挑一个真正能用的。
//
// 不用配置里的第一个就完事：那个目标可能已经下线、被墙、或者被校园网门户劫持。
// 判据是"能让最多源 IP 通过预检"，而不是"第一个勉强能通的"。
func pickTarget(ctx context.Context, cfg Config, sources []Source) (
	target string, pre []PreflightResult, usable []Source, err error,
) {
	bestCount := -1

	for _, t := range cfg.Targets {
		var results []PreflightResult
		var ok []Source
		for _, s := range sources {
			r := preflight(ctx, s, t)
			results = append(results, r)
			if r.Verdict == PreOK {
				ok = append(ok, s)
			}
		}
		if len(ok) > bestCount {
			bestCount = len(ok)
			target, pre, usable = t, results, ok
		}
		if bestCount == len(sources) {
			// 所有源 IP 都能通，不用再试别的目标。
			break
		}
	}

	if bestCount <= 0 {
		// 把预检结果一并带回去，让调用方能打印出具体原因。
		return target, pre, usable, fmt.Errorf(
			"所有测速目标都不可用。检查：1) 网络是否连通；2) 目标 URL 是否失效；3) 是否需要用 -target 指定校园网内可访问的地址")
	}
	return target, pre, usable, nil
}

func toMeasurements(bs []BaselineRecord) []Measurement {
	out := make([]Measurement, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Detail)
	}
	return out
}

func printPreflight(pre []PreflightResult) {
	for _, p := range pre {
		mark := "可用  "
		if p.Verdict != PreOK {
			mark = "不可用"
		}
		fmt.Printf("  [%s] %-15s %-12s HTTP %-3d %s\n",
			mark, p.Source.IP.String(), p.Source.Iface, p.Status, p.Note)
	}
	fmt.Println()
}

func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
