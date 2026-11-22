> ## ⚠️ 本文的认证协议部分已被取代
>
> 本文写作时按"深澜（Srun）+ BitSrunLoginGo"设计。**实测发现农大是 Dr.COM ePortal**，
> `BitSrunLoginGo` 完全不适用，认证器已改为本仓库自研的 `cmd/caulink-auth`。
>
> - 协议与验证记录：[`docs/adr/0004-drcom-auth.md`](../adr/0004-drcom-auth.md)
> - **仍然有效**：§5 的进程看护、§6 的状态判定（探测优先、不解析日志）、§7 的退避与熔断、
>   §8 的状态机——这些与具体认证协议无关，`internal/auth` 的实现正是按它们做的。
> - **历史内容**：§2（BitSrunLoginGo 能力边界）、§4（Config.yaml 模板）、§13（AGPL 叙事）
>   已不再适用，保留仅为记录当时的判断依据。
# 认证层设计（首轮任务 3）

> 对应《要求.md》§3 认证层、§5 角色 A 职责、§6 AGPL 策略、§8 任务 3。
> 落地位置：`internal/auth/`、`cmd/caulinkd/`。配套 `internal/ippool`（唯一写入方）。
> 本文所有关于 BitSrunLoginGo 的结论都标注了来源，`[README]` = 上游 README，`[待实测]` = 必须自己验证后才能写死。
>
> **状态**：`internal/auth` 已实现并通过测试，见 §16。二进制版本锁定 `user_type` 字段版本，平台锁定 Linux。

---

## 1. 先说结论

**每张网卡一个 `BitSrunLoginGo` 实例，用 `--interface` 锁定单网卡，用独立配置文件隔离参数，用外部主动探测判定状态，不解析日志。**

不是因为它不支持多网卡（它其实原生支持），而是因为项目需要**按 IP 粒度的独立控制**：独立重启、独立退避、独立日志归属。用一个进程管全部网卡，调度层就失去了对单个 IP 的控制权。

---

## 2. BitSrunLoginGo 的能力边界（设计的事实基础）

这一节是全篇最重要的部分。下面每一条都直接约束了架构，先确认再动笔。

| # | 能力 | 依据 | 对本项目的影响 |
|---|---|---|---|
| 1 | 多网卡模式：`settings.basic.interfaces` 填网卡名正则（如 `eth0\.[2-3]`），不为空即启用，对匹配的每张网卡登录 | `[README]` | 上游原生支持多拨，**不需要 fork，也不需要 network namespace** |
| 2 | 单网卡：`--interface eth0` 指定单张网卡，**此时多网卡模式无效** | `[README]` | 这是"每 IP 一实例"方案的落点，两种模式互斥，不会打架 |
| 3 | 按网卡覆盖登录参数：`settings.interface_forms.<网卡名>` 可覆盖 `domain/username/password/user_type` | `[README]` main 分支 | 每张网卡可以用不同账号。单实例多网卡方案也能做多账号。**需确认所用 release 是否已含此字段** |
| 4 | 守护模式：`guardian.enable` + `guardian.duration`（默认 300s）周期检查在线状态 | `[README]` | "断连静默重认证"直接用它，不用自己实现 |
| 5 | 指数退避：`backoff.enable/max_retries/initial_duration/max_duration/exponent_factor` | `[README]` | 登录失败的退避已内建，**我们别再造一套** |
| 6 | 网卡绑定「在 Linux 下稳定生效，但在其它系统中可能无法成功绑定」 | `[README]` 原文 | **硬约束**：Windows 上多 IP 认证不可靠，Phase 0/1 必须 Linux |
| 7 | 「少部分学校中在多拨中使用同一个账号时……同一账号在同一网关下的多个设备可能会共享带宽限制」 | `[README]` 原文 | 这正是 Phase 0 要验证的假设，官方文档自己就提示了风险 |
| 8 | 可作为 Go module 引入（`pkg/srun`） | `[README]` | **禁用**。AGPL 传染，见 §10 |
| 9 | 无 JSON 状态输出、无状态查询接口 | `[README]` 中未提供 | 只能靠进程状态 + 外部探测判定，见 §7 |

