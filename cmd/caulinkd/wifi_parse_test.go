package main

import (
	"testing"
)

// netsh 输出值与本机网卡名取交集，应只认真正的接口名。
func TestWifiNamesIntersection(t *testing.T) {
	raw := "There is 1 interface on the system: \r\n\r\n" +
		"    名称                   : WLAN\r\n" +
		"    Description            : Intel(R) Wi-Fi 6E AX211 160MHz\r\n" +
		"    State                  : connected\r\n"
	real := map[string]bool{"WLAN": true, "以太网": true}
	got := intersectNetshValues(raw, real)
	if !got["WLAN"] {
		t.Fatalf("应识别出 WLAN，实际 %v", got)
	}
	if got["Intel(R) Wi-Fi 6E AX211 160MHz"] {
		t.Fatalf("描述字段不应被当成网卡名")
	}
}
