# ADR 0002 · 桌面界面框架选 Wails v3（替代 Flutter）

- 状态：已采纳
- 影响范围：`desktop/`、`.github/CODEOWNERS`、`.github/workflows/ci.yml`、`docs/` 中的实施步骤
- 相关：`desktop/README.md`、`scripts/init-desktop.sh`

## 背景

控制面板需要展示 IP 池状态、实时速率、连接分布和调度策略，并支持一键启停。核心是 Go 写的本地守护进程 `caulinkd`，控制面是 `internal/control` 提供的本地 HTTP/WS 接口（仅监听 `127.0.0.1`）。

原设计选 Flutter，但落地时撞上三个现实问题：

1. **安装成本**：Flutter SDK 约 1GB，Windows 桌面端还要 Visual Studio 的 C++ 构建工具（2–7GB，安装 30 分钟起）。三人团队里只要有一个人没配好，演示就出不来。
2. **技能错配**：核心层是 Go，界面层用 Dart 等于凭空多一门语言，且团队没人有 Flutter 经验。
3. **架构上多余**：控制面已经是 HTTP/WS，Flutter 客户端还要自己再实现一套本地通信与状态同步；Web 技术栈可以直接用 `fetch` / `WebSocket`。

## 决策

桌面客户端使用 **Wails v3**（Go + 系统 WebView2），工程放在 `desktop/`，作为**独立 Go module**（`github.com/Br1m2y/caulink/desktop`），不 import 根模块任何包，只走本地 HTTP/WS。

## 理由

- 复用系统 WebView2（Win10/11 基本预装），产物十几 MB，不需要打包浏览器内核；
- 后端仍是 Go，与服务端同一套语言和工具链，团队不需要学 Dart/Rust；
- 前端就是 HTML/CSS/JS，`index.html` 里直接引本地 `echarts.min.js` 画曲线；
- 安装成本相比 Flutter 少一个数量级：只多一个 `go install` + `wails3 setup`；
- Wails v3 是 2026-10 的稳定版，官方提供托盘、多窗口、开发热重载。

## 后果

**正面**

- 工具链只有 Go + 可选 Node（纯静态前端不需要 npm）；
- 与 `internal/control` 的 HTTP/WS 接口天然对齐，契约仍以 `api/schema/v1/ippool_entry.schema.json` 为真源；
- CI 里可以只跑 `go vet/test/build`，桌面构建放到 Windows 本机或 Windows runner。

**负面 / 需要接受**

- 依赖 WebView2 Runtime；Win7/8 或精简系统可能缺失，安装包要带 bootstrapper（或检测后提示）；
- 没有移动端能力（本项目也不需要）；
- `desktop/` 的 `build/`、`Taskfile.yml`、`main.go` 是 `wails3 init` 的生成物，升级 Wails 时要按模板手工对齐；
- Linux 下需要 GTK4 + WebKitGTK，CI 的 Ubuntu runner 装齐比较费事，因此桌面构建默认 `continue-on-error`。

## 被否决的备选

| 方案 | 否决原因 |
|---|---|
| Flutter | 安装体积、技能错配、与控制面架构重复（见背景） |
| Tauri（Rust + Web） | 工具链负担和 Flutter 一个量级（Rust + MSVC），还要多学一门 Rust |
| Electron | 产物 150MB+、内存占用高，与"本地轻量代理工具"的定位冲突 |
| Fyne（纯 Go GUI） | Windows 上 CGO 需要 mingw-w64，图表能力弱，仪表盘观感一般 |
| 纯浏览器页面（`go:embed` 静态资源） | 作为**降级方案保留**：只要 Go 就能跑，演示时可用；缺点是没有原生窗口和托盘 |

## 迁移影响

- `app/`（Flutter）目录已删除，替换为 `desktop/`；
- `pkg/contract/v1` 与 `api/schema/v1` **零改动**，A、B 不受影响；
- C 的工作内容从 Dart 改为 HTML/JS + Go service。