### 2.1 从这 9 条推出的三个硬结论

**结论一：不需要改它一行代码。** 上游的 `--interface` + 独立配置文件已经满足"每 IP 独立控制"。任何需要改源码才能实现的需求，都要先怀疑是不是方案设计错了 —— 因为改源码会触发 AGPL 全项目开源（§10）。

**结论二：Linux 是唯一可行的开发平台。** 第 6 条不是"可能有坑"，是官方明说"其它系统可能无法成功绑定"。如果在 Windows 上调试，你会看到"认证成功但所有实例都从同一张网卡出去"，表现为多个 IP 认证出同一个 IP，排查起来极其浪费时间。

**结论三：别自己实现退避和保活。** 第 4、5 条已经把这两件事做完了。我们的进程管理器只做**进程级**的存活看护，不做认证级重试。两层混在一起会出现"外层疯狂重启、内层还在退避"的互相干扰，见 §5。

---

## 3. 进程模型：每源 IP 一个实例

### 3.1 两种方案对比

| | 方案 A：单实例 + `interfaces` 正则 | 方案 B：每 IP 一实例 + `--interface` |
|---|---|---|
| 进程数 | 1 | N（校园网场景 2–4 个） |
| 配置文件 | 1 份 + `interface_forms` | N 份 |
| 单 IP 独立重启 | ✗ 只能整体重启，会打断其他 IP 的会话 | ✓ |
| 单 IP 独立退避 | ✗ 全局参数 | ✓ |
| 日志归属哪张网卡 | 混在一起，需要按行猜 | 天然隔离，一进程一日志 |
| 状态映射到 `IPPoolEntry` | 要解析多网卡日志，脆弱 | 进程状态直接对应 `id` |
| 定位故障 | 一个 IP 出问题要翻整份日志 | 直接看对应实例 |

**选 B。** 方案 A 省了 N-1 个进程，但把可观测性和控制粒度全丢了。而本项目的核心卖点就是"按 IP 调度"，调度层必须能精确知道每个 IP 的认证状态、并在单个 IP 认证失败时不牵连其他 IP。

N=2~4 的进程开销可以忽略（每个实例 RSS 约 10–20MB）。

### 3.2 每个实例的启动形态

```text
bitsrun --config <userdata>/auth/<id>/Config.yaml --interface <iface>
```

- `--config` 指向该 IP 专属的配置文件（不同实例互不干扰）
- `--interface` 锁定单网卡，同时让配置文件里的 `interfaces` 正则失效（第 2 条），双保险
- 进程以**独立进程组**启动（`Setpgid: true`），确保停止时能连同它可能派生的子进程一起清理
- stdout/stderr 重定向到该实例的日志文件，不占用我们的管道（避免管道写满导致子进程阻塞）

### 3.3 `id` 与实例的对应关系

沿用 `pkg/contract/v1` 的约定：`id = <interface>@<source_ip>`，如 `eth1@10.3.21.7`。

实例目录 `<userdata>/auth/<id_sanitized>/`，`@` 在路径里安全，但为可读性统一替换为 `_` → `eth1_10.3.21.7`。

---

## 4. 配置文件生成

### 4.1 模板

