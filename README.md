# Caulink（农链智调）

面向中国农业大学师生校园网账号限 3 台设备、单接口约 6MB/s 且频繁断连重认证的问题，本项目是一款本地多 IP 智能调度 APP。用户一键开启后，APP 自动完成多 IP 认证保活，以本地代理/TUN 接管流量，按连接将并发下载分散到不同源 IP，将游戏、会议等敏感流量固定到最优 IP；断连时静默重认证并自动故障切换。在限速锚定 IP 的前提下，多线程下载总吞吐可接近多 IP 之和，单连接仍受单 IP 限制；手动重认证和掉线感知明显减少。

## 实测结论（Phase 0）

| 场景 | 吞吐 |
|---|---|
| 单 IP（Intel Wi-Fi 6E） | **6.10 MB/s** |
| 单 IP（USB 无线网卡） | **6.09 MB/s** |
| **两个 IP 同时下载** | **12.17 MB/s（= 各 IP 之和的 100%）** |

**断连恢复：事件驱动，有流量时 ≤2.5 秒自动重登，空闲时 30 秒兜底。**

**判定：限速锚定在 IP**，每个源 IP 有独立配额，多 IP 可线性叠加。
完整报告见 [`docs/reports/phase0.md`](docs/reports/phase0.md)。

> 注意口径：这证明的是"机制可行"，不是"任何场景都变快"。单 TCP 连接仍受 6MB/s 限制，
> 收益只出现在多连接场景（多线程下载、游戏更新、多分片 CDN）。

单文件也一样能吃到这部分：把文件按 `Range` 切成多片、每片走不同源 IP，
40.8 MB 的镜像 **4.44 秒**下完（8.76 MB/s），单流是 7.19 秒（5.68 MB/s），
**1.50 倍**，两次结果 md5 一致。界面里贴直链、选好保存目录就能用；
分片数上限是「源 IP 数 × 2」，同一 IP 上再多开只会互相抢同一份额度。

## 技术栈

| 层 | 技术 | 目录 |
|---|---|---|
| 守护进程（唯一常驻进程） | Go 1.23 | `cmd/caulinkd`、`internal/*` |
| 认证保活 | 自研 Dr.COM ePortal 客户端（`cmd/caulink-auth`），每源 IP 一个子进程 | `internal/auth` |
| 接管与调度 | Go + SOCKS5（TCP/UDP），连接级加权轮询 | `internal/proxy`、`internal/scheduler` |
| 流量统计 | 每 IP 连接数/字节数，EWMA 速率 | `internal/metrics` |
| 控制面 | 本地 HTTP（仅 `127.0.0.1`） | `internal/control` |
| 加速下载 | 分片跨源 IP 并发（`Range` + `If-Range` 一致性校验），界面内建 | `internal/accel` |
| 桌面控制面板 | **Wails v3**（Go + 系统 WebView2） | `desktop/` |
| 契约 | `pkg/contract/v1`（Go 结构体为真源）+ JSON Schema | `api/schema/v1` |

## 命名约定

- 项目名：`caulink`（农链）
- Go module：`github.com/Br1m2y/caulink`
- 守护进程：`caulinkd`，配置 `configs/caulinkd.example.json`
- 桌面客户端：`caulink-desktop`，独立 module `github.com/Br1m2y/caulink/desktop`

## 目录速览

```text
cmd/caulinkd       守护进程入口（装配所有模块）
cmd/phase0probe    Phase 0 多 IP 并发测速探针
cmd/caulink-auth    Dr.COM ePortal 认证器（由 internal/auth 拉起）
internal/auth      认证子进程管理（拉起 cmd/caulink-auth，每 IP 一个实例）
internal/ippool    IP 池状态中心（唯一写入方：A）
internal/proxy     SOCKS5（TCP+UDP）接管、源 IP 绑定拨号
internal/netsrc    出口网卡绑定（Windows IP_UNICAST_IF），双网卡同网段才走得通
internal/scheduler 连接级调度（平滑加权轮询 + 敏感流量粘性）
internal/health    链路质量探测（RTT/丢包 → 健康分）
internal/metrics   每 IP 流量与连接计数
internal/accel     分片下载器（探测 → 切片 → 跨 IP 并发 → 落盘），带保存目录管理
internal/control   本地控制面 HTTP（仅 127.0.0.1）
internal/classify  流量分类规则（待实现，规则暂在 scheduler/classifier.go）
pkg/contract/v1    ★ 冻结的 v1 契约
api/schema/v1      给前端/脚本消费的 JSON Schema
desktop/           Wails v3 桌面客户端
docs/adr/          架构决策记录
tools/launcher     发布包启动器（校正源列表、腾出 8080、拉起界面）
test/download      真实网络下的并行下载实验（默认跳过，见该目录 README）
```

