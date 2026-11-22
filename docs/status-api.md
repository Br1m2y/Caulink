# 状态接口契约（B → C）

> 面向：做 Wails 桌面控制面板的 C。
> 实现：`internal/control`（服务端）、`internal/metrics`（流量统计）。
> 本文件是契约，改字段前先读 §7。

---

## 1. 端点

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/v1/status` | 完整状态，UI 每次轮询用这个 |
| GET | `/healthz` | 纯文本 `ok`，只用于探活 |

默认监听 `127.0.0.1:7788`，配置项 `status.listen`。

**只监听回环地址**，写死校验，配成 `0.0.0.0` 会直接启动失败。

**没有任何写接口。** 改配置请改文件后重启。原因：一个能改状态的本地接口等于给任何本机进程提供了绕开调度的入口。

---

## 2. 完整响应示例

真实抓取（单 IP，下载进行中）：

```json
{
  "version": "0.1.0-dev",
  "started_at": "1970-01-01T00:00:00.0000000Z",
  "uptime_seconds": 6.6753159,
  "socks5": "127.0.0.1:11080",
  "ips": [
    {
      "id": "WLAN@10.6.92.171",
      "interface": "WLAN",
      "source_ip": "10.6.92.171",
      "auth_status": "online",
      "health_score": 0.4525936444537478,
      "rtt_ms": 241.8975,
      "loss_rate": 0,
      "last_seen": "1970-01-01T00:00:00.0000000Z",
      "schedulable": true,
      "weight": 45,
      "active_conns": 1,
      "total_conns": 1,
      "bytes_up": 752,
      "bytes_down": 1906479,
      "rate_up_bps": 0,
      "rate_down_bps": 440415,
      "errors": 0
    }
  ],
  "scheduler": {
    "strategy": "smooth-weighted-round-robin",
    "candidates": 1
  },
  "totals": {
    "ip_count": 1,
    "schedulable_count": 1,
    "active_conns": 1,
    "total_conns": 1,
    "bytes_up": 752,
    "bytes_down": 1906479,
    "rate_up_bps": 0,
    "rate_down_bps": 440415,
    "errors": 0
  }
}
```

---

## 3. 字段说明

### 3.1 顶层

| 字段 | 类型 | 说明 |
|---|---|---|
| `version` | string | 构建版本，UI 上要显示，便于确认跑的是哪一版 |
| `started_at` | string | 状态服务启动时刻，**UTC**，RFC3339 |
| `uptime_seconds` | double | 运行时长，秒 |
| `socks5` | string | 代理监听地址，UI 上要显示，用户得知道配什么端口 |
| `http_proxy` | string | 可选 HTTP 代理监听地址；未启用时字段不出现。Steam 只能配 HTTP 代理 |
| `ips` | array | 源 IP 列表，**按 `id` 字典序稳定排序**，UI 可以直接按序渲染不跳动 |
| `scheduler` | object | 调度策略状态 |
| `totals` | object | 全局汇总，顶部卡片直接用 |

### 3.2 `ips[]`

前 8 个字段来自 `contract.IPPoolEntry`（冻结契约，见仓库 `pkg/contract/v1`），后 7 个是本接口的附加信息。

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | string | 稳定标识，形如 `<网卡>@<源IP>`。UI 上的列表 key 用它 |
| `interface` | string | 网卡名 |
| `source_ip` | string | 源 IP，纯地址，无端口无掩码 |
| `auth_status` | string | `online` / `offline` / `reauthenticating` 三态之一 |
| `health_score` | double | 健康分，[0,1]。1.0 最好，0 表示不可调度 |
| `rtt_ms` | double | 平滑后的往返时延，毫秒。0 表示未知 |
| `loss_rate` | double | 丢包率，[0,1]。0 表示未知 |
| `last_seen` | string | 最后一次确认存活，UTC RFC3339 |
| `schedulable` | bool | 当前能否接收新连接（认证在线且健康分 > 0） |
| `weight` | int | 调度权重，0–100。**UI 画流量分配饼图用它**，别自己按健康分重算 |
| `active_conns` | int64 | 当前活跃连接数；UDP 按会话计 |
| `total_conns` | uint64 | 进程启动以来累计承载的连接数 |
| `bytes_up` / `bytes_down` | uint64 | 累计字节。UDP 只算载荷，不含 SOCKS5 头 |
| `rate_up_bps` / `rate_down_bps` | double | 平滑后瞬时速率，**字节/秒** |
| `errors` | uint64 | 该 IP 上建连失败与转发异常次数 |

### 3.3 `scheduler`

| 字段 | 类型 | 说明 |
|---|---|---|
| `strategy` | string | 目前固定 `smooth-weighted-round-robin` |
| `sticky_id` | string? | 敏感流量（游戏/会议/语音）当前绑定的 IP 条目 ID，空表示还没走过敏感流量 |
| `sticky_ip` | string? | 上面那个 ID 对应的 IP，省得再查一遍 |
| `candidates` | int | 当前可调度 IP 数量 |

**`candidates == 0` 时必须报警**，不是显示"正常运行"。这时所有新连接都会失败。

### 3.4 `totals`

字段同 `ips[]` 的统计项，口径是全部 IP 之和，另加 `ip_count` 与 `schedulable_count`。

---

## 4. 单位约定（最容易踩的地方）

1. **速率是字节/秒，不是比特/秒，也不是 MB/s。** 显示成 MB/s 请自己 `/ 1e6`。
   服务端不做单位换算，因为客户端要决定用 KB/MB 还是自适应格式。
2. **`health_score` 与 `loss_rate` 是 [0,1] 的比例，不是百分比。** 显示时乘 100。
3. **时间都是 UTC。** 前端 `new Date(str)` 解析后，浏览器/WebView 会按本地时区显示。
4. **`weight` 是整数 0–100**，与 `health_score * 100` 的取整结果一致，下限 1。

---

## 5. 轮询建议

**1 秒一次，不要更快。**

速率由服务端每秒采样一次并用 EWMA 平滑，轮询快于 1 秒只会拿到重复值，白耗 CPU 和电池。

超时建议 3 秒。连续 3 次失败就显示"控制服务未连接"，不要一直转圈。

```js
async function fetchStatus(signal) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), 3000);
  try {
    const resp = await fetch('http://127.0.0.1:7788/api/v1/status', { signal: ctrl.signal });
    if (!resp.ok) throw new Error(`status ${resp.status}`);
    return await resp.json();
  } finally {
    clearTimeout(timer);
  }
}
```

---

## 6. 前端模型（Wails v3）

桌面端是 Wails v3（Go + 系统 WebView2），前端直接用 `fetch`，不需要额外的客户端库。

```ts
interface IpStatus {
  id: string;                 // "<网卡>@<源IP>"，列表 key 用它
  interface: string;          // 网卡名
  source_ip: string;
  auth_status: 'online' | 'offline' | 'reauthenticating';
  health_score: number;       // [0,1]
  rtt_ms: number;
  loss_rate: number;          // [0,1]
  last_seen: string;          // UTC RFC3339
  schedulable: boolean;
  weight: number;             // 0–100，画流量分配饼图用它
  active_conns: number;
  total_conns: number;
  bytes_up: number;           // uint64
  bytes_down: number;         // uint64
  rate_up_bps: number;        // 字节/秒
  rate_down_bps: number;
  errors: number;
}