```yaml
# 适配 BitSrunLoginGo vX.Y.Z —— 升级二进制必须同步核对本文件（见 4.4）
form:
  domain: <portal_domain>
  username: "<account>"
  user_type: "<carrier>"      # 农大若无需运营商后缀，留空字符串
  password: "<password>"
meta:
  n: "200"
  type: "1"
  acid: "<acid>"
  enc: "srun_bx1"
  os: Windows 10
  name: windows
  info_prefix: SRBX1
  double_stack: false
settings:
  basic:
    https: false
    skip_cert_verify: false
    timeout: 5
    interfaces: ""            # 必须留空。非空会启用多网卡模式，与 --interface 冲突
    interfaces_interval: 0
    dns_server: ""
  guardian:
    enable: true              # 断连静默重认证靠它
    duration: 60              # 见 4.3 调优
  backoff:
    enable: true
    max_retries: 0            # 无限重试直到成功
    initial_duration: 2
    max_duration: 300
    exponent_factor: 1
  log:
    debug_level: false
    write_file: true          # 便于事后审计与调试
    log_path: "<userdata>/auth/<id>/"
    log_name: "bitsrun.log"
```

### 4.2 三条硬性要求

1. **`basic.interfaces` 必须留空。** 填了就会进多网卡模式，`--interface` 被忽略，结果是每个实例都去登录所有网卡 —— 症状是"配置改了没反应"，极难排查。
2. **文件权限 0600。** 里面有明文密码。Linux 上 `os.OpenFile(..., 0600)`；写之前用 `os.MkdirAll(dir, 0700)`。
3. **文件不进仓库、不进日志、不进崩溃报告。** 路径固定在用户数据目录（Linux `~/.local/share/caulink/auth/`，Windows `%LOCALAPPDATA%\caulink\auth\`）。

### 4.3 `guardian.duration` 怎么定
上游默认 300s。对"手动重认证明显减少"这个目标，300s 意味着掉线后最多 5 分钟才恢复，用户能明显感知断网。

建议初始 **60s**，然后观察：
- 若认证服务器开始限流或返回错误（日志里连续失败）→ 调回 120–300s
- 若用户反馈恢复太慢 → 降到 30s，但要注意对 portal 的压力

这个值属于 A 的"守护调优"职责，Phase 1 期间做 A/B 对比。**不要一开始就设 10s**，那是在对学校认证服务器做压力测试，既不合理也不合规。

### 4.4 配置模板与二进制版本是耦合的

查资料时发现同一个配置项在不同版本/分支里叫过两个名字：`usertype`（旧）和 `user_type`（当前）。这类改名在上游发生过，说明**内嵌模板必须和锁定的二进制版本绑死**，不能"随便下个最新版就能用"。

规则：

1. `third_party/README.md` 记录版本号 + sha256 + 下载地址；
2. §4.1 的模板顶部加注释写明适配版本（`# 适配 BitSrunLoginGo vX.Y.Z`）；
3. 升级二进制 = 改模板 + 重跑集成测试，单独提一个 PR，不要混在功能分支里；
4. **不要依赖 YAML 解析器的容错。** 字段名写错时，上游很可能不报错而是静默使用默认值 —— 表现为"账号密码都填了，就是认证不上"，且日志里看不出配置有问题。

第 4 条对应 supervisor 里一个必须实现的行为：**子进程启动后 3 秒内退出，判定为配置错误**（正常启动会进入守护循环），直接上报明确原因并熔断，而不是按进程崩溃去指数退避重启 —— 重启一百次也还是配置错误。

---

## 5. 两层退避的分工（最容易写出 bug 的地方）

系统里同时存在两个"重试"，如果不划清边界，会互相破坏。

| 层级 | 触发条件 | 负责方 | 参数 | 表现 |
|---|---|---|---|---|
| **认证级重试** | 密码错、portal 拒绝、登录请求失败 | BitSrunLoginGo `settings.backoff` | 2s 起，指数增长，上限 300s | 进程存活，内部自己重试，不退出 |
| **进程级重启** | 进程 panic、被 OOM kill、异常退出、配置错误 | 我们的 supervisor | 1s→2s→4s→…→上限 60s | 进程没了，我们拉起新进程 |

### 5.1 三条规则

