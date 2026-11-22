# 故障排查手册

> 这份文档只记**踩过的坑**：每个条目都对应一次真实的排查过程，包括当时误判的方向。
> 碰到相似症状时，按「诊断」的顺序做，不要凭直觉猜原因。

---

## 1. 程序能启动、界面正常，但"改系统设置"失败（`Access is denied`）

### 症状

- `caulink-desktop` 打开正常，能显示状态、能下载
- 但设置页的「重启 Steam 以支持多 IP」开关报
  `写入系统代理失败（Access is denied.）`
- `caulinkd` 里任何写 `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings` 的操作都失败
- 而同样这条命令，在**同一个账户**的 PowerShell 里却能成功

### 真因

**可执行文件被打上了 Low 完整性标签**，导致从它启动的进程以 **Low 完整性级别**运行，
而该注册表键需要 Medium。

```powershell
icacls .\desktop\bin\caulink-desktop.exe | Select-String 'Mandatory Label'
# 出错时：
#   Mandatory Label\Low Mandatory Level:(I)(NW)
# 正常时：
#   Mandatory Label\Medium Mandatory Level:(NW)
```

这类标签通常来自**创建该文件的受限环境/沙箱**（重新编译后又被继承），
与谁双击启动无关——**标签在文件上，不在进程上**。

### 诊断顺序（别跳步）

| 步 | 命令 | 看什么 |
|---|---|---|
| 1 | `icacls <exe>` | `Mandatory Label` 行是 Low 还是 Medium |
| 2 | 启动程序后测进程令牌完整性 | 应为 `S-1-16-8192`（Medium） |
| 3 | 同账户的 PowerShell 写同一键 | 若成功 → 说明是**二进制**的问题，不是账户/策略 |

进程完整性（PowerShell 版）：

```powershell
# 需要管理员或以同账户运行
# 结果里 4096=Low, 8192=Medium, 12288=High
```

### 修复

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/fix-integrity.ps1
```

它会把 `bin/`、`desktop/bin/`（目录 + 其中 exe）统一设为 Medium。
`scripts/build-desktop.ps1` 已在构建末尾自动调用这一步。

### 排查中走过的弯路（照做会浪费时间）

| 误判 | 为什么错 |
|---|---|
| "杀毒软件拦截" | McAfee 只是残留注册项，进程/服务都不存在 |
| "Smart App Control / ASR 规则" | SAC 关闭（`VerifiedAndReputablePolicyState=0`），ASR 未配置 |
| "AppContainer 沙箱" | 令牌 `TokenIsAppContainer=0` |
| "限制跟着发起进程走" | 同账户同机器，Shell 能写、程序不能写，此结论自相矛盾 |
| "用 explorer 交接绕过" | explorer 打开 .bat 在该进程下无窗口、脚本未执行 |
| "UAC 提权绕过" | 提权后再让 explorer 启动 Steam 报 `0xc0000142` |

**教训：`Access is denied` 第一步就该比令牌/标签，而不是猜策略。**

---

## 2. `.bat` / `.ps1` 双击后一闪而过

### 原因一：`.bat` 里有中文

`cmd.exe` 按 OEM 代码页（GBK）解码 `.bat`。文件是 UTF-8 时，中文注释会变成乱码，
**cmd 会把它们当命令执行**，脚本立刻退出。

```
'。' is not recognized as an internal or external command
```

**规则：`.bat` 只用纯 ASCII**（注释也写英文）。

### 原因二：`.ps1` 存成了无 BOM 的 UTF-8

Windows PowerShell 5.1 读取无 BOM 的 `.ps1` 时会按 ANSI 解码，中文注释变乱码，
直接语法错误：

```
The string is missing the terminator: ".
```

**规则：含中文的 `.ps1` 必须存为 UTF-8 with BOM。**

```powershell
$t = [IO.File]::ReadAllText($p, [Text.Encoding]::UTF8)
[IO.File]::WriteAllText($p, $t, (New-Object Text.UTF8Encoding($true)))  # $true = 带 BOM
```

### 验证方法

```powershell
# 语法检查（不执行）
powershell -NoProfile -Command "[scriptblock]::Create((Get-Content -Raw 'x.ps1')) | Out-Null; 'ok'"

