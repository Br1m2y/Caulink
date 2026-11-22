package accel

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Br1m2y/caulink/internal/proxy/dialer"
)

func TestNormalizeDir(t *testing.T) {
	base := t.TempDir()

	// 带引号（从"属性"里复制出来的样子）
	quoted := `"` + filepath.Join(base, "quoted") + `"`
	got, err := NormalizeDir(quoted)
	if err != nil || got != filepath.Join(base, "quoted") {
		t.Fatalf("带引号路径应被接受: %v / %q", err, got)
	}

	// 末尾多余的反斜杠要清掉
	got, err = NormalizeDir(filepath.Join(base, "trail") + string(os.PathSeparator))
	if err != nil || got != filepath.Join(base, "trail") {
		t.Fatalf("尾部斜杠应被清理: %v / %q", err, got)
	}

	// 相对路径必须拒绝：它相对的是守护进程的工作目录，用户根本猜不到在哪
	if _, err := NormalizeDir("downloads"); err == nil {
		t.Fatal("相对路径应被拒绝（会落到守护进程的工作目录里）")
	}
	if _, err := NormalizeDir("   "); err == nil {
		t.Fatal("空路径应被拒绝")
	}

	// 目标是文件而不是目录
	f := filepath.Join(base, "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeDir(f); err == nil {
		t.Fatal("指向文件的路径应被拒绝")
	}
}

func TestNormalizeDirExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("没有 home 目录")
	}
	// 只验证展开逻辑，不创建真实目录：用 home 本身。
	got, err := NormalizeDir("~")
	if err != nil {
		t.Fatalf("~ 应该被接受: %v", err)
	}
	if got != filepath.Clean(home) {
		t.Fatalf("~ 应展开成 %q，实际 %q", home, got)
	}
}

func TestSetDefaultDirPersists(t *testing.T) {
	state := filepath.Join(t.TempDir(), "accel.json")
	dir := filepath.Join(t.TempDir(), "下载", "caulink")

	opts := Options{
		Pool:      testPool(t, 1),
		Dialer:    dialer.Dialer{Timeout: time.Second},
		StatePath: state,
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	m, err := NewManager(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}

	got, err := m.SetDefaultDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("返回 %q，期望 %q", got, dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("目录应被创建: %v", err)
	}

	// 新起一个 Manager，应当读到同一个目录
	m2, err := NewManager(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if d := m2.DefaultDir(); d != dir {
		t.Fatalf("重启后应记住 %q，实际 %q", dir, d)
	}

	// 配置损坏时不能崩，退回默认目录
	if err := os.WriteFile(state, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	m3, err := NewManager(context.Background(), opts)
	if err != nil {
		t.Fatalf("损坏的配置不应阻止启动: %v", err)
	}
	if d := m3.DefaultDir(); d == "" {
		t.Fatal("应有兜底目录")
	}
}

// 提交任务时显式带的目录，要顺手变成新的默认值。
func TestStartWithDirRemembersIt(t *testing.T) {
	state := filepath.Join(t.TempDir(), "accel.json")
	m, err := NewManager(context.Background(), Options{
		Pool:      testPool(t, 1),
		Dialer:    dialer.Dialer{Timeout: time.Second},
		StatePath: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "elsewhere")

	// URL 会在下载阶段失败，但我们只关心目录是否被记住
	if _, err := m.Start(Request{URL: "http://127.0.0.1:1/x.iso", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if got := m.DefaultDir(); got != dir {
		t.Fatalf("默认目录应变成 %q，实际 %q", dir, got)
	}
	b, err := os.ReadFile(state)
	if err != nil || !strings.Contains(string(b), "elsewhere") {
		t.Fatalf("应该落盘: %v / %s", err, b)
	}

	// 相对路径必须当场拒绝，不能等到下载时才炸
	if _, err := m.Start(Request{URL: "http://127.0.0.1:1/x.iso", Dir: "relative/dir"}); err == nil {
		t.Fatal("相对路径应被拒绝")
	}
}
