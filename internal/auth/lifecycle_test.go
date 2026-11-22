package auth

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// TestMain 兼作"假 BitSrunLoginGo"。
//
// 子进程是本测试二进制本身，通过环境变量让它扮演不同的行为。
// 这样不用依赖真实二进制和真实校园网，就能验证进程托管的全套逻辑。
func TestMain(m *testing.M) {
	switch os.Getenv("FARMLINK_TEST_HELPER") {
	case "":
		os.Exit(m.Run())
	case "run":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "exit1":
		os.Exit(1)
	default:
		os.Exit(0)
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		BinaryPath:    os.Args[0],
		DataDir:       t.TempDir(),
		Account:       Account{Username: "2023001", Password: "p", Realm: "cau", Portal: "10.3.191.9", Port: 801},
		ProbeURL:      "http://127.0.0.1:1/never", // 探测必然失败，避免测试真的打外网
		ProbeInterval: time.Hour,
		StopTimeout:   2 * time.Second,
		Logger:        quietLogger(),
	}
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待 %s 超时", what)
}

func TestSupervisorStartsAndStops(t *testing.T) {
	t.Setenv("FARMLINK_TEST_HELPER", "run")

	m, err := New(testOptions(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	sup := m.(*Supervisor)
	entry := testEntry()

	if err := m.Start(context.Background(), entry); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 进程起来后状态应先进入 reauthenticating（等待探测确认），而不是直接 online。
	waitFor(t, "reauthenticating", 5*time.Second, func() bool {
		st, _ := m.Status(entry.ID)
		return st == contract.AuthReauthenticating
	})

	// 重复 Start 必须幂等，不能拉起第二个进程。
	if err := m.Start(context.Background(), entry); err != nil {
		t.Fatalf("重复 Start 应返回 nil，实际: %v", err)
	}

	if got := len(sup.workers); got != 1 {
		t.Fatalf("应只有 1 个实例，实际 %d 个", got)
	}

	if err := m.Stop(entry.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	st, _ := m.Status(entry.ID)
	if st != contract.AuthOffline {
		t.Fatalf("Stop 后应为 offline，实际 %s", st)
	}

	// Stop 不存在的实例也应返回 nil（幂等）。
	if err := m.Stop("不存在@1.2.3.4"); err != nil {
		t.Fatalf("Stop 不存在的实例应返回 nil，实际: %v", err)
	}
}

func TestSupervisorEmitsEventOnStart(t *testing.T) {
	t.Setenv("FARMLINK_TEST_HELPER", "run")

	m, err := New(testOptions(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	entry := testEntry()
	if err := m.Start(context.Background(), entry); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case e := <-m.Events():
		if e.ID != entry.ID {
			t.Errorf("事件 ID 应为 %s，实际 %s", entry.ID, e.ID)
		}
		if e.Next != contract.AuthReauthenticating {
			t.Errorf("首个事件的 Next 应为 reauthenticating，实际 %s", e.Next)
		}
		if e.Prev != contract.AuthOffline {
			t.Errorf("首个事件的 Prev 应为 offline，实际 %s", e.Prev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("没有收到状态变更事件")
	}
}

// 启动即退必须被判成配置错误并熔断，而不是进入无限重启。
// 无限重启会反复冲击认证服务器，还可能把账号推进风控。
func TestSupervisorTripsOnImmediateExit(t *testing.T) {
	t.Setenv("FARMLINK_TEST_HELPER", "exit1")

	m, err := New(testOptions(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	sup := m.(*Supervisor)
	entry := testEntry()

	if err := m.Start(context.Background(), entry); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	var lastStatus contract.AuthStatus
	var lastReason string
	for time.Now().Before(deadline) {
		lastStatus, _ = m.Status(entry.ID)
		lastReason = sup.Reason(entry.ID)
		if lastStatus == contract.AuthOffline && strings.Contains(lastReason, "配置错误") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("应熔断为 offline 且原因含\"配置错误\"，实际 status=%s reason=%q", lastStatus, lastReason)
}

func TestStartRejectsBadEntry(t *testing.T) {
	t.Setenv("FARMLINK_TEST_HELPER", "run")

	m, err := New(testOptions(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()

	cases := map[string]contract.IPPoolEntry{
		"空 ID":  {Interface: "eth0", SourceIP: "10.0.0.2"},
		"空网卡":   {ID: "x@10.0.0.2", SourceIP: "10.0.0.2"},
		"IP 非法": {ID: "x@bad", Interface: "eth0", SourceIP: "not-an-ip"},
		"回环地址":  {ID: "lo@127.0.0.1", Interface: "lo", SourceIP: "127.0.0.1"},
		"未指定地址": {ID: "x@0.0.0.0", Interface: "eth0", SourceIP: "0.0.0.0"},
	}
	for name, e := range cases {
		if err := m.Start(context.Background(), e); err == nil {
			t.Errorf("%s：应返回错误，实际 nil", name)
		}
	}
}

func TestNewRejectsMissingBinary(t *testing.T) {
	opts := Options{
		BinaryPath: "/definitely/not/here/bitsrun",
		DataDir:    t.TempDir(),
		Account:    Account{Username: "u", Password: "p"},
		Logger:     quietLogger(),
	}
	if _, err := New(opts); err == nil {
		t.Fatal("二进制不存在时 New 应报错")
	}
}

func TestNewRejectsEmptyAccount(t *testing.T) {
	opts := Options{
		BinaryPath: os.Args[0],
		DataDir:    t.TempDir(),
		Account:    Account{Username: "", Password: ""},
		Logger:     quietLogger(),
	}
	if _, err := New(opts); err == nil {
		t.Fatal("账号为空时 New 应报错")
	}
}