# 捕获真实输出，而不是看着窗口闪
cmd /c "x.bat > out.log 2>&1"
```

---

## 3. Steam 下载没有走多 IP

### 检查顺序

| # | 检查 | 命令/位置 | 期望 |
|---|---|---|---|
| 1 | 守护进程在跑 | `Get-Process caulinkd`；`curl http://127.0.0.1:7788/api/v1/status` | 200，且 `ips[].schedulable` 有 2 条 |
| 2 | 系统代理指向我们 | 设置 → 网络和 Internet → 代理 | `127.0.0.1:8080` |
| 3 | Steam 的连接落点 | `Get-NetTCPConnection -OwningProcess (Get-Process steam).Id` | 集中在 **8080**，不是 VPN 的 17891 |
| 4 | 面板速率 | `desktop/bin/caulink-desktop.exe` | 两个 IP 各约 5.9 MB/s |

### 三个常见原因

**① Steam 只认"启动时"读到的代理**

若 Steam 已在运行，改系统代理没用。必须**先结束 Steam 再启动**。
应用里的开关已经做了这件事；手动操作时注意别只改代理。

**② 本地 VPN 客户端每 8 秒改写系统代理**

实测：0dcloud 连接后每 8 秒把 `ProxyServer` 改回自己。
所以启动 Steam 后需要**持续按住**一段时间（本项目的默认是 90 秒），
等 Steam 把代理读进内存。

**③ 只开了一条连接**

多 IP 聚合作用于**连接级**：单条 TCP 连接永远跑不满。需要多线程/多分片下载
（Steam、迅雷、IDM 都是多连接）。

---

## 4. 面板显示「演示数据」而不是「实时」

`api.js` 连续两次拉不到 `http://127.0.0.1:7788/api/v1/status` 就会退回内置演示数据。

- 检查 `caulinkd` 是否在运行
- 检查 7788 端口是否被占用或不可达
- 演示数据里的 IP（如 `10.6.50.103`）是假的，别当成真实链路

---

## 5. `explorer.exe` 报 `0xc0000142`

在**提权上下文**里用 `explorer.exe` 去启动另一个程序，会因会话/桌面初始化失败
而报此错误。

- 不要在 elevated 脚本里用 explorer 做进程交接
- 需要以普通权限启动程序时，用 `Start-Process`，或让用户在正常桌面双击

---

## 6. 完整性级别速查

| SID | 含义 | 能写 Medium 资源吗 |
|---|---|---|
| `S-1-16-4096` | Low | ❌（本手册 §1 的根源） |
| `S-1-16-8192` | Medium | ✅（普通用户程序） |
| `S-1-16-12288` | High | ✅（管理员提权） |
| `S-1-16-16384` | System | ✅ |

读取方式：`icacls <文件>` 看 `Mandatory Label` 行；
进程级需要在代码里调 `GetTokenInformation(TokenIntegrityLevel)`。

---

## 7. 相关脚本一览

| 脚本 | 用途 | 还需要吗 |
|---|---|---|
| `scripts/fix-integrity.ps1` | 修正 exe 完整性标签 | **需要**，构建后必跑 |
| `scripts/build-desktop.ps1` | 构建桌面端（含完整性修正） | 需要 |
| `scripts/start-caulink.bat` | 双击启动守护进程 | 方便，可选 |
| `scripts/start-steam-with-caulink.bat` + `.ps1` | 极少数环境下应用写代理失败时的回退 | 可选（应用会提示） |
| `scripts/close-steam.bat` | 回退用：关闭 Steam | 可选 |

---

