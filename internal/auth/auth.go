// Package auth 管理校园网认证子进程。
//
// 设计前提（详见 docs/design/auth-layer.md）：
//   - 农大校园网是 **Dr.COM ePortal**（不是深澜 Srun），认证器是本仓库自带的
//     cmd/caulink-auth，仍以独立二进制方式调用，避免凭据与协议逻辑耦合进主进程。
//   - 每个源 IP 一个实例，实例配置里带 source_ip，登录请求绑定该地址发出。
//   - 认证失败的重试交给子进程内的退避；本包只负责进程级看护与重启。
//   - 状态判定靠"进程存活 + 从该源 IP 主动探测"，不解析子进程日志。
//
// 本包是 contract.IPPoolEntry.AuthStatus 的唯一写入方。
//
// 用法：
//
//	m, err := auth.New(auth.Options{
//	    BinaryPath: "/opt/caulink/caulink-auth",
//	    DataDir:    "/home/user/.local/share/caulink",
//	    Account:    auth.Account{Username: "2023001", Password: "***",
//	                             Realm: "cau", Portal: "10.3.191.9", Port: 801},
//	})
//	if err != nil {
//	    return err
//	}
//	defer m.Close()
//
//	// 状态变更通过事件推给 ippool，不要轮询 Status：
//	go func() {
//	    for e := range m.Events() {
//	        ippool.UpdateAuth(e.ID, e.Next, e.At)
//	    }
//	}()
//
//	for _, entry := range entries {
//	    if err := m.Start(ctx, entry); err != nil { /* 记录并跳过 */ }
//	}
//
// 启动后不要假设立刻 online：状态先进入 reauthenticating，
// 只有探测确认在线才转 online。反过来（先乐观置 online）会让调度层
// 把流量分到一个还没认证通的 IP 上，表现为"一连就断"。
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	contract "github.com/Br1m2y/caulink/pkg/contract/v1"
)

// Event 是一次状态变更。只在状态真正改变时发送，不发心跳。
type Event struct {
	ID     string              `json:"id"`
	Prev   contract.AuthStatus `json:"prev"`
	Next   contract.AuthStatus `json:"next"`
	Reason string              `json:"reason"`
	At     time.Time           `json:"at"`
}

// Manager 是认证层对外暴露的唯一入口。
type Manager interface {
	// Start 启动某个源 IP 的认证守护。幂等：已在运行则返回 nil。
	Start(ctx context.Context, entry contract.IPPoolEntry) error

	// Stop 停止并等待退出。幂等：不存在则返回 nil。
	Stop(id string) error

	// Status 返回当前状态。不存在返回 offline。
	Status(id string) (contract.AuthStatus, error)

	// ReauthNow 立即对指定源 IP 做一次「探测 → 必要时登录」。
	//
	// 这是**事件驱动**路径：代理层观察到上游建连失败时调用它，比子进程的
	// 周期轮询更早发现掉线。内部按 Options.ForceCooldown 去抖，
	// 并发调用同一 IP 不会打爆门户。
	//
	// 调用方应放进 goroutine：本方法会做网络请求（探测最多 8s、登录最多 15s）。
	ReauthNow(ctx context.Context, sourceIP string) error

	// Events 返回状态变更事件流。消费方需要持续读取，否则事件会被丢弃。
	Events() <-chan Event

	// Close 停止所有实例并释放资源。
	Close() error
}

// Account 是 Dr.COM ePortal 的登录凭据。所有源 IP 实例共用同一账号。
type Account struct {
	// Username 是学号，**不带** realm 后缀。
	Username string
	Password string
	// Realm 是认证域后缀，登录时拼成 <username>@<realm>。
	// 农大实测为 "cau"；缺了它 RADIUS 会报 "no AT symbol in realm"。
	Realm string
	// Portal 是认证门户地址，例如 10.3.191.9。
	Portal string
	// Port 是 ePortal 端口，农大实测为 801。
	Port int
}

