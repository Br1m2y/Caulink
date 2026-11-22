package main

import (
	"net"
	"os"
	"strconv"
	"testing"
)

// ownerOf 靠解析 netstat 输出拿 PID，格式随语言/系统版本变化，
// 解析错了就会把"端口被谁占了"说错，用户按错误提示去关进程。
// 这个测试保证在真机输出上能拿到正确结果。
func TestOwnerOfSelf(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close()

	addr := ln.Addr().String()
	pid, name := ownerOf(addr)
	if pid != os.Getpid() {
		t.Fatalf("应识别出本进程 PID %d，实际 %d（addr=%s）", os.Getpid(), pid, addr)
	}
	if name == "" {
		t.Fatalf("进程名不应为空（PID=%d）", pid)
	}
	t.Logf("addr=%s pid=%d name=%s", addr, pid, name)
}

func TestOwnerOfClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	pid, _ := ownerOf(addr)
	if pid != 0 {
		t.Fatalf("端口已关闭，不应有占用者，实际 %d", pid)
	}
}

func TestIsNIProcess(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"NIWebServiceContainer.exe", true},
		{"niwebservicecontainer", true},
		{"NIWebServer.exe", true},
		{"wininit.exe", false}, // 名字里带 "ni"，早期版本会误判
		{"python.exe", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isNIProcess(c.name); got != c.want {
			t.Errorf("isNIProcess(%q) = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// 端口探测：一个已被占用的地址必须返回 false。
func TestCanBind(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	if canBind(ln.Addr().String()) {
		t.Errorf("%s 已被监听，canBind 应为 false", ln.Addr().String())
	}
	// 找一个确定空闲的端口
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	free := probe.Addr().String()
	probe.Close()
	if !canBind(free) {
		t.Errorf("%s 空闲，canBind 应为 true", free)
	}
	_ = strconv.Itoa(0)
}

// 8080 腾不出来时必须关掉 HTTP 代理段：caulinkd 把端口绑定失败当致命错误，
// 不关就整个进程退出，用户只看到"连不上"。腾出来之后要能自动恢复。
func TestApplyProxySetting(t *testing.T) {
	newCfg := func(listen string) map[string]any {
		return map[string]any{"http_proxy": map[string]any{"listen": listen}}
	}
	get := func(cfg map[string]any) string {
		return cfg["http_proxy"].(map[string]any)["listen"].(string)
	}

	cfg := newCfg(proxyAddr)
	if !applyProxySetting(cfg, false) || get(cfg) != "" {
		t.Errorf("端口不可用时应关闭代理，实际 %q", get(cfg))
	}

	if !applyProxySetting(cfg, true) || get(cfg) != proxyAddr {
		t.Errorf("端口恢复后应重新打开，实际 %q", get(cfg))
	}

	// 已经是目标状态时不应报告"有改动"，否则每次启动都重写配置
	if applyProxySetting(cfg, true) {
		t.Error("状态未变时不应报告改动")
	}

	// 没有 http_proxy 段的老配置不能崩
	if applyProxySetting(map[string]any{}, false) {
		t.Error("缺少 http_proxy 段时应视为无改动")
	}
}
