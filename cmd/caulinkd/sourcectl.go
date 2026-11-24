package main

// 「重新扫描」的实现：把配置里的源列表与网卡现状重新核对一次。
//
// 为什么需要它：校园网是 DHCP，网卡重连后地址会变
// （实测 10.6.53.173 → 10.6.69.131）。启动时已经校正过一次，
// 但用户可能在使用过程中插拔网线、切换 WiFi——那时不该要求重启程序。
//
// 扫描的语义很窄，只做三件事：
//   1. 用网卡当前地址重算源列表（resolveSources）
//   2. 池里多余的移除、缺的补上
//   3. 认证层同步启停，让新 IP 立刻开始认证

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/Br1m2y/caulink/internal/control"
	"github.com/Br1m2y/caulink/internal/ippool"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

type sourceController struct {
	mu     sync.Mutex
	logger *slog.Logger
	ctx    context.Context

	// configured 是配置文件里的原始源列表，可能包含过期的 IP。
	configured []SourceConfig
	// exclude 是要排除在自动发现之外的网卡名（与配置里的 exclude_interfaces 一致）。
	exclude []string
	pool    *ippool.Pool
	auth    *authController

	// autoConnect 为真时，扫描前会先尝试把"无线且没有地址"的网卡连上校园网。
	autoConnect bool
	// autoSSID 是要自动连接的 WiFi 名称。
	autoSSID string
	// dataDir 用来落临时生成的 WiFi 配置文件。
	dataDir string
}

func newSourceController(ctx context.Context, logger *slog.Logger, configured []SourceConfig, exclude []string, pool *ippool.Pool, auth *authController, autoConnect bool, autoSSID, dataDir string) *sourceController {
	return &sourceController{
		logger: logger, ctx: ctx, configured: configured, exclude: exclude,
		pool: pool, auth: auth,
		autoConnect: autoConnect, autoSSID: autoSSID, dataDir: dataDir,
	}
}

// RescanSources 见 control.SourceController。
func (c *sourceController) RescanSources() (control.RescanResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	res := control.RescanResult{At: time.Now()}

	// 先补充"能连上但还没连"的无线网卡，再算源列表。
	//
	// 新插的 USB 网卡通常没有 CAU 的配置文件，Windows 不会自动连；
	// 这里替用户连一次，避免"扫不到网卡"的误会。只动无线且没有 IPv4 的网卡，
	// 有地址的网卡不会被打断。
	if c.autoConnect {
		if connected := autoConnectWifi(c.ctx, c.logger, c.dataDir, c.autoSSID, c.exclude, 6*time.Second); len(connected) > 0 {
			res.AutoConnected = connected
			c.logger.Info("重新扫描：已自动连接无线网卡", "interfaces", connected)
		}
	}

	next := resolveSources(c.configured, c.exclude, c.logger)

	want := make(map[string]SourceConfig, len(next))
	for _, s := range next {
		want[s.Interface+"@"+s.SourceIP] = s
	}

	cur := make(map[string]contract.IPPoolEntry)
	for _, e := range c.pool.Snapshot() {
		cur[e.ID] = e
	}

	// 1) 移除：池里有、网卡上已不存在的源
	for id := range cur {
		if _, ok := want[id]; ok {
			continue
		}
		c.pool.Remove(id)
		if c.auth != nil {
			if err := c.auth.StopEntry(id); err != nil {
				c.logger.Warn("停止认证实例失败", "id", id, "err", err)
			}
		}
		res.Removed = append(res.Removed, id)
		c.logger.Info("扫描：移除已失效的源", "id", id)
	}

	// 2) 新增：网卡上出现、池里还没有的源
	var entries []contract.IPPoolEntry
	for id, s := range want {
		if _, ok := cur[id]; ok {
			res.Unchanged = append(res.Unchanged, id)
			entries = append(entries, cur[id])
			continue
		}
		e := contract.IPPoolEntry{
			ID:         id,
			Interface:  s.Interface,
			SourceIP:   s.SourceIP,
			AuthStatus: contract.AuthOffline,
		}
		if err := c.pool.Upsert(e); err != nil {
			return res, fmt.Errorf("加入 IP 池失败（%s）: %w", id, err)
		}
		if c.auth != nil {
			// 认证层要知道这个新源；有凭据时它会立刻开始登录。
			if err := c.auth.StartEntry(c.ctx, e); err != nil {
				c.logger.Warn("为新源启动认证失败", "id", id, "err", err)
			}
		}
		res.Added = append(res.Added, id)
		entries = append(entries, e)
		c.logger.Info("扫描：发现新的源", "id", id, "ip", s.SourceIP)
	}

	// 3) 让认证层持有的源列表与池保持一致
	if c.auth != nil {
		c.auth.ReplaceEntries(entries)
	}

	// 4) 把"存在但没被采用"的网卡连原因一起带出去。
	// 插了网卡点扫描却没变化，多半是那块网卡当时没有可路由的 IPv4。
	res.Skipped = scanSkipped(c.exclude)

	switch {
	case len(res.Added) == 0 && len(res.Removed) == 0:
		res.Message = fmt.Sprintf("扫描完成：%d 个端口，没有变化", len(res.Unchanged))
	default:
		res.Message = fmt.Sprintf("扫描完成：新增 %d，移除 %d，未变 %d",
			len(res.Added), len(res.Removed), len(res.Unchanged))
	}
	return res, nil
}

// scanSkipped 列出"本机存在、但这次没有参与调度"的网卡及原因。
//
// 两类典型情况：
//   - 网卡插上了但 DHCP 还没谈完，只有 169.254.x.x 链路本地地址；
//   - 网卡未连接/被禁用，没有任何 IPv4。
//
// 它们都不该进调度池（认证器绑不上地址，只会反复失败），但必须让用户看见，
// 否则"插了网卡点扫描却没变化"看起来就是程序没发现新硬件。
func scanSkipped(exclude []string) []control.SkippedInterface {
	excluded := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		if name != "" {
			excluded[name] = true
		}
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var out []control.SkippedInterface
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if excluded[ifi.Name] {
			continue // 配置里主动排除的，不算"跳过"
		}
		if _, ok := interfaceIPv4(ifi.Name); ok {
			continue // 有可路由地址，已经进池了
		}

		item := control.SkippedInterface{Interface: ifi.Name}
		// 先看有没有地址，再判断为什么没用上。
		hasLinkLocal := false
		if addrs, aerr := ifi.Addrs(); aerr == nil {
			for _, a := range addrs {
				ipnet, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				v4 := ipnet.IP.To4()
				if v4 == nil || !v4.IsLinkLocalUnicast() {
					continue
				}
				hasLinkLocal = true
				if item.Address == "" {
					item.Address = v4.String()
				}
			}
		}

		switch {
		case ifi.Flags&net.FlagUp == 0 && hasLinkLocal:
			// Windows 对"未连接/虚拟"网卡保留 169.254 地址，但接口是 down 的。
			// 它们永远不会进调度池，列出来只是为了解释数量差异。
			item.Reason = "未连接或仅虚拟网卡（接口未启用，只有 169.254 地址）"
		case ifi.Flags&net.FlagUp == 0:
			item.Reason = "接口未启用（网卡被禁用或未插入）"
		case hasLinkLocal:
			item.Reason = "只有 169.254 链路本地地址：DHCP 还没分配，确认网线/Wi-Fi 已连上，等几秒再扫描"
		default:
			item.Reason = "没有 IPv4 地址（可能还在获取 DHCP）"
		}
		out = append(out, item)
	}
	return out
}
