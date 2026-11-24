package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Config 是 caulinkd 的全部配置。
//
// 用 JSON 而不是 YAML：标准库直接支持，不引第三方依赖。
// 将来配置项复杂到需要注释和层级时再考虑换 YAML，现在不值得。
type Config struct {
	// DataDir 存放认证实例的配置文件与日志。里面有明文密码，目录权限是 0700。
	DataDir string `json:"data_dir"`

	// Sources 是要参与调度的源 IP 列表。每个条目对应一张网卡上的一个地址。
	Sources []SourceConfig `json:"sources"`

	// AutoConnectWifi 表示重新扫描时，自动为"无线且没有地址"的网卡注入并连接
	// 校园网配置。默认开启。
	//
	// 为什么要这个：一台新电脑上 Windows 没有 CAU 的配置文件，新插的 USB
	// 无线网卡不会自己连，用户得手动去 WiFi 列表点一次，很容易以为"软件扫不到网卡"。
	AutoConnectWifi *bool `json:"auto_connect_wifi,omitempty"`

	// AutoConnectSSID 是自动连接的 WiFi 名称，默认 "CAU"。
	AutoConnectSSID string `json:"auto_connect_ssid,omitempty"`

	// ExcludeInterfaces 列出**不参与调度**的网卡名（自动发现时跳过）。
	//
	// 为什么需要：自动发现会把本机所有已启用的网卡都收进来，而 VPN 虚拟网卡
	// （0dcloud 的 vgate0、TAP、WireGuard 等）不是校园网链路——
	// 认证器拿它反复登录校园门户必然失败，既刷日志又有账号被风控的风险。
	// 例：["vgate0"]
	ExcludeInterfaces []string `json:"exclude_interfaces,omitempty"`

	Auth   AuthConfig   `json:"auth"`
	Health HealthConfig `json:"health"`
	Socks5 Socks5Config `json:"socks5"`
	Status StatusConfig `json:"status"`
	// HTTPProxy 是可选的 HTTP 代理（CONNECT + 明文转发）。
	// Steam 等客户端只支持 HTTP 代理、不支持 SOCKS5，需要它才能走多 IP 调度。
	// Listen 为空时不启动。
	HTTPProxy HTTPProxyConfig `json:"http_proxy"`
}

// HTTPProxyConfig 是 HTTP 代理的配置，监听地址只允许回环。
type HTTPProxyConfig struct {
	Listen string `json:"listen"`

	// Upstream 可选：把命中规则的目标域名交给另一个本地代理（例如 VPN 客户端），
	// 其余请求仍走多源 IP。这样"被墙的站点"和"要聚合的下载"可以共存。
	Upstream *UpstreamConfig `json:"upstream,omitempty"`
}

// UpstreamConfig 描述上游代理与分流规则。
type UpstreamConfig struct {
	// Proxy 形如 http://127.0.0.1:17891。
	Proxy string `json:"proxy"`
	// Domains 为域名规则：example.com / *.example.com / .example.com。
	// 留空表示全部走上游。
	Domains []string `json:"domains"`
}

// SourceConfig 是一个源 IP 及其所在网卡。
type SourceConfig struct {
	Interface string `json:"interface"`
	SourceIP  string `json:"source_ip"`
	// SourceIP6 留空时由程序从网卡自动发现。
	// 显式填写可以覆盖自动探测（例如某块网卡有多个 v6 前缀）。
	SourceIP6 string `json:"source_ip6,omitempty"`
}