// Options 是 Supervisor 的构造参数。零值会被填成下面注释里的默认值。
type Options struct {
	// BinaryPath 是 BitSrunLoginGo 可执行文件的绝对路径。必填。
	BinaryPath string

	// DataDir 是实例数据根目录，每个实例一个子目录存放 Config.yaml 与日志。必填。
	DataDir string

	// Account 是登录凭据。所有 IP 实例共用同一账号。
	Account Account

	// GuardianDuration 是子进程自身的在线检查周期（秒），默认 15。
	// 这是断连恢复延迟的主要来源：最坏情况要等一个周期才发起重认证。\n\t// 实测一次探测约 0.6s、几百字节，所以 10–15s 是"恢复快"和"请求量"的平衡点；\n\t// 低于 5s 收益递减，且会让门户和探测目标承受不必要的请求。
	GuardianDuration int

	// ProbeURL 是认证层自己用的在线探测地址，默认 http://connect.rom.miui.com/generate_204
	ProbeURL string

	// ProbeInterval 是**有流量时**的探测周期，默认 2s。比 GuardianDuration 小，
	// 这样 UI 上的状态变化能跟上用户感知。
	//
	// 这个间隔同时决定了"门户劫持"被发现的延迟：未认证时门户在网络层拦截 TCP，
	// 代理层看到的是建连成功，只有本探测能识破。探测本身约 0.6s、几百字节，
	// 5 秒一次的开销可以忽略，换来的是掉线后几秒内自动重登。
	ProbeInterval time.Duration

	// ProbeTimeout 是单次探测超时，默认 5s。
	ProbeTimeout time.Duration

	// ActivityFunc 报告"当前是否有用户正在使用的流量"。
	//
	// 探测周期据此自适应：有流量时用 ProbeInterval（密），空闲时用
	// IdleProbeInterval（疏）。理由是不对称的：
	//   - 有流量时掉线用户立刻就能感觉到，值得每 2 秒探一次；此时探测
	//     相对流量本身（几 MB/s）只占万分之一。
	//   - 空闲时没人用，30 秒探一次就够，省下的数据不会影响任何人。
	// 为 nil 时始终用 ProbeInterval。
	ActivityFunc func() bool

	// IdleProbeInterval 是无流量时的探测周期，默认 30s。
	IdleProbeInterval time.Duration

	// StopTimeout 是 SIGTERM 之后等待退出的时长，超时则 SIGKILL。默认 5s。
	StopTimeout time.Duration

	// ForceCooldown 是事件驱动重认证的最小间隔，默认 3s。
	//
	// 代理层一次掉线会连续报告多条建连失败，没有冷却就会并发发起多次登录。
	ForceCooldown time.Duration

	// RestartBase 是进程级重启的初始退避，默认 1s，每次翻倍。
	RestartBase time.Duration

	// RestartMax 是重启退避上限，默认 60s。
	RestartMax time.Duration

	// StabilizeAfter 是"算作稳定运行"的时长，默认 60s。
	// 进程存活超过这个时长才把重启退避重置为 RestartBase，
	// 否则启动即崩的 flapping 会让退避永远清零。
	StabilizeAfter time.Duration

	// CrashWindow 与 CrashLimit 构成熔断：默认 5 分钟内崩 5 次就停止重启。
	CrashWindow time.Duration
	CrashLimit  int

	// ConfigErrorWindow 是"启动后多久内退出判定为配置错误"，默认 3s。
	// 配置写错时子进程会立即退出，这种情况下无限重启毫无意义。
	ConfigErrorWindow time.Duration

	// NetworkErrorLimit 是探测连续出现链路错误的次数上限，达到后把状态降为
	// reauthenticating，默认 3 次。注意链路错误不等于认证失效，
	// 不能因为一次超时就判定掉线。
	NetworkErrorLimit int

	// Logger 为空时使用 slog.Default()。
	Logger *slog.Logger
}

func (o *Options) applyDefaults() {
	if o.GuardianDuration <= 0 {
		o.GuardianDuration = 15
	}
	if o.ProbeURL == "" {
		o.ProbeURL = "http://connect.rom.miui.com/generate_204"
	}
	if o.ProbeInterval <= 0 {
		o.ProbeInterval = 2 * time.Second
	}
	if o.ProbeTimeout <= 0 {
		o.ProbeTimeout = 2 * time.Second
	}
	if o.IdleProbeInterval <= 0 {
		o.IdleProbeInterval = 30 * time.Second
	}
	if o.StopTimeout <= 0 {
		o.StopTimeout = 5 * time.Second
	}
	if o.ForceCooldown <= 0 {
		o.ForceCooldown = 3 * time.Second
	}
	if o.RestartBase <= 0 {
		o.RestartBase = time.Second
	}
	if o.RestartMax <= 0 {
		o.RestartMax = 60 * time.Second
	}
	if o.StabilizeAfter <= 0 {
		o.StabilizeAfter = 60 * time.Second
	}
	if o.CrashWindow <= 0 {
		o.CrashWindow = 5 * time.Minute
	}
	if o.CrashLimit <= 0 {
		o.CrashLimit = 5
	}
	if o.ConfigErrorWindow <= 0 {
		o.ConfigErrorWindow = 3 * time.Second
	}
	if o.NetworkErrorLimit <= 0 {
		o.NetworkErrorLimit = 3
	}
	if o.Account.Realm == "" {
		o.Account.Realm = "cau"
	}
	if o.Account.Port <= 0 {
		o.Account.Port = 801
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

func (o Options) validate() error {
	if o.BinaryPath == "" {
		return errors.New("auth: BinaryPath 不能为空")
	}
	if _, err := os.Stat(o.BinaryPath); err != nil {
		return fmt.Errorf("auth: 找不到认证器二进制 %s: %w", o.BinaryPath, err)
	}
	if o.Account.Portal == "" {
		return errors.New("auth: Account.Portal 不能为空")
	}
	if o.DataDir == "" {
		return errors.New("auth: DataDir 不能为空")
	}
	if o.Account.Username == "" || o.Account.Password == "" {
		return errors.New("auth: Account 的账号或密码为空")
	}
	if o.RestartMax < o.RestartBase {
		return fmt.Errorf("auth: RestartMax(%s) 小于 RestartBase(%s)", o.RestartMax, o.RestartBase)
	}
	return nil
}

// New 创建 Supervisor。调用方拿到的 Manager 已经可以直接 Start。
func New(opts Options) (Manager, error) {
	opts.applyDefaults()

	abs, err := filepath.Abs(opts.BinaryPath)
	if err == nil {
		opts.BinaryPath = abs
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("auth: 创建数据目录 %s: %w", opts.DataDir, err)
	}

	s := &Supervisor{
		opts:    opts,
		workers: map[string]*worker{},
		events:  make(chan Event, 64),
	}
	return s, nil
}
