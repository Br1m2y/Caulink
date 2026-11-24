package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 租约里的 Valid=false 表示"没有可用快照"，还原时不应写注册表，只删文件。
// 这里用环境变量把租约指到临时目录，避免动到真实用户配置。
func TestRestoreProxyLeaseRemovesFileWhenInvalid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lease.json")
	t.Setenv("CAULINK_PROXY_LEASE", p)
	if err := os.WriteFile(p, []byte(`{"valid":false,"enable":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreProxyLease()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("无效租约还原后应删除文件，实际 err=%v", err)
	}
}

// 没有租约时什么都不做（不能误删、也不能改注册表）。
func TestRestoreProxyLeaseNoopWithoutFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAULINK_PROXY_LEASE", filepath.Join(dir, "absent.json"))
	restoreProxyLease() // 不应 panic、不应报错
}
