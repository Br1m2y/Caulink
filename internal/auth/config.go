package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// instanceConfig 是每个源 IP 实例的认证器配置，由 cmd/caulink-auth 读取。
//
// 为什么从 BitSrunLoginGo 的 YAML 换成自己的 JSON：
// 农大校园网是 Dr.COM ePortal，不是深澜（Srun），BitSrunLoginGo 不适用。
// 协议细节见 docs/design/auth-layer.md：登录/注销都是对
// http://<portal>:801/eportal/?c=ACSetting&a=Login|Logout 的表单请求，
// 不需要验证码，但账号必须带 @realm 后缀。
//
// 文件里含明文密码，因此必须 0600，目录 0700。
type instanceConfig struct {
	// Portal 是认证门户地址（IP 或域名），例如 10.3.191.9。
	Portal string `json:"portal"`
	// Port 是门户的 ePortal 端口，农大实测为 801。
	Port int `json:"port"`
	// Username 是学号，**不带** realm 后缀。
	Username string `json:"username"`
	// Realm 是认证域后缀，登录时拼成 <username>@<realm>。
	// 缺了它 RADIUS 会报 "no AT symbol in realm"。
	Realm    string `json:"realm"`
	Password string `json:"password"`
	// SourceIP 是该实例要绑定的源地址：登录请求必须从它发出，
	// 否则登录的是别的 IP。
	SourceIP string `json:"source_ip"`
	// CheckSeconds 是在线检查周期（秒）。
	CheckSeconds int `json:"check_seconds"`
	// LogDir 是日志目录。
	LogDir string `json:"log_dir"`
	// PortalCache 是"源 IP → 正确门户节点"的缓存文件路径。
	//
	// 校园网 ePortal 按网段分流，多个节点并存；登录必须发到管本网段的那个，
	// 否则门户照样回 3.htm 成功页、流量却继续被劫持。缓存让子进程与守护进程
	// 共用同一个"上次成功的节点"。
	PortalCache string `json:"portal_cache"`
}

// instanceDir 返回某个源 IP 实例的数据目录。
func instanceDir(dataDir, id string) string {
	return filepath.Join(dataDir, "auth", sanitizeID(id))
}

// writeInstanceConfig 生成该实例专属的 auth.json，并返回配置文件与日志目录路径。
//
// 权限必须是 0600/0700：文件里有明文密码。这不是洁癖，
// 多用户机器上 0644 等于把校园网密码写给所有人看。
func (o Options) writeInstanceConfig(entry contract.IPPoolEntry) (cfgPath, logPath string, err error) {
	dir := instanceDir(o.DataDir, entry.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("创建实例目录 %s: %w", dir, err)
	}

	cfg := instanceConfig{
		Portal:       o.Account.Portal,
		Port:         o.Account.Port,
		Username:     o.Account.Username,
		Realm:        o.Account.Realm,
		Password:     o.Account.Password,
		SourceIP:     entry.SourceIP,
		CheckSeconds: o.GuardianDuration,
		LogDir:       dir,
		PortalCache:  o.PortalCachePath,
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("序列化实例配置: %w", err)
	}
	raw = append(raw, '\n')

	cfgPath = filepath.Join(dir, "auth.json")
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		return "", "", fmt.Errorf("写配置文件 %s: %w", cfgPath, err)
	}
	// WriteFile 对已存在的文件不改权限，显式再设一次。
	if err := os.Chmod(cfgPath, 0o600); err != nil {
		return "", "", fmt.Errorf("设置配置文件权限 %s: %w", cfgPath, err)
	}

	return cfgPath, dir, nil
}

// sanitizeID 把 "<iface>@<ip>" 变成安全的目录名。
func sanitizeID(id string) string {
	r := strings.NewReplacer("@", "_", "/", "_", "\\", "_", ":", "_", " ", "_")
	return r.Replace(id)
}
