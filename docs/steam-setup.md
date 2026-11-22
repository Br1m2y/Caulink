# Steam 下载走多 IP 调度

> 适用版本：`caulinkd` 带 `http_proxy` 配置（`B14` 之后）。
> 相关设计：`docs/adr/0002-ui-framework-wails.md`、`docs/status-api.md`。

## 为什么需要单独配

Steam 客户端的代理设置**只支持 HTTP 代理**，不支持 SOCKS5。所以它不能像迅雷那样
直接填 `127.0.0.1:1080`，必须由 `caulinkd` 另开一个 HTTP 代理口。

加上这个口之后，Steam 的每条下载连接都会经过调度器，被分配到不同的源 IP：

```
Steam → HTTP 代理(127.0.0.1:8080) → scheduler.Pick → dialer.Dial(绑定源 IP) → Steam CDN
```

## 前提条件（缺一不可）

| 前提 | 怎么确认 |
|---|---|
| `caulinkd` 已运行，且代理口正常 | `GET http://127.0.0.1:7788/api/v1/status` 能返回 JSON |
| **至少两个**已认证的源 IP | 响应里 `ips[].schedulable` 至少两条为 `true` |
| Phase 0 已判定"限速锚定 IP" | 若锚定账号，多 IP 不会提速，本文步骤无意义 |
| 两个 IP 不共用空口（建议，非必须） | 见下文「关于空口」 |

### 关于空口

两块网卡连在同一个 AP、同一信道时，理论上共享空口时间。但 **实测表明：
即使同 AP 同信道，多 IP 仍然完整叠加（12.17 MB/s = 2 × 6.10）**——因为该 AP 的空口
容量远大于两条链路的需求。

所以空口不是必须排除的前提，只在**并发吞吐明显低于各 IP 之和**时才值得怀疑：

- 这时优先改用有线（物理上不共享空口）；
- 其次检查目标 CDN 是否限流、是否处在晚高峰。

## 步骤

### 1. 启用 HTTP 代理

编辑 `configs/caulinkd.json`：

```json
"http_proxy": {
  "listen": "127.0.0.1:8080"
}
```

留空或删掉该段表示不启动。**只允许回环地址**，填 `0.0.0.0` 会被拒绝启动。

### 2. 启动 caulinkd

```powershell
caulinkd.exe -config configs/caulinkd.json -v
```

启动日志里应出现代理监听信息；同时确认状态接口已就绪：

```powershell
curl.exe -s http://127.0.0.1:7788/api/v1/status | Select-String 'http_proxy'
```

### 3. 验证代理本身可用

先用 curl 走一遍，确认代理链路通，再去动 Steam：

```powershell
curl.exe -x http://127.0.0.1:8080 -s -o NUL -w "status=%{http_code}`n" https://www.baidu.com
```

返回 `200` 才算代理正常。常见的非 200：

| 状态码 | 含义 | 处理 |
|---|---|---|
| `502` | 从选中的源 IP 建连失败 | 检查该 IP 是否真的能上网 |
| `503` | 没有可用的源 IP | 认证掉了，查 `/api/v1/status` 的 `auth_status` |
| `400` | 请求不是 CONNECT 也不是绝对 URI | 客户端配置错了 |

### 4. 在 Steam 里配置代理

```
Steam → 设置（Settings）
  → 下载（Downloads）
  → 代理服务器（Proxy Server）
      ☑ 使用代理服务器
      服务器地址：127.0.0.1
      端口：8080
  → 确定 → 重启 Steam
```

> Steam 的代理设置是**全局且持久**的：只要配了，所有 Steam 流量都走它。
> 所以 `caulinkd` 停止后 Steam 会连不上网，见「用完记得取消」。

### 5. 验证流量确实分散到了多个 IP

开一个 Steam 下载，同时反复读状态接口：

```powershell
while ($true) {
  curl.exe -s http://127.0.0.1:7788/api/v1/status |
    ConvertFrom-Json |
    ForEach-Object { $_.ips | Select-Object source_ip, auth_status, active_conns, rate_down_bps }
  Start-Sleep -Seconds 2
}
```

判读标准：

| 现象 | 说明 |
|---|---|
| 多个 `source_ip` 的 `active_conns` 都 > 0 | ✅ 连接确实分散了 |
| 只有一个 IP 有连接 | ⚠️ 另一个 IP 不可调度（认证掉/健康分为 0） |
| 多个 IP 都有连接，但总速率仍接近单 IP | 见「为什么可能不加速」 |

## 为什么可能不加速

| 原因 | 判断方法 |
|---|---|
| 限速锚定**账号**（Phase 0 结论） | 单 IP 与多 IP 总吞吐几乎一样 |
| 目标 CDN 对单客户端限流 | 换个下载目标试试 |
| 两块网卡共用空口且 AP 已饱和 | 改用有线；同 AP 未必是问题，见上文「关于空口」 |
| Steam 只用了一条连接 | Steam 分片下载通常是多连接，但小文件可能只有 1–2 条 |
| 单连接场景 | 单个 TCP 连接的源 IP 固定，永远无法叠加 |

## 用完记得取消

**`caulinkd` 停止后 Steam 会无法联网**，因为它仍指向一个不存在的代理。两种收尾方式：

1. Steam → 设置 → 下载 → 代理服务器 → **取消勾选**；
2. 或者保持 `caulinkd` 常驻运行。

> 如果忘了取消又希望临时上网：把代理端口改成 8080 以外的关闭端口，Steam 会快速失败；
> 更稳妥的做法还是按第 1 条取消勾选。

## 已知边界

- **只加速下载**：HTTP 代理只处理 TCP，游戏联机流量是 UDP，不经过它；
- **不支持 keep-alive 复用**：代理对明文 HTTP 采用一次请求一条连接，
  HTTPS（CONNECT）不受影响；
- **不认代理的程序**（如部分客户端自带的更新器）需要 TUN 接管，目前未实现（看板 B10）。

## 附：迅雷的配置（对照）

迅雷支持 SOCKS5，不需要 HTTP 代理：

```
迅雷 → 设置 → 下载/网络 → 代理
  类型：SOCKS5
  地址：127.0.0.1
  端口：1080
```

| 客户端 | 端口 | 协议 |
|---|---|---|
| 迅雷 / 浏览器 | `127.0.0.1:1080` | SOCKS5 |
| Steam | `127.0.0.1:8080` | HTTP |

## 进阶：社区能开 + 下载仍走多 IP（上游分流）

Steam 只支持填**一个**代理，但用户往往既要"社区/商店能访问"（需 VPN），
又要"下载走多 IP 聚合"（我们的代理）。这两者可以共存：让**我们的代理**做分流。

```json
"http_proxy": {
  "listen": "127.0.0.1:8080",
  "upstream": {
    "proxy": "http://127.0.0.1:17891",
    "domains": ["steamcommunity.com", "*.steamcommunity.com",
                "*.steampowered.com", "steamstatic.com"]
  }
}
```

行为：

| 目标域名 | 走向 |
|---|---|
| 命中 `domains` | 交给上游代理（本地 VPN 客户端） |
| 其余（CDN 下载） | 继续走校园网多源 IP 并行 |

规则写法：

- `example.com` 精确匹配
- `*.example.com` 只匹配子域
- `.example.com` 匹配自身与所有子域
- 留空表示全部走上游

**上游不可达时自动回退直连**（只记一条 WARN 日志），不会让请求卡死。

> 注意：走上游的流量不计入"每 IP 速率"统计——它没走校园网链路，
> 计进去会让面板数字失真。