## 8. 构建报 `open bin/caulink-desktop.exe: Access is denied`

三个独立原因，逐个排除：

### ① 应用正在运行

exe 被占用，`go build` 覆盖时会失败。

```powershell
Get-Process caulink-desktop | Stop-Process -Force
```

> 若仍失败但**能改名**（`Move-Item` 成功）：说明文件被映射而非锁定。
> 把旧文件改名让出目标名，再构建即可。

### ② 目录 ACL 里有拒绝删除的 ACE

```powershell
icacls desktop\bin | Select-String 'DENY'
# Everyone:(I)(CI)(DENY)(DC)      ← DC = 拒绝删除子项
```

带 `(I)` 表示从上级继承。在目标目录上断开继承再移除：

```powershell
icacls desktop\bin /inheritance:d
icacls desktop\bin /remove:d "Everyone"
# 对 bin 目录同样处理
```

### ③ `wails3 build` 的任务运行器在受限环境里失败

症状：`wails3 build` 报 `Access is denied`，但**手工执行同一条 `go build` 却成功**。

```powershell
cd desktop
go build -tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui" -o bin/caulink-desktop.exe .
```

`scripts/build-desktop.ps1` 已经改成**直接调用 go build**（图标 / syso / 编译三步显式执行），
不再依赖 wails3 的任务运行器。

---

## 9. 主页的 Steam 卡片没显示

卡片结构是**写死在 `index.html` 里**的，JS 只负责更新状态。

如果卡片空白，先确认页面加载的是最新资源：

```javascript
// 在 DevTools / CDP 里
fetch('/app.js').then(r => r.text()).then(t => t.includes('steam-mode-switch'))
```

历史教训：早期版本用 JS 动态生成卡片内容（`renderSteamCard()`），
在 WebView2 里出现过"元素存在但内容为空"的情况；改静态 HTML 后稳定。

---

## 10. BT 客户端（qBittorrent）连不上任何对等体

### 症状

- 种子状态显示"等待"，`做种数 0`、`用户 0`、`DHT 0 节点`、速度恒为 0
- 已经配好 SOCKS5，`curl --socks5-hostname` 测同一目标却是通的
- qBittorrent 日志里成片的：

```
SOCKS5 代理错误。地址：[2402:f000:1:400::2]:41653。消息："host unreachable"
SOCKS5 代理错误。地址：127.0.0.1:1080。消息："在其上下文中，该请求的地址无效"
```

### 原因一：源地址与目标地址族不一致（最常见）

一个 TCP socket 的**源地址必须与目标同族**。校园网的 BT 对等体大量是 IPv6，
而代理只绑了 IPv4 源 → 内核直接返回 `WSAEADDRNOTAVAIL`
（中文即"在其上下文中，该请求的地址无效"）。

修法见 `internal/netsrc`：每块网卡同时记录 `SourceIP` 与 `SourceIP6`，
拨号时按目标族选源；族不匹配时**报错而不是回退默认路由**。

验证方式（用 IPv6 字面量强制走 v6 源）：

```powershell
curl -sv --max-time 15 --socks5-hostname 127.0.0.1:1080 "http://[2402:f000:1:400::2]/"
# 期望看到：SOCKS5 request granted.
```

### 原因二：qBittorrent 5.x 的配置文件换了位置和格式

**这一条坑得最惨**——改了半天的配置，程序根本不读。

| 版本 | 真正生效的文件 |
|---|---|
| qBittorrent 4.x | `%APPDATA%\qBittorrent\qBittorrent.conf` |
| **qBittorrent 5.x** | **`%APPDATA%\qBittorrent\qBittorrent.ini`** |

而且**键名分区也变了**：

```ini
# 5.x 正确写法
[Network]
Proxy\Type=SOCKS5
Proxy\IP=127.0.0.1
Proxy\Port=1080
Proxy\HostnameLookupEnabled=true
Proxy\PeerConnections=true
Proxy\Profiles\BitTorrent=true
Proxy\Profiles\Misc=true
Proxy\Profiles\RSS=true

[WebUI]
Enabled=true
Port=8082
Address=127.0.0.1

# 4.x 的 [Preferences] Proxy\... 在 5.x 里会被完全忽略
```

