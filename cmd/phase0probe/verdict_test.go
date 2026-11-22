package main

import (
	"testing"
)

func meas(v float64) Measurement { return Measurement{AvgMBps: v} }

func TestJudgeIPAnchored(t *testing.T) {
	// 两个 IP 各 6 MB/s，并发跑到 11.8 ≈ 两者之和 → 独立配额
	v := judge([]Measurement{meas(6.0), meas(6.1)}, meas(11.8))
	if v.Conclusion != "ip_anchored" {
		t.Fatalf("期望 ip_anchored，得到 %s（ratio_sum=%.2f ratio_max=%.2f）",
			v.Conclusion, v.RatioToSum, v.RatioToMax)
	}
	if !v.Valid {
		t.Fatal("ip_anchored 时 Valid 应为 true")
	}
}

func TestJudgeAccountAnchored(t *testing.T) {
	// 两个 IP 各 6 MB/s，并发只有 6.1 ≈ 单个 IP 上限 → 共享配额
	v := judge([]Measurement{meas(6.0), meas(6.1)}, meas(6.1))
	if v.Conclusion != "account_anchored" {
		t.Fatalf("期望 account_anchored，得到 %s（ratio_sum=%.2f ratio_max=%.2f）",
			v.Conclusion, v.RatioToSum, v.RatioToMax)
	}
}

func TestJudgeInconclusiveWhenBetweenThresholds(t *testing.T) {
	// 并发 9 MB/s：是单 IP 的 1.5 倍，但又不到两者之和的 80%
	v := judge([]Measurement{meas(6.0), meas(6.0)}, meas(9.0))
	if v.Conclusion != "inconclusive" {
		t.Fatalf("期望 inconclusive，得到 %s（ratio_sum=%.2f ratio_max=%.2f）",
			v.Conclusion, v.RatioToSum, v.RatioToMax)
	}
	if v.Valid {
		t.Fatal("inconclusive 时 Valid 应为 false")
	}
}

func TestJudgeNeedsTwoIPs(t *testing.T) {
	v := judge([]Measurement{meas(6.0)}, meas(6.0))
	if v.Conclusion != "inconclusive" {
		t.Fatalf("单个 IP 时应为 inconclusive，得到 %s", v.Conclusion)
	}
	// 即使结论是"数据不足"，统计值也必须填上，否则报告里全是 0.00
	if v.MaxBaselineMBps != 6.0 {
		t.Fatalf("MaxBaselineMBps 应为 6.0，得到 %.2f", v.MaxBaselineMBps)
	}
	if v.ConcurrentMBps != 6.0 {
		t.Fatalf("ConcurrentMBps 应为 6.0，得到 %.2f", v.ConcurrentMBps)
	}
}

func TestJudgeRejectsSlowTarget(t *testing.T) {
	// 单 IP 只有 0.2 MB/s，瓶颈不在校园网限速，任何结论都不可信
	v := judge([]Measurement{meas(0.2), meas(0.2)}, meas(0.35))
	if v.Conclusion != "inconclusive" {
		t.Fatalf("基线过低时应为 inconclusive，得到 %s", v.Conclusion)
	}
	if len(v.Reasons) == 0 {
		t.Fatal("应给出基线过低的原因说明")
	}
}

// 机房实测复现：同楼层约 200 人上课，晚高峰拥塞，
// 单 IP 只有 1.00 / 0.65 MB/s、并发 1.20 MB/s。比值看似"锚定账号"，
// 实际是环境受限，必须判 inconclusive，否则会得出错误的产品方向。
func TestJudgeRejectsCongestedBaseline(t *testing.T) {
	v := judge([]Measurement{meas(1.00), meas(0.65)}, meas(1.20))
	if v.Conclusion != "inconclusive" {
		t.Fatalf("晚高峰拥塞数据应判 inconclusive，得到 %s（ratio_max=%.2f）",
			v.Conclusion, v.RatioToMax)
	}
	if v.Valid {
		t.Fatal("基线低于下限时 Valid 必须为 false")
	}
	if len(v.Reasons) == 0 {
		t.Fatal("应说明基线过低、瓶颈不在校园网限速")
	}
}

func TestMedian(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{[]float64{5.0, 6.0, 7.0}, 6.0},
		{[]float64{7.0, 5.0, 6.0, 8.0}, 6.5},
		{[]float64{6.0}, 6.0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := median(c.in); got != c.want {
			t.Errorf("median(%v) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

func TestMedianResistsOutlier(t *testing.T) {
	// 一次卡顿把某一轮拉低，中位数不应该被带跑
	got := median([]float64{6.0, 6.1, 1.2})
	if got != 6.0 {
		t.Errorf("中位数应抗离群值，得到 %.2f", got)
	}
}