// AuthConfig 控制是否真的去调认证器（cmd/caulink-auth）登录校园网。
//
// Enabled=false 时程序假设这些 IP 已经由用户手动认证过，直接标记为在线。
// 这个模式在开发与演示时很实用：不用真实账号、不用校园网，
// 也能把 SOCKS5 → 调度 → 拨号这条链路跑起来。
//
// 农大是 Dr.COM ePortal：账号要拼成 <username>@<realm>，门户默认 10.3.191.9:801。
type AuthConfig struct {
	Enabled         bool   `json:"enabled"`
	Binary          string `json:"binary"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	Realm           string `json:"realm"`
	Portal          string `json:"portal"`
	Port            int    `json:"port"`
	GuardianSeconds int    `json:"guardian_seconds"`
}

// HealthConfig 控制链路质量探测。
type HealthConfig struct {
	IntervalSeconds int    `json:"interval_seconds"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	Target          string `json:"target"`
	Window          int    `json:"window"`
}

// Socks5Config 控制本地代理监听。
type Socks5Config struct {
	Listen string `json:"listen"`
}

// StatusConfig 控制本地状态接口。
//
// 不写 status 段时用默认端口；把 listen 显式写成 "" 则关闭。
// 之所以允许关闭：纯命令行调试时多开一个端口没有意义，
// 而且端口冲突会让启动日志多一条容易误判的报错。
type StatusConfig struct {
	Listen string `json:"listen"`
}

func defaultConfig() Config {
	autoConnect := true
	return Config{
		DataDir:         "data",
		AutoConnectWifi: &autoConnect,
		AutoConnectSSID: "CAU",
		Health: HealthConfig{
			IntervalSeconds: 10,
			TimeoutSeconds:  3,
			Target:          "connect.rom.miui.com:80",
			Window:          10,
		},
		Socks5: Socks5Config{Listen: "127.0.0.1:1080"},
		Status: StatusConfig{Listen: "127.0.0.1:7788"},
	}
}

func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()

	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("解析配置 %s: %w", path, err)
	}

	// JSON 里没写的字段会被零值覆盖，这里把关键默认值补回来。
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}

	// 配置里的相对路径要能稳定解析。
	//
	// 不能只依赖进程的工作目录：桌面端从 bin\ 拉起守护进程时，
	// "bin/caulink-auth.exe" 会变成 bin\bin\caulink-auth.exe 而找不到。
	// 也不能只相对配置文件目录：示例配置里的 "bin/..." 是相对仓库根的。
	//
	// 做法：按"工作目录 → 配置目录 → 配置目录上一级 → exe 目录 → exe 上一级"
	// 依次探测，取第一个真实存在的。都不存在时保留原值（好让报错信息可读）。
	cfg.Auth.Binary = resolveExistingPath(cfg.Auth.Binary, filepath.Dir(path))
	cfg.DataDir = resolveExistingPath(cfg.DataDir, filepath.Dir(path))
	if cfg.Health.IntervalSeconds <= 0 {
		cfg.Health.IntervalSeconds = 10
	}
	if cfg.Health.TimeoutSeconds <= 0 {
		cfg.Health.TimeoutSeconds = 3
	}
	if cfg.Health.Target == "" {
		cfg.Health.Target = "connect.rom.miui.com:80"
	}
	if cfg.Health.Window <= 0 {
		cfg.Health.Window = 10
	}
	if cfg.Socks5.Listen == "" {
		cfg.Socks5.Listen = "127.0.0.1:1080"
	}

	return cfg, nil
}

