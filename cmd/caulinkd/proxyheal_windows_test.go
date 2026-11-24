package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// 启动自愈：有效租约应被读入并删除；损坏的租约应被清掉而不是卡住启动。
func TestHealProxyLeaseHandlesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lease.json")
	t.Setenv("CAULINK_PROXY_LEASE", p)
	if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	healProxyLease(slog.Default())
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("损坏的租约应被删除，实际 err=%v", err)
	}
}

// 没有租约文件时不应做任何事。
func TestHealProxyLeaseNoop(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAULINK_PROXY_LEASE", filepath.Join(dir, "absent.json"))
	healProxyLease(slog.Default())
}
