# Windows 双网卡同网段的出口问题：`IP_UNICAST_IF`

> 实现：`internal/netsrc/unicastif_windows.go` + `dialer`、`drcom`、`auth`、`health`、`authctl`、`phase0probe` 六处接入
> 平台：Windows 10/11（非 Windows 分支自动退化为 `nil`，无平台判断）
> 相关：[`dual-path.md`](dual-path.md)（问题就是在这轮双源实测里暴露的）

---

## 一、问题是什么

`net.Dialer.LocalAddr` 只决定**源 IP**，不决定**包从哪块网卡出去**。

校园网双网卡几乎必然落在同一网段（`10.6.0.0/17`，网关都是 `10.6.0.1`）：

```text
WLAN    10.6.93.195   InterfaceMetric 45   ← 默认路由实际走的出口
WLAN 2  10.6.54.214   InterfaceMetric 50
```

此时绑 `10.6.54.214` 发起的连接，内核按**目的地址**选路，仍然从 metric 更低的 `WLAN` 发出，源 IP 却写着 `10.6.54.214`。包的出口与身份对不上，用户的感受是：

- 第二个 IP「一直认证中」——登录请求 `context deadline exceeded`；
- 第二条线路「连上了但没速度」——健康探测全失败，调度层直接把这条线路判死。

Linux 上不出现这个问题：内核会按源地址反查路由。所以这是**只在 Windows 暴露**的缺陷，Linux CI 也测不出来。

---

## 二、怎么修

`internal/netsrc/unicastif_windows.go` 提供：

```go
// 返回 net.Dialer.Control，在 socket 建好、bind 之前把出口网卡钉住
func DialControl(src net.IP) func(network, address string, c syscall.RawConn) error
```

调用方只需在已有的 `LocalAddr` 旁边补一行 `Control`。改了 6 处源地址绑定点：

| 位置 | 影响 |
|---|---|
| `internal/proxy/dialer/dialer.go` | 代理出站（TCP + UDP），核心链路 |
| `internal/drcom/drcom.go` | 登录 / 注销 / 在线判定 |
| `internal/auth/probe.go` | 认证态探测 |
| `internal/health/health.go` | 链路质量探测（决定调度权重） |
| `cmd/caulinkd/authctl.go` | 登录前的「该 IP 还能不能绑」预检 |
| `cmd/phase0probe/{measure,source}.go` | 测速工具本身 |

非 Windows 平台 `DialControl` 返回 `nil`（`unicastif_other.go`），调用方代码没有平台分支——平台差异只存在于 `internal/netsrc` 一个包里。

---

## 三、一个必须记住的坑：两个选项的字节序**不一样**

| 选项 | 级别 | 选项号 | 索引字节序 | 写错的后果 |
|---|---|---:|---|---|
| `IP_UNICAST_IF` | `IPPROTO_IP`(0) | 31 | **网络序** | `WSAEADDRNOTAVAIL` |
| `IPV6_UNICAST_IF` | `IPPROTO_IPV6`(41) | 31 | **主机序** | `WSAEINVAL` |

这是本机 `setsockopt` 逐个试出来的。第一版按「两个都是网络序」写，v6 用例当场报 `An invalid argument was supplied`，就是这个原因。两者写错都直接失败（不是静默失效），所以 `unicastif_windows_test.go` 里 v4/v6 各钉了一个用例。

另一个更阴的坑：**`IP_UNICAST_IF` 用主机序写进去，`setsockopt` 同样返回成功**，只是内核把它当成一个不存在的接口号，直到发包时才失败。这种静默失效只能靠真机连一条链路上才能发现，`TestBindToInterfaceOnUDP` 就是干这个的。

---

## 四、真机验证（同一台机器，两块网卡都在线）

只绑源 IP vs 加上 `Control`，目标为公网资源：

| 源 IP | 只绑 `LocalAddr` | 加 `Control` |
|---|---|---|
| `10.6.93.195`（metric 45） | HTTP 200 | HTTP 200 |
| `10.6.54.214`（metric 50） | `wsarecv: ... connected host has failed to respond` | HTTP 200 |

也就是说：**修复前第二个 IP 的公网请求是坏的，修复后正常**。

反直觉的一点：校内门户（`10.3.191.9`）两种写法都通——那是同校 L3 内部互访，交换机不做出口校验。所以**只拿门户做验证会误判成「没问题」**，必须用公网目标才复现。

---

## 五、新增的测试

`internal/netsrc/unicastif_windows_test.go`（Windows-only）：

- `TestHtonl` —— 字节序转换
- `TestNetworkFamily` —— `tcp4/tcp6/udp` 的族判定
- `TestDialControlNilSource` —— 没源地址时不干预
- `TestDialControlBindsLoopback` —— 真建一条连接，选项号写错会失败
- `TestDialControlUnknownSourceFails` —— 源地址不在本机必须**报错**，不能悄悄走默认路由
- `TestBindToInterfaceOnUDP` —— v4/v6 两个选项分别验一遍（字节序坑就在这里被抓到）
- `TestInterfaceIndexCaches` —— 索引缓存生效

本地验证命令：

```bash
go build ./...
go test ./internal/netsrc/ ./internal/proxy/dialer/ ./internal/drcom/ ./internal/health/
GOOS=linux go build ./...      # 非 Windows 分支必须仍能编译
```

---

## 六、几个设计取舍

1. **索引缓存 30 秒**：每次拨号都枚举网卡（`GetAdaptersAddresses`）在高并发下载时是浪费；但永久缓存会在 USB 网卡重插拔后把包发到错误网卡（故障现象和没修一样）。首次拨号多花约 40 ms，之后命中缓存。
2. **找不到源地址所属网卡时明确报错**，只有 loopback 放过。这种拨号必然超时，报「找不到持有源地址的网卡（网卡已拔出或地址已变更）」比 8 秒后报 timeout 好排查。
3. **没有回退**：`Control` 失败即建连失败，不会被吞掉改成默认路由——与 `dialer` 既有的「绝不回退默认路由」规则一致。

---

## 七、没有覆盖的地方（有意为之）

- `internal/proxy/httpproxy/upstream.go`：连的是**上游代理**，本身不绑源地址，走默认路由是正确行为。
- `internal/proxy/socks5/udp.go` 的应答 socket：监听回环给客户端回包，与出口网卡无关。
- 有线 + 无线、不同 AP 的双链路：本修复对它们同样有效，但**叠加比**仍受上游拓扑限制（同 AP 只有 1.39×，见双源实测报告）。

---

## 八、发布时注意

1. 这个修复改变了**认证与出站拨号**，必须重新构建 `caulinkd.exe` / `caulink-auth.exe` / `caulink-ui.exe` 并重打发布包，旧 exe 不含修复。
2. 换一台连不同 AP 的机器重跑双源测速，确认叠加比是否接近 2×。