## 当前实现状态

| 模块 | 状态 |
|---|---|
| `internal/auth` 认证子进程管理 | ✅ 实现 + 测试 + **真机联调**（Dr.COM，见 ADR 0004） |
| `internal/drcom` Dr.COM ePortal 客户端 | ✅ 实现 + 测试（登录/注销/在线判定） |
| `internal/reauth` 事件驱动重认证 | ✅ 实现 + 测试；实测检测→重登 0.3–1.3 秒 |
| `internal/ippool` / `health` / `scheduler` / `dialer` | ✅ 实现 + 测试 |
| `internal/netsrc` 出口网卡绑定 | ✅ 实现 + 测试（双网卡同网段实测） |
| `internal/proxy/socks5` TCP | ✅ 实现 + 真机验证 |
| `internal/proxy/socks5` UDP | ✅ 实现 + 真机验证（DNS 往返） |
| `internal/proxy/httpproxy` | ✅ 实现 + 测试（Steam 用） |
| `internal/metrics` / `control` | ✅ 实现 + 真机验证 |
| `internal/accel` 双通道分片下载 | ✅ 实现 + 测试 + 双网卡实测（1.50×，md5 一致） |
| `cmd/caulinkd` 装配 | ✅ 两种运行模式，优雅退出 |
| `cmd/phase0probe` | ✅ 实现 + 双 IP 实测（并发 12.17 MB/s = 2× 单 IP） |
| `desktop/`（Wails v3） | ✅ 已交付，接真实状态接口（`wails3 build` 出 exe） |
| `internal/proxy/tun` | ❌ 未实现（Windows 需 wintun + netstack，成本高，优先级低） |
| **Phase 0 正式实验** | ✅ 完成：判定**限速锚定 IP** |

任务级进度看 [`docs/kanban.md`](docs/kanban.md)。

## 开始

```bash
# 1) 装 Go 1.23+
winget install --id GoLang.Go -e

# 2) 编译与测试
go vet ./... && go test ./... && go build ./...

# 3) 构建桌面端（可选，需要 Go >= 1.26）
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0
wails3 setup
cd desktop && wails3 build     # 产物 desktop/bin/caulink-desktop.exe
```

## 运行

**模式一：开发/演示（不需要账号、不需要校园网）**

```bash
cp configs/caulinkd.example.json configs/caulinkd.local.json
# 把 sources 改成你本机真实的网卡名与 IP，auth.enabled 保持 false
go run ./cmd/caulinkd -config configs/caulinkd.local.json -v

# 另开一个终端
curl -x socks5h://127.0.0.1:1080 https://mirrors.tuna.tsinghua.edu.cn/ -I
curl -s http://127.0.0.1:7788/api/v1/status
```

`auth.enabled: false` 时程序假设源 IP 已手动认证，直接标为在线并给占位健康分。
这让没有校园网账号的人也能把整条链路跑起来。

**模式二：完整运行（需要真实账号）**

```bash
cp configs/caulinkd.example.json configs/caulinkd.local.json
# 填 sources、auth.username、auth.password（realm 默认 cau、portal 默认 10.3.191.9:801）
go run ./cmd/caulinkd -config configs/caulinkd.local.json
```

> 认证器用 `net.Dialer{LocalAddr}` 绑定源 IP，Windows 上实测可正常工作
> （两块无线网卡各自认证、各自跑满 6MB/s）。

## 配置字段

| 字段 | 说明 |
|---|---|
| `data_dir` | 认证实例的配置与日志目录，权限 0700（内含明文密码） |
| `sources[]` | 参与调度的源 IP。`interface` 是网卡名，`source_ip` 是该网卡上的地址 |
| `auth.enabled` | `true` 启动认证子进程；`false` 假设已手动认证 |
| `auth.binary` | 认证器路径，默认 `bin/caulink-auth.exe` |
| `auth.realm` / `auth.portal` / `auth.port` | Dr.COM 认证域与门户，默认 `cau` / `10.3.191.9` / `801` |
| `auth.guardian_seconds` | 子进程在线检查周期，默认 60。别低于 30 |
| `health.interval_seconds` | 链路探测周期，默认 10 |
| `health.target` | 探测目标 `host:port`，用 TCP 握手测 RTT |
| `socks5.listen` | **必须是回环地址**，非回环拒绝启动 |
| `status.listen` | 控制面监听地址，默认 `127.0.0.1:7788`；写 `""` 关闭 |

> 用 JSON 而不是 YAML：标准库直接支持，仓库保持**零第三方依赖**。
> 早期骨架里的 `.yaml` 示例是实现前的设计稿，字段与实现不一致，已移除。

## 验证

