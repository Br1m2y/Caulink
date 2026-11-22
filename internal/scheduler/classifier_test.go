package scheduler

import "testing"

func TestRulesClassify(t *testing.T) {
	r := DefaultRules()

	cases := []struct {
		network, addr string
		want          Class
		note          string
	}{
		{"udp", "1.2.3.4:53", ClassInteractive, "UDP 默认视为敏感"},
		{"tcp", "1.2.3.4:3478", ClassInteractive, "STUN/TURN 端口"},
		{"tcp", "1.2.3.4:27015", ClassInteractive, "Source 引擎游戏端口"},
		{"tcp", "1.2.3.4:443", ClassBulk, "普通 HTTPS 归批量"},
		{"tcp", "mirror.example.com:80", ClassBulk, "镜像下载归批量"},
		{"tcp", "zoom.us:443", ClassInteractive, "会议域名后缀"},
		{"tcp", "us02web.zoom.us:443", ClassInteractive, "子域名也匹配后缀"},
		{"tcp", "meeting.qq.com:443", ClassInteractive, "腾讯会议"},
		{"tcp", "notzoom.us:443", ClassBulk, "不能被部分后缀误伤"},
	}

	for _, c := range cases {
		got := r.Classify(c.network, c.addr)
		if got != c.want {
			t.Errorf("%s %s（%s）= %s，期望 %s", c.network, c.addr, c.note, got, c.want)
		}
	}
}

func TestRulesHandleMalformedAddr(t *testing.T) {
	r := DefaultRules()
	// 拿不到端口时不能 panic，也不能瞎猜成敏感流量。
	for _, addr := range []string{"", "no-port", ":::", "host:"} {
		if got := r.Classify("tcp", addr); got != ClassBulk {
			t.Errorf("addr=%q 应归为 bulk，实际 %s", addr, got)
		}
	}
}

func TestRulesCanDisableUDPRule(t *testing.T) {
	r := DefaultRules()
	r.TreatUDPAsInteractive = false

	if got := r.Classify("udp", "1.2.3.4:9"); got != ClassBulk {
		t.Errorf("关闭 UDP 规则后应归为 bulk，实际 %s", got)
	}
	if got := r.Classify("udp", "1.2.3.4:3478"); got != ClassInteractive {
		t.Errorf("关闭 UDP 规则不影响端口规则，实际 %s", got)
	}
}

func TestRulesUppercaseNetwork(t *testing.T) {
	r := DefaultRules()
	if got := r.Classify("UDP", "1.2.3.4:9"); got != ClassInteractive {
		t.Errorf("network 大小写不应影响判定，实际 %s", got)
	}
}