1. **supervisor 不判断"认证是否成功"。** 它只看进程是否还活着。认证失败是子进程内部的事，它会自己退避重试，不需要我们插手。
2. **重启退避必须带"稳定窗口"。** 进程存活超过 60s 才把退避计数清零。否则出现"启动即崩"的 flapping 时，计数永远清零，等于没有退避，会在 10 秒内 spawn 上百次进程。
3. **连续崩溃要有熔断。** 同一 `id` 在 5 分钟内崩溃超过 5 次，标记为 `offline` 并停止重启，同时上报事件让 UI 提示用户（大概率是配置错了，比如密码变更，盲目重启没有意义）。用户改配置后才允许重新启动。

### 5.2 为什么不把退避统一到 supervisor

因为两层的时间尺度完全不同：认证失败可能连续几十次（密码错时），进程崩溃应该是罕见的。合并成一个计数器会导致"认证失败次数"影响"进程重启间隔"，语义混乱，且会让内建 backoff 失效。

---

## 6. 状态机

```text
              ┌──────────────┐
   start ────►│reauthenticating│◄──── 进程重启 / 探测失败
              └───────┬──────┘
                      │ 探测确认在线
                      ▼
                 ┌─────────┐
                 │ online  │
                 └────┬────┘
       探测连续失败 N 次 │
                      ▼
              ┌──────────────┐
              │reauthenticating│
              └───────┬──────┘
                      │ 熔断 / 用户停用 / 配置无效
                      ▼
                 ┌─────────┐
                 │ offline │
                 └─────────┘
```

映射到 `contract.AuthStatus` 的三态（v1 契约只有三个值，不擅自扩展）：

| 实际情形 | `auth_status` | 说明 |
|---|---|---|
| 进程刚启动，尚未确认在线 | `reauthenticating` | |
| 进程存活 + 探测通过 | `online` | 调度池可用 |
| 进程存活但探测持续失败 | `reauthenticating` | 子进程在内部重试中 |
| 进程崩溃且未超熔断阈值 | `reauthenticating` | 正在被拉起 |
| 进程崩溃且已熔断 / 被用户停用 | `offline` | 需要人工介入 |
| 配置无效（密码为空等） | `offline` | 启动前校验直接拒绝 |

**默认写 `reauthenticating` 而不是 `online`。** 只有探测确认在线才转 `online`。反过来（先乐观置 online）会让调度层把流量分到一个还没认证通的 IP 上，表现为"一连就断"。

---

## 7. 状态判定：主动探测，不解析日志

### 7.1 为什么不用日志解析

`[README]` 没有对日志格式做任何承诺。日志是中英混排的自由文本，格式随版本变化。用它做状态判据意味着：上游一次小版本升级就可能让整个状态机失效，而且很难查 —— 因为症状是"认证明明成功，UI 显示离线"。

日志只做**归档和人工排查**，不进状态判定链路。

### 7.2 主判据一：进程存活

`cmd.Wait()` 返回即判定进程退出，记录退出码与最后 N 行日志（供上报），进入重启流程。

注意 Go 的坑：必须**始终**有人调用 `Wait()`，否则子进程退出后变僵尸进程。supervisor 里每个实例一个 goroutine 专职 `Wait`。

### 7.3 主判据二：从该源 IP 主动探测

这是我们自己控制的、不依赖 BitSrunLoginGo 的判定手段：

```go
d := net.Dialer{
    Timeout:   5 * time.Second,
    LocalAddr: &net.TCPAddr{IP: net.ParseIP(entry.SourceIP)},
}
conn, err := d.DialContext(ctx, "tcp", "www.msftconnecttest.com:80")
```

`LocalAddr` 强制从指定源 IP 发起连接，这正好也是 Phase 0 测速脚本要用的同一套机制（复用）。

判定逻辑：
- 请求 `http://www.msftconnecttest.com/connecttest.txt`，期望 200 且 body 为 `Microsoft Connect Test`
- 拿到 302/200 但 body 是认证页 → 未认证
- 连接超时/DNS 失败 → 该 IP 不可用（链路问题，不一定是认证问题）

探测周期 **15–30s**，比 guardian 的 60s 快，这样 UI 的状态变化跟得上用户感知。

