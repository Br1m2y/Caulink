package main

import (
	"strings"
	"testing"
)

// SSID 里的 XML 特殊字符必须转义，否则 netsh 会拒绝整份配置。
func TestBuildWifiProfileXMLEscapesSSID(t *testing.T) {
	out := buildWifiProfileXML(`A&B<C>"D'`)
	if strings.Contains(out, "A&B<C>") {
		t.Fatalf("原始字符未被转义: %s", out)
	}
	for _, want := range []string{
		"A&amp;B&lt;C&gt;&quot;D&apos;",
		"<authentication>open</authentication>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少 %q，实际输出：%s", want, out)
		}
	}
}

// 普通 SSID 要原样写入 name 与 SSID 两处。
func TestBuildWifiProfileXMLContainsSSIDTwice(t *testing.T) {
	out := buildWifiProfileXML("CAU")
	if got := strings.Count(out, "<name>CAU</name>"); got != 2 {
		t.Fatalf("期望 name 出现 2 次，实际 %d", got)
	}
}

// 文件名必须只含安全字符，且空串要有兜底。
func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"CAU":    "CAU",
		"CAU-5G": "CAU-5G",
		"校园网":    "___",
		"":       "ssid",
		"a/b\\c": "a_b_c",
	}
	for in, want := range cases {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("sanitizeFileName(%q) = %q, 期望 %q", in, got, want)
		}
	}
}
