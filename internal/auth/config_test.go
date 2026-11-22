package auth

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

func testEntry() contract.IPPoolEntry {
	return contract.IPPoolEntry{
		ID:        "eth0@10.0.0.2",
		Interface: "eth0",
		SourceIP:  "10.0.0.2",
	}
}

// 生成的 auth.json 必须能被 cmd/caulink-auth 原样解析：
// 字段名就是两边的契约，改名要同时改两边。
func TestWriteInstanceConfigProducesDrcomJSON(t *testing.T) {
	opts := Options{
		DataDir: t.TempDir(),
		Account: Account{
			Username: "2023001",
			Password: `p@ss"word\with#chars`,
			Realm:    "cau",
			Portal:   "10.3.191.9",
			Port:     801,
		},
		GuardianDuration: 45,
	}
	opts.applyDefaults()

	cfgPath, dir, err := opts.writeInstanceConfig(testEntry())
	if err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("读配置失败: %v", err)
	}

	var got instanceConfig
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("生成的配置不是合法 JSON: %v\n%s", err, raw)
	}

	if got.Username != "2023001" || got.Realm != "cau" {
		t.Errorf("账号字段不正确: %+v", got)
	}
	if got.Portal != "10.3.191.9" || got.Port != 801 {
		t.Errorf("门户字段不正确: %+v", got)
	}
	// 密码里有引号和反斜杠，必须原样往返，否则登录会静默失败。
	if got.Password != `p@ss"word\with#chars` {
		t.Errorf("密码没有原样保存: %q", got.Password)
	}
	// 源 IP 必须进配置：认证器要绑定它发请求，否则登录的是别的 IP。
	if got.SourceIP != "10.0.0.2" {
		t.Errorf("source_ip 应为 10.0.0.2，得到 %q", got.SourceIP)
	}
	if got.CheckSeconds != 45 {
		t.Errorf("check_seconds 应继承 GuardianDuration(45)，得到 %d", got.CheckSeconds)
	}
	// 日志目录落在实例目录，便于按 IP 归属排查。
	if got.LogDir != dir {
		t.Errorf("log_dir 应指向实例目录 %s，得到 %s", dir, got.LogDir)
	}
	if strings.HasSuffix(cfgPath, "Config.yaml") {
		t.Error("仍在使用 BitSrunLoginGo 的 Config.yaml，农大是 Dr.COM，不该走那条路")
	}
}

func TestWriteInstanceConfigPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不用 POSIX 权限位，跳过")
	}

	opts := Options{
		DataDir: t.TempDir(),
		Account: Account{Username: "u", Password: "p", Portal: "10.3.191.9"},
	}
	opts.applyDefaults()

	cfgPath, _, err := opts.writeInstanceConfig(testEntry())
	if err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}

	fi, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("配置文件权限应为 0600（里面有明文密码），实际 %04o", perm)
	}
}

func TestInstanceDirSanitizesAtSign(t *testing.T) {
	got := instanceDir("/data", "eth0@10.0.0.2")
	if strings.Contains(got, "@") {
		t.Errorf("目录名不应包含 @: %s", got)
	}
	if !strings.HasSuffix(got, "eth0_10.0.0.2") {
		t.Errorf("目录名不符合预期: %s", got)
	}
}
