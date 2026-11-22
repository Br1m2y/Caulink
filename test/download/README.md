# 真实网络下的并行下载实验

这组用例回答一个问题：**「多连接 + `Range` 并行下载」经本项目自己的代理链路（`ippool → scheduler → dialer → socks5 / httpproxy`）到底行不行**。它只用仓库公开接口，不改任何被测代码。

第一次运行的实测结果见 [`docs/reports/parallel-download.md`](../../docs/reports/parallel-download.md) 与 [`docs/reports/dual-path.md`](../../docs/reports/dual-path.md)。

## 默认跳过

要真实校园网环境（已认证的源 IP、能访问公网镜像站），所以整包默认跳过，CI 不会跑：

```bash
# Linux / macOS
CAULINK_LIVE_TEST=1 go test ./test/download/ -v -timeout 600s

# Windows CMD
set CAULINK_LIVE_TEST=1 && go test ./test/download/ -v -timeout 600s

# PowerShell
$env:CAULINK_LIVE_TEST=1; go test ./test/download/ -v -timeout 600s
```

不带环境变量时 `TestMain` 直接退出，输出一行提示。

## 跑之前要改的常量

| 文件 | 常量 | 说明 |
|---|---|---|
| `dl_test.go` | `targetURL` | 支持 Range 的直链，默认清华 TUNA 的 Ubuntu ISO |
| | `srcIP` / `iface` | 本机已认证的源 IP 与对应网卡名 |
| `dl_two_ip_test.go` | `srcA` / `srcB` | 两个已认证的源 IP（双源叠加用） |
| `dl_two_ip_win_test.go` | 同上 | 另含 `IP_UNICAST_IF` 的位号与字节序，见文件内注释 |

## 四个文件各测什么

| 文件 | 平台 | 验证什么 |
|---|---|---|
| `dl_test.go` | 通用 | 4 连接各取 5 MiB 经 HTTP 代理与 SOCKS5，拼接 md5 与单流逐字节相同；单 IP 下聚合速率 ≈ 单连接（证明限速按 IP 计量） |
| `dl_two_ip_test.go` | 通用 | 8 分片铺到两个源 IP，统计每路实际搬运字节；含 `TestSpreadAcrossEntries`（两条并发是否落到不同条目）、`TestPlainHTTPRePicks`（明文 HTTP 是否逐请求换 IP） |
| `dl_two_ip_win_test.go` | Windows | 双网卡同网段时只绑 `LocalAddr` 与加 `IP_UNICAST_IF` 的对照；包含 `DisableKeepAlives` 的正确写法 |
| `dl_diag_test.go` | Windows | 诊断用：把拨号、首字节、读超时分别打印出来，定位是「连不上」还是「连上没数据」 |

## 两个容易测错的点

1. **keep-alive 会把并行请求串成一条连接。** 默认 `http.Transport` 下 8 个 `Range` 请求复用同一条隧道，测出来是单连接串行（第一轮 5.93 MB/s 的假象就是这么来的）。必须 `DisableKeepAlives: true`。
2. **拨号次数 ≠ 分片数。** Go 的 `Transport` 在隧道建连失败时会静默重试，8 片可能产生 10 次拨号。统计按实际搬运字节算，不要按拨号次数算。

单 IP 环境只能验证机制正确，验证不了带宽叠加；叠加要看两个源 IP 是否共享上游（同 AP 只有 1.39×，见上引报告）。
