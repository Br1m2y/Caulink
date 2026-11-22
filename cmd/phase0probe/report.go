package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RunRecord 是落盘的完整实验记录。报告模板里的每个数字都应该能从这份 JSON 追到。
type RunRecord struct {
	Tool       string            `json:"tool"`
	Version    string            `json:"version"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at"`
	Host       HostInfo          `json:"host"`
	TargetURL  string            `json:"target_url"`
	Params     Params            `json:"params"`
	Preflight  []PreflightRecord `json:"preflight"`
	Baseline   []BaselineRecord  `json:"baseline"`
	Concurrent ConcurrentRecord  `json:"concurrent"`
	Verdict    Verdict           `json:"verdict"`
}

type HostInfo struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	NumCPU   int    `json:"num_cpu"`
	GoVer    string `json:"go_version"`
}

type Params struct {
	ConnectionsPerIP int `json:"connections_per_ip"`
	WarmupSeconds    int `json:"warmup_seconds"`
	DurationSeconds  int `json:"duration_seconds"`
	Rounds           int `json:"rounds"`
}

type PreflightRecord struct {
	Iface   string `json:"interface"`
	IP      string `json:"source_ip"`
	Verdict string `json:"verdict"`
	Status  int    `json:"http_status"`
	Note    string `json:"note"`
}

// BaselineRecord 是单个源 IP 单独测速的结果（多轮 + 中位数）。
type BaselineRecord struct {
	Iface      string      `json:"interface"`
	IP         string      `json:"source_ip"`
	RoundsMBps []float64   `json:"rounds_mbps"`
	MedianMBps float64     `json:"median_mbps"`
	Detail     Measurement `json:"detail"`
}

// ConcurrentRecord 是所有源 IP 同时测速的结果。
type ConcurrentRecord struct {
	RoundsMBps []float64   `json:"rounds_mbps"`
	MedianMBps float64     `json:"median_mbps"`
	Detail     Measurement `json:"detail"`
}

func hostInfo() HostInfo {
	h, _ := os.Hostname()
	return HostInfo{
		Hostname: h,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		NumCPU:   runtime.NumCPU(),
		GoVer:    runtime.Version(),
	}
}

// writeOutputs 同时写 JSON（机器可读）和 Markdown（人能贴进报告）。
// 返回两个文件的路径。
func writeOutputs(rec RunRecord, dir string) (jsonPath, mdPath string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("创建输出目录 %s: %w", dir, err)
	}
	stamp := rec.StartedAt.Format("20060102-150405")

	jsonPath = filepath.Join(dir, "phase0-"+stamp+".json")
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("序列化结果: %w", err)
	}
	if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
		return "", "", fmt.Errorf("写 %s: %w", jsonPath, err)
	}

	mdPath = filepath.Join(dir, "phase0-"+stamp+".md")
	if err := os.WriteFile(mdPath, []byte(renderMarkdown(rec)), 0o644); err != nil {
		return jsonPath, "", fmt.Errorf("写 %s: %w", mdPath, err)
	}
	return jsonPath, mdPath, nil
}

