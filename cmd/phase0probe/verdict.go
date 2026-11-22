package main

import (
	"fmt"
	"sort"
)

// 判定阈值。这三个数字是整份报告的判断依据，改动必须同步改《验证报告》里的说明。
const (
	// ratioToSum 达到这个值，认为并发总吞吐已经接近"各 IP 单独测之和"，
	// 也就是每个 IP 各有独立配额 → 限速锚定在 IP。
	thresholdSumHigh = 0.80

	// ratioToMax 低于这个值，认为并发总吞吐没有超出单 IP 上限太多，
	// 也就是多个 IP 共享同一份配额 → 限速锚定在账号。
	thresholdMaxLow = 1.35

	// 单个 IP 的基线吞吐低于这个值（MB/s），说明瓶颈在测速目标、出口总带宽
	// 或采集环境（如晚高峰、同 AP 上百人抢空口），而不是校园网限速，
	// 此时任何"锚点"结论都不可信。
	//
	// 取值依据：闲时实测单 IP 约 5–6 MB/s。在机房（同楼层
	// 约 200 人上课）测得 1.00 / 0.65 MB/s、并发 1.20 MB/s，原 0.5 的下限
	// 让这组拥塞数据混过了可信性检查，被误判成"锚定账号"。
	thresholdMinBaseline = 2.0
)

// Verdict 是 Phase 0 的最终结论。
type Verdict struct {
	// Conclusion 取 ip_anchored / account_anchored / inconclusive 之一，
	// 与《要求.md》§4 "给出限速锚定 IP / 账号 / 不确定的明确结论" 对应。
	Conclusion string `json:"conclusion"`

	RatioToSum float64 `json:"ratio_to_sum"`
	RatioToMax float64 `json:"ratio_to_max"`

	SumBaselineMBps float64 `json:"sum_baseline_mbps"`
	MaxBaselineMBps float64 `json:"max_baseline_mbps"`
	ConcurrentMBps  float64 `json:"concurrent_mbps"`

	// Valid 表示这次实验的数据是否足以支撑结论。
	Valid   bool     `json:"valid"`
	Reasons []string `json:"reasons"`
}

// median 取中位数。多轮测量用中位数而不是平均值，
// 因为偶发的一次卡顿会把平均值拉低，中位数更抗抖动。
func median(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	s := make([]float64, len(vs))
	copy(s, vs)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// judge 根据"逐 IP 单独测"与"全部 IP 并发测"的结果给出限速锚点结论。
//
// 判断思路：
//   - 若限速锚定在 IP，每个 IP 有独立配额，并发总吞吐 ≈ 各 IP 单独测之和；
//   - 若限速锚定在账号，所有 IP 共享一份配额，并发总吞吐 ≈ 单 IP 的上限；
//   - 两者都不像，就老实说"不确定"，不要硬凑一个结论。
func judge(baselines []Measurement, concurrent Measurement) Verdict {
	v := Verdict{Conclusion: "inconclusive"}

	// 统计先算，不管后面走哪条分支都要填进报告 ——
	// 否则"数据不足"的结论会显示成一堆 0.00，看起来像测量失败。
	var sum, max float64
	for _, b := range baselines {
		if b.AvgMBps > max {
			max = b.AvgMBps
		}
		sum += b.AvgMBps
	}
	v.SumBaselineMBps = sum
	v.MaxBaselineMBps = max
	v.ConcurrentMBps = concurrent.AvgMBps
	if sum > 0 {
		v.RatioToSum = concurrent.AvgMBps / sum
	}
	if max > 0 {
		v.RatioToMax = concurrent.AvgMBps / max
	}

	if len(baselines) < 2 {
		v.Reasons = append(v.Reasons,
			fmt.Sprintf("只有 %d 个可用源 IP，无法对比；Phase 0 至少需要 2 个已认证的不同源 IP", len(baselines)))
		return v
	}

	if max < thresholdMinBaseline {
		v.Reasons = append(v.Reasons, fmt.Sprintf(
			"单个 IP 的最高基线只有 %.2f MB/s，低于 %.1f MB/s 的判定下限。瓶颈在测速目标、出口总带宽或链路质量，不在校园网限速。换一个更快、更近的测速目标重跑。",
			max, thresholdMinBaseline))
		return v
	}
	if concurrent.AvgMBps <= 0 {
		v.Reasons = append(v.Reasons, "并发测速没有产生有效流量，检查目标 URL 与预检结果")
		return v
	}

	switch {
	case v.RatioToSum >= thresholdSumHigh:
		v.Conclusion = "ip_anchored"
		v.Valid = true
		v.Reasons = append(v.Reasons, fmt.Sprintf(
			"并发总吞吐 %.2f MB/s，达到各 IP 单独测之和 %.2f MB/s 的 %.0f%%，说明每个 IP 有独立配额。",
			concurrent.AvgMBps, sum, v.RatioToSum*100))

	case v.RatioToMax <= thresholdMaxLow:
		v.Conclusion = "account_anchored"
		v.Valid = true
		v.Reasons = append(v.Reasons, fmt.Sprintf(
			"并发总吞吐 %.2f MB/s，只有最强单 IP %.2f MB/s 的 %.2f 倍，多 IP 没有叠加出带宽，说明配额锚定在账号。",
			concurrent.AvgMBps, max, v.RatioToMax))

	default:
		v.Reasons = append(v.Reasons, fmt.Sprintf(
			"并发总吞吐 %.2f MB/s，是各 IP 之和的 %.0f%%、是最强单 IP 的 %.2f 倍，落在两个阈值之间，无法判定。加大并发连接数、延长测量时间后重跑。",
			concurrent.AvgMBps, v.RatioToSum*100, v.RatioToMax))
	}

	// 基线之间差距过大时，即使结论成立也要提醒：可能有一个 IP 本身链路就差，
	// 这会同时压低 sum 和并发总量，让比例看起来"正常"但结论其实不可靠。
	minBaseline := max
	for _, b := range baselines {
		if b.AvgMBps < minBaseline {
			minBaseline = b.AvgMBps
		}
	}
	if minBaseline > 0 && max/minBaseline > 3 {
		v.Reasons = append(v.Reasons, fmt.Sprintf(
			"注意：各 IP 基线差距达 %.1f 倍（最强 %.2f / 最弱 %.2f MB/s），可能有 IP 链路质量本身较差，结论强度相应下降。",
			max/minBaseline, max, minBaseline))
	}

	return v
}

// Sentence 给出一句可直接写进报告的结论。
func (v Verdict) Sentence() string {
	switch v.Conclusion {
	case "ip_anchored":
		return fmt.Sprintf("限速锚定在 IP。多 IP 并发可实现吞吐叠加（并发 %.2f MB/s vs 单 IP 最高 %.2f MB/s），项目可按「多 IP 聚合」主线推进。",
			v.ConcurrentMBps, v.MaxBaselineMBps)
	case "account_anchored":
		return fmt.Sprintf("限速锚定在账号。多 IP 无法叠加带宽（并发 %.2f MB/s，仅单 IP 的 %.2f 倍），项目应转向「多链路冗余与稳定性优化」主线。",
			v.ConcurrentMBps, v.RatioToMax)
	default:
		return "结论不确定。当前数据不足以判定限速锚点，需调整测速目标或加大并发后重跑。"
	}
}