**判断配置是否被读取**：改完重启，看文件有没有被程序改写（QSettings 会
重排键顺序）。如果文件纹丝不动，说明你改的不是它在读的那个文件。

### 原因三：种子没有 tracker

有些种子只靠 DHT 找节点。判断方法：

```powershell
$t = [System.Text.Encoding]::UTF8.GetString([IO.File]::ReadAllBytes('<x>.torrent'))
$t.Contains('announce')   # 为 False 就是 trackerless
```

这种种子必须**开启 DHT**（选项 → BitTorrent → DHT）。DHT 走 UDP，
无法通过 SOCKS5，但它只负责"找节点"，实际下载的 TCP 连接仍走代理。

### 原因四：uTP 走不了 SOCKS5

uTP 是 UDP 协议，代理承载不了。必须关掉：

```ini
[BitTorrent]
uTP=false
```

### 快速自检清单

| 检查 | 命令/位置 | 期望 |
|---|---|---|
| 代理是否能连 v6 目标 | `curl --socks5-hostname ... http://[v6]/` | `request granted` |
| qBittorrent 是否在用代理 | `Get-NetTCPConnection -RemotePort 1080` | 有 qbittorrent 的连接 |
| 配置是否被读取 | 重启后看 ini 的修改时间 | 被改写说明读到了 |
| 种子有无 tracker | 上面那行 PowerShell | `True` |
| DHT / uTP | 配置或界面 | DHT 开、uTP 关 |

---

## 11. 「按住系统代理」停止后整机断网

### 症状

停止按住后浏览器所有页面变成：

```
ERR_PROXY_CONNECTION_FAILED
```

### 原因：只还原了代理地址，没还原开关

系统代理由**两个**值共同决定：

```
ProxyServer = 127.0.0.1:17891   ← 地址
ProxyEnable = 1                 ← 开关
```

如果用户原本是"不使用代理"（`ProxyEnable=0`），而我们只把地址写回去、
开关留成 1，系统就会把全部流量丢给那个地址。那个地址上的 VPN 没开时，
就是整机断网。

**修法**：做完整快照，停止时按原样写回四样东西：

```
ProxyServer / ProxyEnable / ProxyOverride / AutoConfigURL
```

并且**先写地址与例外，最后写开关**，避免出现"开关已开、地址还是旧的"的瞬时状态。

### 同类教训

这个坑踩过两次，都是"只还原一部分"：

1. 第一次：只还原地址 → 代理一直指向 8080
2. 第二次：还原地址+开关 → 例外列表被外部改写后无法恢复

**结论：涉及系统配置的"还原"，必须整份快照，不能挑字段。**

---

## 12. 在 Windows 上验证 BT 能否走代理的最小实验

不需要打开任何 BT 客户端，三步就能判断链路是否可用：

```powershell
# 1) 代理是否在监听
Test-NetConnection 127.0.0.1 -Port 1080

# 2) IPv4 目标（域名，会自动选族）
curl -s -o NUL -w '%{http_code}' --max-time 15 `
     --socks5-hostname 127.0.0.1:1080 https://mirrors.tuna.tsinghua.edu.cn/

# 3) IPv6 目标（字面量，强制走 v6 源）
curl -s -o NUL -w '%{http_code}' --max-time 15 `
     --socks5-hostname 127.0.0.1:1080 -H 'Host: mirrors.tuna.tsinghua.edu.cn' `
     "http://[2402:f000:1:400::2]/"
```

三项都返回非 0 状态码（403/301 也算成功，那是服务端的 Host 校验），
说明代理由内到外是通的。剩下的 0 速度问题就只可能来自**种子本身没有对等体**。