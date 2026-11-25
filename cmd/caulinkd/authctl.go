package main

// 认证运行时控制器：让界面上的"账号 / 密码 / 登录"直接驱动真实的校园网认证。
//
// 之前认证只能在配置文件里填死，用户必须改 JSON 才能登录——这不是产品该有的样子。
// 现在：
//   登录  → 先用 drcom 客户端真实验证一次，通过后启动认证子进程，并按需保存凭据
//   退出  → 停止认证、删除本机凭据、把端口标记为离线
//   启动时 → 若本机存有凭据，自动恢复认证（断线静默重连依赖它）
//
// 凭据保存在 <data_dir>/credentials.json（0600）。它不在版本库里，
// 内容也不会进日志。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Br1m2y/caulink/internal/auth"
	"github.com/Br1m2y/caulink/internal/control"
	"github.com/Br1m2y/caulink/internal/drcom"
	"github.com/Br1m2y/caulink/internal/ippool"
	"github.com/Br1m2y/caulink/internal/netsrc"
	"github.com/Br1m2y/caulink/internal/portal"
	"github.com/Br1m2y/caulink/internal/reauth"
	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// storedCredential 是落盘的凭据文件结构。
type storedCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
	SavedAt  string `json:"saved_at"`
}

// authController 实现 control.AuthController。
type authController struct {
	logger  *slog.Logger
	opts    auth.Options // 模板：账号密码在每次登录时填入
	entries []contract.IPPoolEntry
	pool    *ippool.Pool
	credDir string
	ctx     context.Context

	mu         sync.Mutex
	mgr        auth.Manager
	username   string
	password   string
	remember   bool
	lastErr    string
	pending    bool
	dialFail   func(string)
	reauthStop context.CancelFunc
}

func newAuthController(
	ctx context.Context,
	logger *slog.Logger,
	opts auth.Options,
	entries []contract.IPPoolEntry,
	pool *ippool.Pool,
	credDir string,
) *authController {
	// "源 IP → 正确门户节点"缓存，守护进程与认证子进程共用。
	// 校园网门户按网段分流，登录发给错误节点会"假成功"，必须按源 IP 记住正确的那个。
	cachePath := filepath.Join(credDir, "portal-map.json")
	opts.Portals = portal.Load(cachePath)
	opts.PortalCachePath = cachePath

	return &authController{
		logger:  logger,
		opts:    opts,
		entries: entries,
		pool:    pool,
		credDir: credDir,
		ctx:     ctx,
	}
}

// credPath 返回凭据文件路径。
func (c *authController) credPath() string {
	return filepath.Join(c.credDir, "credentials.json")
}