### 7.4 为什么不复用 guardian 的检测结果

因为它不对外暴露。而且即使将来暴露，我们也不该依赖 —— 那是它的内部实现，不是契约。

---

## 8. 与调度层的交界（最容易埋雷的一处）

**guardian 的探测流量必须绕过 TUN。**

这是 A 和 B 之间一条必须写进接口约定的规则，忽略它的后果是死循环：

```text
TUN 接管全部流量 → guardian 的在线检测请求被 TUN 捕获
→ TUN 走代理 → 代理按 IP 池状态选路 → 而 IP 池状态正等这次检测结果
→ 检测永远拿不到直连答案，或拿到被代理污染的答案 → 状态判定错乱
```

更糟的一种：代理把检测请求也发到某个已认证 IP 上，guardian 看到"能连通"，误判在线，实际上这张网卡的认证早就掉了。

**三种排除方式，按可靠性排序：**

| 方式 | 做法 | 可靠性 | 复杂度 |
|---|---|---|---|
| 按进程排除 | 认证相关进程放进独立 cgroup，TUN 规则跳过该 cgroup | 高 | 中 |
| 按 uid 排除 | 认证进程用专用 uid 运行，TUN 规则跳过该 uid | 高 | 中（需 root 建用户） |
| 按目的地址排除 | TUN 路由表里给检测端点 IP 加 direct 规则 | 中 | 低（但端点 IP 会变） |

**建议：Linux 上用 cgroup 方式**，因为它按进程边界而不是按用户，将来同一个用户跑多个实例时不会互相影响。

这条规则要写进 A→B 的交接：**B 在做 TUN 路由规则时，必须为 `internal/auth` 启的子进程留出直连白名单。**

---

## 9. 与 ippool 的接口

认证层是 `auth_status` 的**唯一写入方**（见《仓库骨架设计.md》§3 归属矩阵）。

### 9.1 对外接口

```go
package auth

// Manager 管理所有认证子进程。实现者只有一个，A 负责。
type Manager interface {
    // Start 启动某个 IP 的认证守护。幂等：已在运行则直接返回 nil。
    Start(ctx context.Context, entry contract.IPPoolEntry) error

    // Stop 优雅停止，先 SIGTERM，超时后 SIGKILL。幂等。
    Stop(id string) error

    // Status 返回当前状态。不存在则返回 offline。
    Status(id string) (contract.AuthStatus, error)

    // Events 状态变化事件流。ippool 订阅后更新池内条目。
    Events() <-chan Event
}

// Event 状态变更事件。只在状态真正改变时发送，不发心跳。
type Event struct {
    ID         string
    Prev, Next contract.AuthStatus
    Reason     string    // 简短原因，进日志和 UI，不含密码
    At         time.Time
}
```

### 9.2 为什么用事件而不是轮询

轮询会让 ippool 和 auth 的周期互相耦合：ippool 多久刷一次？auth 多久写一次？定不出来。事件驱动下，auth 是唯一知道"状态刚变了"的角色，它推一次，ippool 更新一次，`last_seen` 也就自然准确。

### 9.3 写入边界

- `auth_status`、`last_seen`：**只有 auth 层写**
- `health_score`、`rtt_ms`、`loss_rate`：只有 `internal/health`（B）写
- 两方都不改对方的字段，通过 ippool 的字段级更新接口操作，不是整条覆盖

> 这条要写进 `internal/ippool` 的实现：提供 `UpdateAuth(id, status, at)` 和 `UpdateHealth(id, score, rtt, loss)` 两个窄接口，不给"整条替换"的方法。否则一定会出现 A 写入时把 B 刚算好的 health_score 覆盖成 0 的 bug。

---

## 10. AGPL 边界（三条红线）

`BitSrunLoginGo` 是 AGPL-3.0。以下三条任何一条被打破，整个项目必须以 AGPL-3.0 开源。

