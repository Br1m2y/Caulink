# 网络栈：IPv4 / IPv6 到底怎么走

> 一句话：**软件是双栈的，但策略上"能 IPv4 就 IPv4"，只有目标本身是 IPv6 时才走 IPv6。**
>
> 本文说明选路规则、源地址从哪来、为什么这么定，以及怎么验证。

---

## 1. 结论先摆出来

| 目标形式 | 实际使用的族 | 说明 |
|---|---|---|
| **域名**（绝大多数场景） | **IPv4 优先** | 解析后优先选 v4；条目没有 v4 源时才用 v6 |
| IPv4 字面量（`101.6.15.130`） | IPv4 | 源地址必须同族 |
| IPv6 字面量（`[2402:f000:1:400::2]`） | IPv6 | 同上；没有 v6 源就**报错**，绝不回退默认路由 |

两条底层约束：

1. **一个 TCP socket 的源地址必须与目标地址同族。**
   用 v4 源去连 v6 目标，内核直接返回 `WSAEADDRNOTAVAIL`
   （中文："在其上下文中，该请求的地址无效"）。
2. **源地址绑定不可省略。** 不绑的话所有连接都从默认路由出去，
   多 IP 调度就形同虚设，而现象是"程序在跑、速度上不去"，极难排查。

---

## 2. 为什么默认优先 IPv4

| 原因 | 说明 |
|---|---|
| 认证是 v4 的 | 校园网门户 `10.3.191.9:801` 是 v4 地址，调度池的"在线"判定建立在 v4 会话上 |
| 稳定性 | 实测 v4 路径的中转与成功率更稳；v6 在部分站点有 MTU / 路由异常 |
| 覆盖面 | 绝大多数下载源、CDN、tracker 都有 v4；v6 是锦上添花，不是默认路径 |

**但 v6 是必须支持的**：校园网 IPv6 资源丰富，BT 对等体大量是 v6 地址。
早期版本只绑 v4 源，导致 qBittorrent 一个对等体都连不上、速度恒为 0
（详见 [`troubleshooting.md`](troubleshooting.md) 第 10 节）。

---

## 3. 各部分实际用的协议

| 组件 | 用的族 | 说明 |
|---|---|---|
| 校园网认证（Dr.COM ePortal） | **只 v4** | 门户地址是 v4；认证子进程绑定 `source_ip` |
| 健康探测 | 目标 `www.msftconnecttest.com` 是域名 → **优先 v4** | 探测同样遵守"源与目标同族" |
| SOCKS5 服务 | **v4 / 域名 / v6 全支持** | 协议层 `atypIPv4(0x01)`、`atypDomain(0x03)`、`atypIPv6(0x04)` |
| HTTP 代理（CONNECT + 明文） | 同上 | 目标来自 `Host` / `URL.Host`，支持 `[v6]:port` 形式 |
| 源地址池 | 每块网卡 **1 个 v4 + 1 个全局 v6** | v4 与 v6 都会自动发现 |

---

## 4. 源地址从哪来

### 4.1 IPv4：配置优先，其余自动发现

枚举本机网卡，**长得像"能用的出口"就收**：网卡 up、不是回环、有全局 IPv4
（`169.254.x.x` 链路本地地址自动排除——那是没拿到 DHCP 的空地址）。

配置里显式写了某个网卡就以配置为准（可以固定 IP、也可以指定 v6 前缀），
没写的网卡自动补进池子：

```json
"sources": [
  { "interface": "WLAN",   "source_ip": "10.6.18.3" },
  { "interface": "WLAN 2", "source_ip": "10.6.53.173" }
]
```

`sources` 留空 `[]` 就是**完全自动发现**——插上网卡即可用，不用改配置。

配置里写死的 IP 会在启动时用网卡**当前**地址校正
（校园网是 DHCP，重连后 IP 会变，实测 `10.6.53.173` → `10.6.69.131` → `10.6.95.188` → `10.6.44.6`）。
地址过期不会卡死：启动时按网卡实际地址覆盖，界面上点「重新扫描」也能就地校正，
并且**会把新插上的网卡补进来**（见 4.3）。

### 4.2 IPv6