// Bootstrap 在启动时恢复认证：优先用本机保存的凭据。
//
// 返回是否启用了真实认证。false 表示退化成"假设已认证"（开发/演示模式）。
func (c *authController) Bootstrap() (bool, error) {
	stored, err := c.loadCredential()
	if err != nil {
		c.logger.Warn("读取已保存的凭据失败", "err", err)
	}
	if stored != nil {
		c.logger.Info("使用本机保存的凭据恢复认证")
		if err := c.startWith(stored.Username, stored.Password, true); err != nil {
			c.logger.Error("恢复认证失败", "err", err)
			return false, err
		}
		return true, nil
	}

	// 配置文件里写了账号密码时同样启用
	if c.opts.Account.Username != "" && c.opts.Account.Password != "" {
		if err := c.startWith(c.opts.Account.Username, c.opts.Account.Password, false); err != nil {
			c.logger.Error("按配置启动认证失败", "err", err)
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// Login 见 control.AuthController。
func (c *authController) Login(username, password string, remember bool) error {
	// 允许"留空沿用本机保存的凭据"。
	//
	// 界面上的密码框一直写着"已保存（留空则沿用）"，但此前只有认证子进程的
	// 自动重连会读凭据，手动点「登录」仍会报"请输入密码"——这是用户实际遇到的
	// "勾了记住密码，第二次登录还要重新输密码"。
	username = strings.TrimSpace(username)
	if password == "" {
		stored, err := c.loadCredential()
		if err != nil {
			return fmt.Errorf("读取本机凭据失败：%w", err)
		}
		if stored == nil {
			return errors.New("请输入密码：本机还没有保存可沿用的凭据")
		}
		if username == "" {
			username = stored.Username
		}
		if username != stored.Username {
			return fmt.Errorf("更换账号需要重新输入密码（本机保存的是 %s）", maskAccount(stored.Username))
		}
		password = stored.Password
		// 沿用本机凭据时继续记住；否则这次登录的 remember=false 分支会把
		// 刚用上的凭据文件删掉，下一次又回到要手输密码的状态。
		remember = true
	} else if username == "" {
		return errors.New("请输入校园网账号")
	}

	// 先真实验证一次：错密码立刻就能告诉用户，而不是等子进程反复失败。
	pending, err := c.verifyCredential(username, password)
	if err != nil {
		return err
	}
	if err := c.startWith(username, password, remember); err != nil {
		return err
	}
	if pending {
		c.mu.Lock()
		c.pending = true
		c.mu.Unlock()
	}
	if remember {
		if err := c.saveCredential(username, password); err != nil {
			c.logger.Warn("保存凭据失败（本次登录仍然有效）", "err", err)
		} else {
			c.logger.Info("凭据已保存到本机", "path", c.credPath())
		}
	} else {
		_ = os.Remove(c.credPath())
	}
	return nil
}

// Logout 见 control.AuthController。
func (c *authController) Logout() error {
	c.mu.Lock()
	mgr := c.mgr
	c.mgr = nil
	c.username, c.password, c.remember = "", "", false
	c.mu.Unlock()

	if mgr != nil {
		_ = mgr.Close()
	}
	_ = os.Remove(c.credPath())

	// 端口回到未认证：调度层不再把流量派给它们。
	for _, e := range c.entries {
		_ = c.pool.UpdateAuth(e.ID, contract.AuthOffline, time.Now())
	}
	c.logger.Info("已退出认证并清除本机凭据")
	return nil
}

// AuthStatus 见 control.AuthController。
// HasCredential 表示本机是否保存了可沿用的凭据（见 control.AuthController）。
func (c *authController) HasCredential() bool {
	stored, err := c.loadCredential()
	return err == nil && stored != nil
}

func (c *authController) AuthStatus() control.AuthRuntimeStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	acct := ""
	if c.username != "" {
		acct = maskAccount(c.username)
	}
	running := c.mgr != nil
	msg := ""
	// 后台验证有了结论（任一 IP 上线）就不再显示"验证中"。
	// 失败的情况由端口行体现（离线 + 错误原因），这里只负责不再挂着"验证中"。
	for _, e := range c.pool.Snapshot() {
		if e.AuthStatus == contract.AuthOnline {
			c.pending = false
			break
		}
	}

	switch {
	case c.pending:
		msg = "已提交登录，正在后台验证…"
	case running && c.remember:
		msg = "已登录，凭据保存在本机，掉线会自动重连"
	case running:
		msg = "已登录，凭据未保存（重启后需要重新输入）"
	case c.lastErr != "":
		msg = "登录失败：" + c.lastErr
	default:
		msg = "未登录"
	}
	return control.AuthRuntimeStatus{
		Configured: acct != "",
		Running:    running,
		Account:    acct,
		Remember:   c.remember,
		Message:    msg,
		At:         time.Now(),
	}
}

// OnDialFailure 暴露给 reauth.Watcher：代理层建连失败时触发即时重认证。
func (c *authController) OnDialFailure(srcIP string) {
	c.mu.Lock()
	fn := c.dialFail
	c.mu.Unlock()
	if fn != nil {
		fn(srcIP)
	}
}

// Close 在进程退出时释放认证层。
func (c *authController) Close() {
	c.mu.Lock()
	mgr := c.mgr
	c.mgr = nil
	c.mu.Unlock()
	if mgr != nil {
		_ = mgr.Close()
	}
}

// startWith 用指定凭据（重新）启动认证层。
func (c *authController) startWith(username, password string, remember bool) error {
	c.stopManager()

	opts := c.opts
	opts.Account.Username = username
	opts.Account.Password = password
	opts.Logger = c.logger

	mgr, err := auth.New(opts)
	if err != nil {
		c.setErr(err)
		return fmt.Errorf("初始化认证层失败: %w", err)
	}

	// 事件驱动重认证：代理层发现上游失败时立刻重登
	watcher := reauth.New(reauth.Options{
		Handler: func(wctx context.Context, srcIP string) error {
			c.mu.Lock()
			m := c.mgr
			c.mu.Unlock()
			if m == nil {
				return errors.New("认证层未运行")
			}
			return m.ReauthNow(wctx, srcIP)
		},
		Logger: c.logger,
	})
	go watcher.Run(c.ctx)

	c.mu.Lock()
	c.mgr = mgr
	c.username, c.password = username, password
	c.remember = remember
	c.lastErr = ""
	c.dialFail = watcher.OnDialFailure
	c.mu.Unlock()

	go consumeAuthEvents(c.ctx, mgr, c.pool, c.logger)

	for _, e := range c.entries {
		if err := mgr.Start(c.ctx, e); err != nil {
			c.logger.Error("启动认证实例失败", "id", e.ID, "err", err)
		}
	}
	c.logger.Info("认证层已启动", "account", maskAccount(username), "sources", len(c.entries))
	return nil
}

// stopManager 关闭现有认证层（如果有）。
func (c *authController) stopManager() {
	c.mu.Lock()
	mgr := c.mgr
	c.mgr = nil
	c.mu.Unlock()
	if mgr != nil {
		_ = mgr.Close()
	}
}

// verifyCredential 尽可能做一次"真实"登录验证。
//
// 三个坑，都实测踩过：
//  1. 在已经在线 的 IP 上登录不算验证——门户对已认证来源直接放行，
//     错误密码也会返回成功，用户到会话过期才发现密码错了。
//  2. 用不在本机的 IP（网卡拔了、IP 变了）登录会报 bind 错误，
//     那不是"密码错"，不能拿来判断凭据。
//  3. 为了验证而登出用户当前唯一在线的 IP，风险太大。
//
// 所以策略是：
//   - 只挑"能绑定"的源 IP；
//   - 优先挑离线的（全新登录 = 真验证）；
//   - 没有离线 IP 就不做预验证，交给认证子进程在后台确认，
//     界面显示"已提交，正在验证"。
func (c *authController) verifyCredential(username, password string) (pending bool, err error) {
	if len(c.entries) == 0 {
		return false, errors.New("没有可用的源 IP，无法验证账号")
	}

	target, fresh, found := c.pickVerifyTarget()
	if !found {
		c.logger.Warn("没有可绑定的源 IP，跳过登录前验证，交由认证子进程确认")
		return true, nil
	}

	client := drcom.Client{
		Portal:   c.opts.Account.Portal,
		Port:     c.opts.Account.Port,
		Username: username,
		Realm:    c.opts.Account.Realm,
		Password: password,
		SourceIP: target.SourceIP,
	}
	ctx, cancel := context.WithTimeout(c.ctx, 25*time.Second)
	defer cancel()

	// 只对"原本离线"的 IP 做验证性登录。
	//
	// 绝不用"先登出再登录"的方式验证在线 IP——实测这样一旦密码错，
	// 会把用户当前唯一可用的连接踢下线，代价远大于收益。
	// 所有 IP 都在线时跳过预验证，交给认证子进程在后台确认，
	// 界面显示"已提交，正在后台验证"。
	if !fresh {
		return true, nil
	}

	// 登录前验证同样要"选对节点 + 验证生效"，否则手动登录也会假成功。
	if err := client.LoginWithDiscovery(ctx, c.opts.Portals); err != nil {
		c.setErr(err)
		return false, fmt.Errorf("认证失败：%w", err)
	}
	c.logger.Info("账号验证通过", "account", maskAccount(username),
		"via", target.SourceIP, "fresh_login", fresh)
	return false, nil
}
func (c *authController) setErr(err error) {
	c.mu.Lock()
	if err != nil {
		c.lastErr = err.Error()
	}
	c.mu.Unlock()
}

// loadCredential 读取本机保存的凭据。
func (c *authController) loadCredential() (*storedCredential, error) {
	data, err := os.ReadFile(c.credPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sc storedCredential
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, err
	}
	if sc.Username == "" || sc.Password == "" {
		return nil, nil
	}
	return &sc, nil
}

// saveCredential 把凭据写到本机（0600）。
func (c *authController) saveCredential(username, password string) error {
	if err := os.MkdirAll(c.credDir, 0o700); err != nil {
		return err
	}
	sc := storedCredential{Username: username, Password: password, SavedAt: time.Now().Format(time.RFC3339)}
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return err
	}
	path := c.credPath()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// maskAccount 只保留账号前 4 位，避免日志和界面里出现完整学号。
func maskAccount(s string) string {
	if len(s) <= 4 {
		return "***"
	}
	return s[:4] + "***"
}

// pickVerifyTarget 选一个"能绑定"的源 IP 用于验证。
//
// 返回 fresh=true 表示该 IP 当前不在线——用它登录才是一次真实验证。
// found=false 表示没有任何可绑定的源 IP（网卡可能都断了）。
func (c *authController) pickVerifyTarget() (entry contract.IPPoolEntry, fresh bool, found bool) {
	online := make(map[string]bool)
	for _, e := range c.pool.Snapshot() {
		online[e.ID] = e.AuthStatus == contract.AuthOnline
	}

	var offline []contract.IPPoolEntry
	var live []contract.IPPoolEntry
	for _, e := range c.entries {
		if !c.canBind(e.SourceIP) {
			c.logger.Warn("源 IP 当前不可绑定，跳过验证候选", "ip", e.SourceIP)
			continue
		}
		if online[e.ID] {
			live = append(live, e)
		} else {
			offline = append(offline, e)
		}
	}
	if len(offline) > 0 {
		return offline[0], true, true
	}
	// 全都在线：不做预验证（宁可不验证，也不冒险登出用户的连接）。
	_ = live
	return contract.IPPoolEntry{}, false, false
}

// canBind 用一次 TCP 连接判断该源 IP 是否还能绑定。
//
// 网卡拔掉或 DHCP 换了地址后，旧 IP 会留在配置里但绑不上；
// 此时用它登录会得到 bind 错误，那不是凭据问题。
func (c *authController) canBind(srcIP string) bool {
	ip := net.ParseIP(srcIP)
	d := net.Dialer{
		Timeout:   2 * time.Second,
		LocalAddr: &net.TCPAddr{IP: ip},
		// 出口网卡同样要钉：否则 Windows 双网卡时这条"可绑定性"检查会误判成
		// 地址不可用，把一条正常的线路从认证列表里剔掉。
		Control: netsrc.DialControl(ip),
	}
	addr := net.JoinHostPort(c.opts.Account.Portal, strconv.Itoa(c.opts.Account.Port))
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ReplaceEntries 更新认证层持有的源列表（重新扫描后调用）。
func (c *authController) ReplaceEntries(entries []contract.IPPoolEntry) {
	c.mu.Lock()
	c.entries = entries
	c.mu.Unlock()
}

// StartEntry 为一个源启动认证（重新扫描发现新 IP 时调用）。
//
// 认证层没在运行时什么也不做——等用户登录后统一启动。
func (c *authController) StartEntry(ctx context.Context, e contract.IPPoolEntry) error {
	c.mu.Lock()
	mgr := c.mgr
	c.mu.Unlock()
	if mgr == nil {
		return nil
	}
	return mgr.Start(ctx, e)
}

// StopEntry 停止某个源的认证（地址已不存在时调用）。
func (c *authController) StopEntry(id string) error {
	c.mu.Lock()
	mgr := c.mgr
	c.mu.Unlock()
	if mgr == nil {
		return nil
	}
	return mgr.Stop(id)
}
