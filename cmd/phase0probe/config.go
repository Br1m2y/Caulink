package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// Config 是 Phase 0 探针的全部可调参数。
// 用 JSON 而不是 YAML：Go 标准库原生支持，不引第三方依赖。
type Config struct {
	// Targets 测速目标 URL 列表。必须是能持续输出大流量的大文件或无限流端点。
	// 只用第一个做实测，其余的仅做连通性备份，避免把结果混在一起。
	Targets []string `json:"targets"`

	// ConnectionsPerIP 每个源 IP 上并发发起的 TCP 连接数。
	// 少于 4 条时单条连接的 TCP 慢启动会让结果偏低；多于 16 条对结果提升有限，且对目标服务器不友好。
	ConnectionsPerIP int `json:"connections_per_ip"`

	// DurationSeconds 稳态测量时长（不含预热）。
	DurationSeconds int `json:"duration_seconds"`

	// WarmupSeconds 预热时长。这段内的数据全部丢弃，用于跨过 TCP 慢启动和拥塞窗口爬升。
	WarmupSeconds int `json:"warmup_seconds"`

	// Rounds 每个场景重复轮数，取中位数以压低抖动。
	Rounds int `json:"rounds"`

	// CooldownSeconds 轮次之间的间隔，让连接和服务器侧限速状态恢复。
	CooldownSeconds int `json:"cooldown_seconds"`

	// IncludeInterfaces 只保留匹配此正则的网卡名。为空表示不限制。
	IncludeInterfaces string `json:"include_interfaces"`

	// ExcludeInterfaces 排除匹配此正则的网卡名。默认排除回环、容器网桥等虚拟设备。
	ExcludeInterfaces string `json:"exclude_interfaces"`

	// MaxSourceIPs 最多使用几个源 IP。Phase 0 只需要 2 到 3 个。
	MaxSourceIPs int `json:"max_source_ips"`

	// OutputDir JSON 与 Markdown 结果的落盘目录。
	OutputDir string `json:"output_dir"`
}

func defaultConfig() Config {
	return Config{
		// 默认目标按"国内可直连、文件足够大"排序。2026-11 实测：
		// 清华 TUNA、中科大镜像可用；Cloudflare 的 __down 端点对 bytes>=100000000
		// 直接返回 403，所以只能用 50000000。
		// 镜像站的 ISO 文件名随版本变化，失效时换成本地镜像的最新 ISO 即可。
		Targets: []string{
			"https://mirrors.tuna.tsinghua.edu.cn/ubuntu-releases/22.04/ubuntu-22.04.5-desktop-amd64.iso",
			"https://mirrors.ustc.edu.cn/ubuntu-releases/22.04/ubuntu-22.04.5-desktop-amd64.iso",
			"https://speed.cloudflare.com/__down?bytes=50000000",
		},
		ConnectionsPerIP:  8,
		DurationSeconds:   15,
		WarmupSeconds:     3,
		Rounds:            3,
		CooldownSeconds:   5,
		IncludeInterfaces: "",
		// (?i) 不能省：Windows 上网卡名大小写不固定（"Radmin VPN" 而不是 "radmin"），
		// 区分大小写的正则会把 VPN 虚拟网卡漏掉，白测一次超时。
		ExcludeInterfaces: `(?i)^(lo|docker|veth|br-|virbr|vmnet|utun|tun|tap|wg|tailscale|radmin|zerotier|hamachi)`,
		MaxSourceIPs:      3,
		OutputDir:         "tools/phase0/out",
	}
}

// loadConfig 读配置文件。路径为空时直接返回默认值。
func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	if path == "" {
		return cfg, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	return cfg, nil
}

func (c Config) warmup() time.Duration { return time.Duration(c.WarmupSeconds) * time.Second }
func (c Config) duration() time.Duration {
	return time.Duration(c.DurationSeconds) * time.Second
}
func (c Config) cooldown() time.Duration {
	return time.Duration(c.CooldownSeconds) * time.Second
}

// validate 在真正开始测速前拦住明显错误的参数。
// 这些检查的意义是：跑了 10 分钟才发现参数不对，比启动时就报错贵得多。
func (c Config) validate() error {
	if len(c.Targets) == 0 {
		return fmt.Errorf("targets 为空：至少需要一个测速目标 URL")
	}
	for i, t := range c.Targets {
		if !strings.HasPrefix(t, "http://") && !strings.HasPrefix(t, "https://") {
			return fmt.Errorf("targets[%d] 不是 http(s) URL: %q", i, t)
		}
	}
	if c.ConnectionsPerIP < 1 || c.ConnectionsPerIP > 64 {
		return fmt.Errorf("connections_per_ip 必须在 1 到 64 之间，当前 %d", c.ConnectionsPerIP)
	}
	if c.DurationSeconds < 5 {
		return fmt.Errorf("duration_seconds 至少 5 秒，当前 %d（时间太短无法跨过慢启动且误差过大）", c.DurationSeconds)
	}
	if c.WarmupSeconds < 0 || c.WarmupSeconds >= c.DurationSeconds {
		return fmt.Errorf("warmup_seconds 必须 >=0 且小于 duration_seconds，当前 %d vs %d",
			c.WarmupSeconds, c.DurationSeconds)
	}
	if c.Rounds < 1 || c.Rounds > 10 {
		return fmt.Errorf("rounds 必须在 1 到 10 之间，当前 %d", c.Rounds)
	}
	if c.MaxSourceIPs < 2 {
		return fmt.Errorf("max_source_ips 至少为 2，当前 %d（Phase 0 的核心就是对比多个源 IP）", c.MaxSourceIPs)
	}
	if _, err := regexp.Compile(c.IncludeInterfaces); err != nil {
		return fmt.Errorf("include_interfaces 正则非法: %w", err)
	}
	if _, err := regexp.Compile(c.ExcludeInterfaces); err != nil {
		return fmt.Errorf("exclude_interfaces 正则非法: %w", err)
	}
	return nil
}