**自动发现**，只接受全球单播（`2000::/3`）：

- 排除链路本地 `fe80::`
- 排除唯一本地 `fc00::/7`
- 排除回环 `::1`

想覆盖自动探测（例如网卡有多个 v6 前缀），可以显式写：

```json
"sources": [
  { "interface": "WLAN", "source_ip": "10.6.18.3",
    "source_ip6": "2001:250:209:2000:552:4ae1:dc41:61ac" }
]
```

留空即自动发现。网卡没有全局 v6 也能正常跑，只是连不上 v6 对等体。

### 4.3 「重新扫描」的语义

点界面上的「重新扫描」（`POST /api/v1/sources/rescan`，需要 `X-Caulink-Action: 1` 头）会：

1. 用网卡当前地址重算源列表（配置里的校正 + 没写过的自动发现）
2. 池里失效的源移除、新出现的源补上
3. 认证层同步启停，让新 IP 立刻开始认证

启动路径和「重新扫描」调用的是同一个 `resolveSources`，
所以"启动时能发现"和"扫描时能发现"是同一套规则。

---

## 5. 代码位置

| 文件 | 职责 |
|---|---|
| `internal/netsrc/netsrc.go` | **选族规则唯一实现**：`Resolve(ctx, hostPort, entry) → (network, addr, source)` |
| `internal/proxy/dialer/dialer.go` | 按上面的结果拨 `tcp4` / `tcp6`，绑定同族源地址 |
| `internal/scheduler/scheduler.go` | 目标是 v6 字面量时，只挑有 v6 源的条目 |
| `internal/health/health.go` | 探测也按族选源与目标，避免把正常链路误判成 0 分 |
| `cmd/caulinkd/netif.go` | `resolveSources`（校正配置）+ `discoverSources`（自动发现）+ 从网卡发现 v4/v6 |
| `cmd/caulinkd/sourcectl.go` | 「重新扫描」：增删源、同步认证层 |
| `pkg/contract/v1/ippool_entry.go` | `IPPoolEntry.SourceIP` / `SourceIP6` |

选族逻辑的关键注释（原文）：

```go
//   - 目标是 IPv6 字面量 → 必须用 v6 源；没有就报错，绝不退回默认路由
//   - 目标是 IPv4 字面量 → 必须用 v4 源
//   - 目标是域名 → 解析后优先选与条目有源地址的族；两边都有时优先 v4
```

---

## 6. 怎么验证

### 6.1 看池里有哪些源

```powershell
Invoke-RestMethod http://127.0.0.1:7788/api/v1/status |
  Select-Object -ExpandProperty ips |
  Format-Table source_ip, source_ip6, auth_status, schedulable
```

期望每块在用网卡出现一行，v4 与 v6 都不为空。

### 6.2 验证「域名 → IPv4」

```powershell
# 一边下载，一边看 caulinkd 的连接
curl.exe -s -o NUL --max-time 10 --socks5-hostname 127.0.0.1:1080 `
     "https://mirrors.tuna.tsinghua.edu.cn/ubuntu-releases/22.04/ubuntu-22.04.5-desktop-amd64.iso"

Get-NetTCPConnection -OwningProcess (Get-Process caulinkd).Id -State Established |
  Where-Object { $_.RemoteAddress -notmatch '^(127\.|::1)' } |
  Select-Object LocalAddress, RemoteAddress, RemotePort
```

实测输出（本机，两条线各出现一次）：

```
LocalAddress  RemoteAddress   RemotePort
10.6.44.6     101.6.15.130    443          ← 走 IPv4
10.6.54.214   2.16.153.57     80           ← 第二条线也在用（CDN 换过一次）
```

### 6.3 验证「IPv6 字面量 → IPv6」

```powershell
curl.exe -s -o NUL --max-time 10 --socks5-hostname 127.0.0.1:1080 `
     -H "Host: mirrors.tuna.tsinghua.edu.cn" `
     "http://[2402:f000:1:400::2]/ubuntu/ls-lR.gz"
