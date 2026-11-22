# desktop —— Caulink 桌面控制面板（Wails v3）

本目录放 **Wails v3 桌面客户端**，与根模块的 Go 核心（`cmd/caulinkd`、`internal/*`）**完全解耦**：

- 客户端不 import 根模块的任何 Go 包，只通过 `internal/control` 暴露的本地 HTTP/WS 接口读写状态；
- 因此 `desktop/` 是**独立 Go module**（`github.com/Br1m2y/caulink/desktop`），不需要 `go.work`；
- 契约仍以 `pkg/contract/v1/ippool_entry.go` 与 `api/schema/v1/ippool_entry.schema.json` 为唯一真源，前端按 JSON Schema 消费。

## 为什么不再用 Flutter

见 `docs/adr/0002-ui-framework-wails.md`：Flutter 需要 1GB SDK + 2–7GB Visual Studio C++ 工具链，而 Wails 复用系统 WebView2，只多一个 Go 工具，产物约十几 MB。

## 初始化（每个人第一次拉完仓库后跑一次）

Wails 工程包含图标、清单、Taskfile 等生成物，不适合手写，统一用脚本生成：

```bash
# 1) 安装 Wails CLI（版本钉死，不要用 @latest）
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0
wails3 setup          # 检查并安装系统依赖（Windows 上会确认 WebView2）

# 2) 在本仓库根目录执行
scripts/init-desktop.sh
```

脚本会：

1. 在临时目录跑 `wails3 init -n caulink-desktop -t vanilla-js`；
2. 把生成物移入本目录（`main.go`、`greetservice.go`、`frontend/`、`build/`、`Taskfile.yml` 等）；
3. 把 `desktop/go.mod` 的 module 改成 `github.com/Br1m2y/caulink/desktop`；
4. 删掉示例 `greetservice.go`，换成调用本地控制面的最小服务。

## 生成后的目录

```text
desktop/
├── main.go              # Wails 应用入口（窗口、托盘、启动守护进程）
├── service.go           # 暴露给前端的 Go 方法：读 IP 池快照、启动/停止调度
├── go.mod               # github.com/Br1m2y/caulink/desktop
├── Taskfile.yml         # wails3 构建任务
├── frontend/
│   ├── index.html       # 单页控制面板
│   ├── src/main.js      # 调 window.go.main.* 与订阅 WS 事件
│   └── package.json     # 纯静态可不用 npm
└── build/               # 图标、清单、平台 Taskfile（生成物，提交）
```

## 与守护进程的关系

两种运行方式，Phase 3 前必须选一种定死：

| 方式 | 说明 | 取舍 |
|---|---|---|
| **A. 客户端拉起守护进程** | `caulink-desktop.exe` 启动时 exec `caulinkd.exe` 子进程，退出时杀掉 | 演示友好（双击一个图标），但要处理端口占用、残留进程 |
| **B. 守护进程常驻 + 客户端只连云** | `caulinkd` 作为服务/开机自启，客户端连 `127.0.0.1:<port>` | 更像正经产品，但要装服务、要提权 |

建议演示用 A、正式发布用 B，两者共用同一份 `internal/control` 接口。