```bash
# TCP：通过代理访问外网
curl -x socks5h://127.0.0.1:1080 https://mirrors.tuna.tsinghua.edu.cn/ -I

# 看每条连接走了哪个源 IP
curl -x socks5h://127.0.0.1:1080 https://example.com -v 2>&1 | grep SOCKS5
```

`-v` 模式下每次建连会输出源 IP：

```text
level=DEBUG msg="连接已建立" target=... source_ip=10.0.0.2 iface=eth0
level=DEBUG msg="连接已建立" target=... source_ip=10.0.0.3 iface=eth1
```

连续跑几次，两个 IP 应交替出现。若始终相同，检查另一个 IP 的 `auth_status` 是否为 `online`、
健康分是否被探测判成 0。

**UDP** 用浏览器更直接：打开 `https://cloudflare.com/`（支持 HTTP/3），日志里会出现：

```text
level=DEBUG msg="UDP 关联已建立" bind=127.0.0.1:63225
level=DEBUG msg="UDP 会话已建立" target=162.159.137.85:443 source_ip=10.0.0.2
```

`curl` 走不了 SOCKS5 的 UDP，所以命令行验证需要一个支持 `UDP ASSOCIATE` 的客户端，
或者以上面的日志为准。

## 已知限制

1. **单条 TCP 连接不能跨多个 IP。** 源地址在连接期内固定，这是 TCP 语义决定的。
   多 IP 只在多条并发连接时才有吞吐叠加。
2. **SOCKS5 支持 CONNECT 与 UDP ASSOCIATE，不支持 BIND。** BIND 只有 FTP 主动模式需要。
3. **UDP 的目标域名用系统 DNS 解析**，这一步不走调度。影响很小但确实没被分摊。
4. **端口分类覆盖不到走 443 的会议流量**，这类流量被当成普通下载，不享受粘性。
5. **健康分只反映到探测目标的链路质量**，不等于到任意目标的质量。
6. **UDP 会话上限 512 / 空闲 60 秒回收**，超限时丢弃新会话而非淘汰旧的。
7. **控制面无历史数据**，只有当前快照，重启即清零。要曲线得客户端自己攒。

## 实验与报告

| 报告 | 内容 |
|---|---|
| [`docs/reports/phase0.md`](docs/reports/phase0.md) | Phase 0 限速锚点验证（判定 `ip_anchored`） |
| [`docs/reports/parallel-download.md`](docs/reports/parallel-download.md) | 「换 IP 就断、不能并行」这个说法的核实，以及四条方案取舍 |
| [`docs/reports/dual-path.md`](docs/reports/dual-path.md) | 双源并行实测：叠加成立、1.39×，瓶颈在同一台 AP |
| [`docs/reports/unicast-if.md`](docs/reports/unicast-if.md) | Windows 双网卡同网段只绑源 IP 会失效，以及 `IP_UNICAST_IF` 的修法 |
| [`docs/reports/accel.md`](docs/reports/accel.md) | 双通道分片下载：实现、接口与实测（1.50×，md5 一致） |

## 相关文档

| 文档 | 什么时候看 |
|---|---|
| [`docs/kanban.md`](docs/kanban.md) | **开工前必读。** 找自己的任务编号，看依赖与验收标准 |
| [`docs/status-api.md`](docs/status-api.md) | 做控制面板时。字段含义、单位、前端模型、兼容性规则 |
| [`docs/squash-checklist.md`](docs/squash-checklist.md) | 点合并按钮前。五项检查 + 合并后三件事 |
| [`docs/git-workflow.md`](docs/git-workflow.md) | 第一次提 PR / 遇到冲突 / 不知道怎么办 |
| [docs/troubleshooting.md](docs/troubleshooting.md) | **出问题时先看这个。** 权限/编码/代理/面板无数据的排查清单 |
| [`docs/steam-setup.md`](docs/steam-setup.md) | 要让 Steam 下载走多 IP 时。含代理配置、验证方法与取消步骤 |
| [`docs/thunder-setup.md`](docs/thunder-setup.md) | 要让迅雷下载走多 IP 时。SOCKS5 配置、BT 任务的注意事项 |
| [`docs/adr/`](docs/adr/) | 为什么选 Wails 而不是 Flutter 等决策记录 |
| `仓库骨架设计.md` | 仓库结构与模块边界的原始设计 |

## 合规

- 认证器为**本项目自行实现**的 Dr.COM 客户端，不依赖任何第三方认证二进制；
- `go.mod` 零第三方依赖（详见 `docs/compliance.md`）；
- 仅限本人授权账号与设备使用，遵守中国农业大学校园网使用规定；
- 所有代理与控制面仅监听 `127.0.0.1`，不对外提供代理服务。