interface StatusResponse {
  version: string;
  started_at: string;
  uptime_seconds: number;
  socks5: string;
  ips: IpStatus[];
  scheduler: {
    strategy: string;
    sticky_id?: string;
    sticky_ip?: string;
    candidates: number;
  };
  totals: {
    ip_count: number;
    schedulable_count: number;
    active_conns: number;
    total_conns: number;
    bytes_up: number;
    bytes_down: number;
    rate_up_bps: number;
    rate_down_bps: number;
    errors: number;
  };
}
```

三个注意点：

1. **CORS 已经由服务端打开**（`Access-Control-Allow-Origin: *`）。Wails 页面来自自定义 scheme，与 `127.0.0.1:7788` 不同源，没有这个头 `fetch` 会被浏览器直接拦掉——而且服务端日志里什么都没有，现象是"curl 能通，界面上全是空"。

2. **`interface` 是 JS 保留字**，不能写 `const { interface } = obj`。要么用 `obj.interface`，要么解构时重命名：`const { interface: iface } = obj`。

3. **`bytes_*` 是 uint64**，JS 的 `number` 超过 2^53 会丢精度。本项目要跑到 9 PB 才触边界，实际不可能，但知道这个上限在哪，比出事后再查强。

---

## 7. 兼容性约定

**可以改（向后兼容）：**
- 新增字段。客户端忽略未知字段，旧的 UI 照常工作。
- 新增 `auth_status` 的取值（如果将来加 `pending`）。**但这属于契约变更**，
  必须同步改 `pkg/contract/v1` 并在 `docs/adr/` 记一条，不能只改这里。

**不能改（破坏兼容）：**
- 重命名或删除任何已发布字段。
- 改变字段类型（比如 `number` ↔ `string`，前端解析会直接拿到 NaN）。
- 改变单位（比如把 `rate_*_bps` 改成 KB/s）。

需要破坏性变更时，开 `/api/v2/status`，`/api/v1` 保留一个版本周期。

---

## 8. 现在拿不到的数据

明确说清楚，免得 UI 做出来是空的：

| 想展示 | 能不能 | 原因 |
|---|---|---|
| 每条 IP 的实时速率 | ✅ | `rate_*_bps` |
| 每条 IP 的连接数 | ✅ | `active_conns` |
| 流量在各 IP 间的分配比例 | ✅ | 用 `weight`，或按 `bytes_down` 算实际占比 |
| 每个**目标**的连接分布 | ❌ | 需要在数据面按目标聚合，属于额外开销，暂不做 |
| 历史曲线 | ❌ | 服务端不存历史，只有当前值。要曲线请客户端自己攒 |
| 单条连接的速率 | ❌ | 同上 |
| 认证失败的具体原因 | ⚠️ | 只有日志里有，接口只给 `auth_status` 三态 |

历史曲线这条 C 要特别注意：**服务端是无状态的，重启即清零。** 曲线数据只能客户端自己滚动缓存。