```

实测输出（本机，40 MB 用时 7.2 秒）：

```
LocalAddress                            RemoteAddress        RemotePort
2001:250:209:2000:a577:91a:b825:618f    2402:f000:1:400::2   80        ← 走 IPv6
```

> **PowerShell 引号陷阱**：别用
> `Start-Process -ArgumentList @('-H','Host: mirrors.tuna.tsinghua.edu.cn', ...)`。
> PowerShell 会把带空格的参数拆成两个传给 curl，发出去的请求头畸形，
> 运维看到的是 `400 Bad Request`，很容易误判成代理坏了。
> 直接写 `curl.exe ... -H "Host: mirrors.tuna.tsinghua.edu.cn"` 即可。

### 6.4 最省事的判据

```powershell
curl.exe -sv --max-time 15 --socks5-hostname 127.0.0.1:1080 "http://[2402:f000:1:400::2]/"
# 期望看到：Opened SOCKS connection ...（等价于 SOCKS5 request granted）
```

看到这一行就说明 v6 通路是通的。剩下的 0 速度问题只可能来自**种子本身没有对等体**。

### 6.5 验证多 IP 是否真在聚合

单条连接永远只走一条线（调度是按连接的），要看聚合必须多连接：

```powershell
$url='https://mirrors.aliyun.com/ubuntu/ls-lR.gz'; $chunk=4194304; $n=8
$procs=@(); for($i=0;$i -lt $n;$i++){ $s=$i*$chunk; $e=$s+$chunk-1
  $procs += Start-Process curl.exe -ArgumentList @('-4','-s','--ssl-no-revoke','-r',"$s-$e",'-o','NUL',$url) -PassThru -NoNewWindow }
$sw=[Diagnostics.Stopwatch]::StartNew(); $procs | ForEach-Object { $_.WaitForExit() }; $sw.Stop()
"{0} MB/s" -f [math]::Round(($chunk*$n)/1MB/$sw.Elapsed.TotalSeconds,2)
```

把 `-x http://127.0.0.1:8080` 加进去再跑一次，对比两次结果。

---

## 7. 常见误解

**"有 IPv6 就该优先用 IPv6，不是更快吗？"**
不必然。速度取决于对端与链路，而不是协议版本。v6 默认不优的原因是稳定性与覆盖面，
不是性能。需要时（对端只有 v6）自动会走 v6。

**"v6 源和 v4 源可以混着用吗？"**
不行。一条连接只能有一个源地址，且必须与目标同族。
调度器会在"目标已知是 v6 字面量"时先过滤掉没有 v6 源的条目。

**"DHT / uTP 也走代理吗？"**
不。它们是 UDP，SOCKS5 的 UDP ASSOCIATE 只在客户端显式使用时才生效，
BT 客户端通常直接走本机网络。所以 DHT 只负责"找节点"，
真实下载的 TCP 连接仍然走代理与多 IP 调度。

**"让 QQ 走代理就能加速吗？"**
不一定成立，要同时满足三个条件：QQ 真的配置了 SOCKS5 代理（默认是直连）、
目标走 v4 域名（走 v6 字面量就只用一条 v6 线）、下载器开多条连接。
实测 QQ 的出站连接全是 IPv6 且完全不走代理——那种情况下 caulinkd 一个字节都收不到。

---

## 8. 本机现状（2026-11-22）

```
WLAN    v4: 10.6.44.6
        v6: 2001:250:209:2000:a577:91a:b825:618f
WLAN 2  v4: 10.6.54.214                     ← USB Wi-Fi（AIC8800D80）
        v6: 2001:250:209:2000:ccc2:63f6:a9ae:d64e
```

- 池里 **2 个源**，两条都 `auth_status=online`、`schedulable=true`，`candidates=2`
- `WLAN 2` 未写进配置文件，是启动时**自动发现**进来的（`discoverSources`）
- 双 IP 聚合实测（8 线程 / 32 MB / IPv4）：

  | 场景 | 速度 |
  |---|---|
  | 直连（单 IP） | 2.96 / 2.73 MB/s |
  | 经 `127.0.0.1:8080`（两 IP） | **6.22 / 8.18 MB/s** |

  单线程下载速度不变——聚合只在多连接时体现。

IP 会随 DHCP 变化，程序启动时会自动跟随，界面「重新扫描」也能就地校正并补上新网卡。