1. **不 `import "github.com/Mmx233/BitSrunLoginGo/pkg/srun"`。** `[README]` 明确演示了作为 module 使用的写法，诱惑很大。一旦 import，本项目成为衍生作品，必须整体 AGPL。
2. **不修改其源码、不自行编译改版二进制。** 网上能找到加了 `net_iface` 字段的 fork（`It-is-Sean/BitSrunLoginGo`），**不要用**。用 fork 的 release 二进制在法律上仍是"独立程序调用"，但一旦形成"我们依赖某个私有 fork 的独有功能"，后续维护和合规叙述都会变复杂。坚持用上游 release。
3. **交互只走进程边界。** 只通过 `exec.Command` + 命令行参数 + 配置文件 + 退出码 + 日志文件。不通过共享内存、不通过 cgo、不加载它的 `.so`。

### 10.1 实证方式

`third_party/README.md` 里记录版本号 + sha256 + 下载地址，每次换二进制单独提 PR。这是"我们用的是未修改的上游二进制"最直接的证据。

### 10.2 需要主动披露的话术

> 认证守护通过独立子进程调用 AGPL-3.0 的 `BitSrunLoginGo` 实现，调度层与其进程级隔离。本项目不修改、不 import、不复制其源码。

---

## 11. 账号配额：已知代价，不做额度管理

农大账号限 **3 台设备**。多 IP 同时认证 = 多个设备会话，可能把用户自己的手机和另一台电脑挤下线。

**团队决定：使用者已知晓该程序会占用设备额度，不做额度提示、不做 IP 数量上限开关。** 这部分功能不实现，`internal/auth` 不感知账号配额。

保留的观察项（不是功能需求，是 Phase 0 的记录项）：多 IP 同时认证时其他设备是否掉线，结果写进验证报告 §6。如果确实会踢设备，属于产品说明里必须写明的使用前提，不影响代码。

---

## 12. 待实测清单（Phase 0 期间必须完成）

设计里有 6 处标了 `[待实测]`，对应下表。这些不验证就写死代码，等于在猜。

| # | 待验证 | 方法 | 影响 |
|---|---|---|---|
| 1 | `--interface` 是否真从指定网卡发出认证请求 | Linux 上 `tcpdump -i <iface> -nn host <portal>`，对比两个实例的源 IP | 决定方案 B 是否成立。**最高优先级** |
| 2 | 掉线后 guardian 多久恢复 | 手动踢下线（或拔网线 10s），记录从断到恢复的耗时 | 定 `guardian.duration` |
| 3 | 收 SIGTERM 是否干净退出 | `kill -TERM <pid>`，看退出码与残留子进程 | 决定 `Stop()` 的超时与是否需要 SIGKILL |
| 4 | 日志是否可稳定解析 | 跑 1 小时，看同一事件是否总输出相同前缀 | 决定是否值得加日志解析（当前设计是不加） |
| 5 | 同账号多 IP 是否挤掉其他设备 | 手机保持在线，另开 2 个 IP 认证，看手机是否掉线 | 仅作使用前提记录（见 §11），不影响代码 |
| 6 | 多 IP 并发总吞吐是否超过单 IP | Phase 0 测速脚本，多 IP 并发 vs 单 IP | 决定走"聚合"还是"冗余"主线 |
| 7 | Windows 上 `--interface` 的实际行为 | 同上第 1 项，在 Windows 上重做 | 决定演示平台。预期：不生效 |

第 5、6 项和《要求.md》§2 的决策门直接对应，属于 A 的 Phase 0 工作，不是纯认证层任务，但结论会反过来决定认证层要不要继续做多 IP。

---

## 13. 实现清单

按依赖顺序，A 的下一步：