func renderMarkdown(rec RunRecord) string {
	var b strings.Builder

	b.WriteString("# Phase 0 限速锚点验证报告（自动生成）\n\n")
	b.WriteString("> 本文由 `phase0probe` 自动生成，数据结论可直接引用；带 `TODO` 的部分需要人工补充。\n\n")

	b.WriteString("## 1. 实验环境\n\n")
	b.WriteString("| 项 | 值 |\n|---|---|\n")
	fmt.Fprintf(&b, "| 时间 | %s |\n", rec.StartedAt.Format("2006-01-02 15:04:05 -0700"))
	fmt.Fprintf(&b, "| 主机 | %s (%s/%s, %d 核) |\n", rec.Host.Hostname, rec.Host.OS, rec.Host.Arch, rec.Host.NumCPU)
	fmt.Fprintf(&b, "| 测速目标 | `%s` |\n", rec.TargetURL)
	fmt.Fprintf(&b, "| 每 IP 并发连接 | %d |\n", rec.Params.ConnectionsPerIP)
	fmt.Fprintf(&b, "| 预热 / 测量时长 | %d s / %d s |\n", rec.Params.WarmupSeconds, rec.Params.DurationSeconds)
	fmt.Fprintf(&b, "| 轮数 | %d |\n", rec.Params.Rounds)
	b.WriteString("\n")

	b.WriteString("## 2. 源 IP 预检\n\n")
	b.WriteString("| 源 IP | 网卡 | 结果 | 说明 |\n|---|---|---|---|\n")
	for _, p := range rec.Preflight {
		mark := "可用"
		switch p.Verdict {
		case "ok":
			mark = "可用"
		case "unauthenticated":
			mark = "**未认证**"
		case "network_fail":
			mark = "**链路不通**"
		default:
			mark = "目标异常"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", p.IP, p.Iface, mark, p.Note)
	}
	b.WriteString("\n未认证或链路不通的 IP 不参与后续测速，否则会得出 0 MB/s 并污染结论。\n\n")

	b.WriteString("## 3. 逐 IP 单独测速（基线）\n\n")
	b.WriteString("| 源 IP | 网卡 | 各轮 MB/s | 中位数 MB/s |\n|---|---|---|---|\n")
	for _, r := range rec.Baseline {
		fmt.Fprintf(&b, "| %s | %s | %s | **%.2f** |\n",
			r.IP, r.Iface, joinFloats(r.RoundsMBps), r.MedianMBps)
	}
	b.WriteString("\n")

	b.WriteString("## 4. 全部 IP 并发测速\n\n")
	b.WriteString("| 场景 | 各轮 MB/s | 中位数 MB/s |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| %d 个 IP 同时测 | %s | **%.2f** |\n",
		len(rec.Concurrent.Detail.PerTarget), joinFloats(rec.Concurrent.RoundsMBps), rec.Concurrent.MedianMBps)
	b.WriteString("\n")

	b.WriteString("并发时各 IP 的实际贡献：\n\n")
	b.WriteString("| 源 IP | 网卡 | 平均 MB/s | 请求数 |\n|---|---|---|---|\n")
	for _, d := range rec.Concurrent.Detail.PerTarget {
		fmt.Fprintf(&b, "| %s | %s | %.2f | %d |\n", d.IP, d.Iface, d.AvgMBps, d.Requests)
	}
	b.WriteString("\n")

	b.WriteString("## 5. 结论\n\n")
	conclusion := map[string]string{
		"ip_anchored":      "**限速锚定在 IP**",
		"account_anchored": "**限速锚定在账号**",
		"inconclusive":     "**不确定**",
	}[rec.Verdict.Conclusion]
	fmt.Fprintf(&b, "### %s\n\n", conclusion)
	fmt.Fprintf(&b, "%s\n\n", rec.Verdict.Sentence())

	b.WriteString("| 指标 | 值 |\n|---|---|\n")
	fmt.Fprintf(&b, "| 各 IP 基线之和 | %.2f MB/s |\n", rec.Verdict.SumBaselineMBps)
	fmt.Fprintf(&b, "| 最强单 IP 基线 | %.2f MB/s |\n", rec.Verdict.MaxBaselineMBps)
	fmt.Fprintf(&b, "| 并发总吞吐 | %.2f MB/s |\n", rec.Verdict.ConcurrentMBps)
	fmt.Fprintf(&b, "| 并发 / 基线之和 | %.2f |\n", rec.Verdict.RatioToSum)
	fmt.Fprintf(&b, "| 并发 / 最强单 IP | %.2f |\n", rec.Verdict.RatioToMax)
	fmt.Fprintf(&b, "| 数据是否足以支撑结论 | %v |\n", rec.Verdict.Valid)
	b.WriteString("\n")

	if len(rec.Verdict.Reasons) > 0 {
		b.WriteString("判定依据：\n\n")
		for _, r := range rec.Verdict.Reasons {
			fmt.Fprintf(&b, "- %s\n", r)
		}
		b.WriteString("\n")
	}

	b.WriteString("## 6. 待人工补充\n\n")
	b.WriteString("- [ ] TODO 实测时的校园网账号与设备情况（几个账号、几台设备在线）\n")
	b.WriteString("- [ ] TODO 单接口标称限速（约 6 MB/s）与实际测得的对比说明\n")
	b.WriteString("- [ ] TODO 多 IP 同时认证是否导致其他设备掉线（见《认证层设计.md》§12 第 5 项）\n")
	b.WriteString("- [ ] TODO 异常情况记录（选错目标、中途断线、后台有其他大流量程序等）\n")
	b.WriteString("- [ ] TODO 团队对结论的确认签字（A / B / C）\n\n")

	b.WriteString("## 7. 结论如何影响后续计划\n\n")
	b.WriteString("| 结论 | 下一步 |\n|---|---|\n")
	b.WriteString("| 限速锚定 IP | 继续「多 IP 聚合」主线，进入 Phase 1 核心骨架 |\n")
	b.WriteString("| 限速锚定账号 | 转入「多链路冗余、静默重认证、敏感流量固定最优 IP」的稳定性主线，调整 PPT 叙事 |\n")
	b.WriteString("| 不确定 | 换测速目标或加大并发连接数重跑，最多重跑 3 次；仍不确定则按「账号锚定」的保守方案推进 |\n")

	return b.String()
}

func joinFloats(vs []float64) string {
	if len(vs) == 0 {
		return "-"
	}
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = fmt.Sprintf("%.2f", v)
	}
	return strings.Join(parts, " / ")
}