func (c Config) validate() error {
	// sources 允许为空：此时完全依赖自动发现（插上网卡就能用）。
	// 显式写进配置的条目优先，用于固定 IP 或指定 v6。

	seen := map[string]bool{}
	for i, s := range c.Sources {
		if s.Interface == "" {
			return fmt.Errorf("sources[%d] 缺少 interface", i)
		}
		ip := net.ParseIP(s.SourceIP)
		if ip == nil {
			return fmt.Errorf("sources[%d].source_ip %q 不是合法 IP", i, s.SourceIP)
		}
		if seen[s.SourceIP] {
			return fmt.Errorf("sources[%d] 的 IP %s 重复", i, s.SourceIP)
		}
		seen[s.SourceIP] = true
	}

	if c.Auth.Enabled {
		if c.Auth.Binary == "" {
			return errors.New("auth.enabled=true 时必须指定 auth.binary")
		}
		if _, err := os.Stat(c.Auth.Binary); err != nil {
			return fmt.Errorf("找不到认证二进制 %s: %w", c.Auth.Binary, err)
		}
		// 账号密码可以为空：用户会在界面上的「校园网账号」里填写。
		//
		// 早期这里强制要求写死在配置文件里，等于逼用户去改 JSON——
		// 界面上明明有输入框。现在凭据有三个来源，优先级从高到低：
		//   1. 本机保存的凭据（用户勾了"记住密码"）
		//   2. 配置文件里的 username/password
		//   3. 界面登录时实时提交
		// 三者都没有时以"假设已认证"模式运行，提示用户在界面上登录。
	}

	if err := requireLoopback("socks5.listen", c.Socks5.Listen); err != nil {
		return err
	}

	if c.HTTPProxy.Listen != "" {
		if err := requireLoopback("http_proxy.listen", c.HTTPProxy.Listen); err != nil {
			return err
		}
		if c.HTTPProxy.Listen == c.Socks5.Listen {
			return fmt.Errorf("http_proxy.listen 与 socks5.listen 不能相同（都是 %q）", c.Socks5.Listen)
		}
	}

	if c.Status.Listen != "" {
		if err := requireLoopback("status.listen", c.Status.Listen); err != nil {
			return err
		}
		// 两个服务抢同一个端口时，后起的那个会失败，而失败信息是
		// "address already in use"，排查时容易以为是系统占用了端口。
		if c.Status.Listen == c.Socks5.Listen {
			return fmt.Errorf("status.listen 与 socks5.listen 不能相同（都是 %q）", c.Socks5.Listen)
		}
	}

	return nil
}

// requireLoopback 校验地址是回环。两个本地服务共用这条规则。
func requireLoopback(field, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s %q 非法: %w", field, addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s 必须是回环地址，当前 %q", field, addr)
	}
	return nil
}

func (c HealthConfig) interval() time.Duration {
	return time.Duration(c.IntervalSeconds) * time.Second
}

func (c HealthConfig) timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

func (c AuthConfig) guardian() int {
	if c.GuardianSeconds <= 0 {
		return 60
	}
	return c.GuardianSeconds
}

// resolveExistingPath 把相对路径解析成真实存在的绝对路径。
//
// 依次在：当前工作目录、配置目录、配置目录上一级、exe 目录、exe 上一级
// 中查找；都不存在时原样返回（保留可读的报错）。
func resolveExistingPath(p, configDir string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}

	var bases []string
	if wd, err := os.Getwd(); err == nil {
		bases = append(bases, wd)
	}
	bases = append(bases, configDir, filepath.Join(configDir, ".."))
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		bases = append(bases, d, filepath.Join(d, ".."))
	}

	for _, base := range bases {
		cand := filepath.Join(base, p)
		if _, err := os.Stat(cand); err == nil {
			if abs, err := filepath.Abs(cand); err == nil {
				return abs
			}
			return cand
		}
	}
	return p
}

// autoConnectWifiEnabled 返回是否允许自动连接校园网（未配置时默认 true）。
func (c Config) autoConnectWifiEnabled() bool {
	if c.AutoConnectWifi == nil {
		return true
	}
	return *c.AutoConnectWifi
}

// autoConnectSSID 返回自动连接的 WiFi 名称（未配置时默认 CAU）。
func (c Config) autoConnectSSID() string {
	if c.AutoConnectSSID == "" {
		return "CAU"
	}
	return c.AutoConnectSSID
}

// isExcludedInterface 判断某块网卡是否被配置排除在调度之外。
func (c Config) isExcludedInterface(name string) bool {
	for _, ex := range c.ExcludeInterfaces {
		if ex != "" && ex == name {
			return true
		}
	}
	return false
}