| 文件 | 内容 | 依赖 |
|---|---|---|
| `internal/auth/config.go` | 配置模板渲染、目录创建、0600 权限 | 无 |
| `internal/auth/probe.go` | 源 IP 绑定探测（`net.Dialer.LocalAddr`） | 无 |
| `internal/auth/process.go` | 单个实例的 exec / Wait / SIGTERM / 日志重定向 | 无 |
| `internal/auth/supervisor.go` | 多实例管理、两层退避、熔断、事件发送 | 上面三个 |
| `internal/auth/manager.go` | 实现 §9.1 的 `Manager` 接口 | supervisor |
| `internal/ippool/`（协作） | A 写状态字段的窄接口 | 与 B 约定字段边界 |

先做 `probe.go`。它不依赖 BitSrunLoginGo，能独立测试，而且 Phase 0 测速脚本也要复用它 —— 先做这个等于同时推进两件事。

---

## 14. 明确不做的事

- **不自己实现 SRun 认证协议。** 那是 `BitSrunLoginGo` 的职责，重写既费时又违反合规叙事。
- **不解析加密表单字段**（`info`、`password` 的 enc 编码）。`--auto-enc` / `--auto-acid` 已经解决了。
- **不做多账号池化调度。** 契约 v1 没有 `AccountID` 字段（见《仓库骨架设计.md》§8 第 2 条），确定要支持再加。
- **不在 Windows 上做多 IP 认证。** 第 2 节第 6 条已经说明原因。Windows 演示只做 SOCKS5 单 IP 接管。

---

## 15. 已确认的事项

| 问题 | 结论 | 影响 |
|---|---|---|
| 限速锚定 IP 还是账号 | **锚定 IP**（团队判断） | 走「多 IP 聚合」主线，认证层按多实例设计。注意：这是判断，Phase 0 的实测仍需跑一遍作为答辩证据 |
| 二进制版本 | **`user_type` 字段版本**（当前上游） | §4.1 模板按此生成；`usertype` 旧版的字段名会导致配置静默失效 |
| 开发与演示平台 | **Linux** | §2 第 6 条已说明 Windows 的网卡绑定不可靠；多 IP 认证不在 Windows 上做 |
| 设备额度提示 | **不做** | 见 §11 |

---

## 16. 实现状态

`internal/auth` 已实现并通过测试（`go build` / `go vet` / `go test` 全绿）。

| 文件 | 内容 | 对应章节 |
|---|---|---|
| `auth.go` | Manager 接口、Options、Account、New | §9.1 |
| `config.go` | Config.yaml 模板渲染（`user_type` 版）、0600 权限 | §4 |
| `probe.go` | 源 IP 绑定的在线探测，三种结论 | §7.3 |
| `process.go` | 子进程启动、等待、优雅停止、日志重定向 | §3.2 |
| `proc_unix.go` / `proc_windows.go` | 进程组与终止方式的平台差异 | §3.2 |
| `worker.go` | 单实例看护循环：两层退避、稳定窗口、熔断、配置错误判定 | §5、§6 |
| `supervisor.go` | 多实例管理、事件分发、幂等 Start/Stop | §9 |

测试覆盖：配置字段与权限、探测的三种结论（含跨主机重定向劫持、同主机重定向不误杀）、
启动/停止幂等、事件顺序、启动即退熔断、非法 entry 拒绝。

**未覆盖的部分**：真实 `BitSrunLoginGo` 二进制与真实校园网环境。§12 的 7 项待实测依旧要跑，
测试只保证"逻辑对"，不保证"和学校的认证服务器合得来"。

### 16.1 实现过程中发现的两个真 bug（已修）

记下来是因为它们都属于"只看代码看不出来"的类型：

1. **`elapsed` 算晚了。** 最初在 `wg.Wait()` 之后计算进程存活时长，而探测协程的初始 3 秒等待打断不了，
   导致"启动即崩"被算成"运行了 3 秒"，配置错误判定完全失效，退回无限重启。
   修法：`waitExit` 返回后立刻记录时长；同时让探测协程的初始等待可被 stop 打断。

2. **`judge` 提前 return 导致报告全 0**（Phase 0 工具里的同类问题）。统计值没算就返回，
   报告显示成一堆 0.00，看起来像测量失败。
