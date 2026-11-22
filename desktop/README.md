# CAU WIFI HELPER · 桌面端

Wails v3 + WebView2 的 Windows 桌面外壳，界面与 `../HELPER UI/原型/` 完全相同（同一套 HTML/CSS/JS，
零构建、不依赖 npm），只是多了两层：读真实状态的适配层，和接窗口按钮的壳层。

当前产物：`bin/caulink-desktop.exe`（约 10 MB，单文件，双击即用；未入库）。

---

## 1. 目录结构

```
desktop/                      ← Wails 独立 module（github.com/Br1m2y/caulink/desktop）
├─ main.go                    创建窗口：1440×900、最小 1024×700、无系统标题栏
├─ go.mod / go.sum
├─ Taskfile.yml               构建入口（已改成不依赖 npm）
├─ build/                     Wails 构建资源（图标、manifest、syso 生成）
│  └─ Taskfile.yml            其中 build:frontend 已被置空，见下文
├─ frontend/app/              ← go:embed 的就是这里
│  ├─ index.html              与原型一致，额外引入 /wails/runtime.js
│  ├─ styles.css              与原型一致（多几条 --wails-draggable 规则）
│  ├─ app.js                  与原型一致（纯渲染逻辑，不含平台代码）
│  ├─ mock.js                 演示数据源（与原型一致）
│  ├─ api.js                  ★ 桌面端新增：轮询 127.0.0.1:7788，失败退回演示数据
│  └─ shell.js                ★ 桌面端新增：最小化/最大化/关闭、双击标题栏最大化
└─ bin/caulink-desktop.exe    构建产物（未入库，见 .gitignore）
```

两个 `★` 文件是原型里没有的。想回到纯浏览器演示，只拷前四个文件即可。

---

## 2. 构建

需要 Go >= 1.26（见 `go.mod`）与 wails3 v3.1.0。**用脚本一键构建**（在仓库根目录）：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-desktop.ps1
```

脚本会把工具链与缓存都放进仓库的 `.tools/`（已 gitignore，不进版本库）：

| 变量 | 指向 | 为什么 |
|---|---|---|
| `GOPATH` / `GOBIN` | `.tools/gopath` | wails3 装这里，不污染系统 |
| `GOCACHE` | `.tools/gocache` | 受限环境下系统缓存目录会 Access denied |
| `GOTMPDIR` | `.tools/gotmp` | 同上，`go build` 建临时工作目录需要它 |
| `PATH` | 追加 Git 的 `usr\bin` | Taskfile 会调 `uname`/`test`/`tail` |

手动构建的话，等价于：

```powershell
$env:GOPATH  = "$PWD\.tools\gopath"
$env:GOBIN   = "$PWD\.tools\gopath\bin"
$env:GOCACHE = "$PWD\.tools\gocache"
$env:GOTMPDIR= "$PWD\.tools\gotmp"
$env:PATH    = "$env:GOBIN;C:\Program Files\Git\usr\bin;$env:PATH"
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0
cd desktop; wails3 build
```

每个新终端先设环境变量（PowerShell）：

```powershell
$root = '<仓库根目录>'
$git  = 'C:\Program Files\Git'   # 只为提供 uname / test / tail 等命令
$env:GOROOT      = "$root\.tools\go"
$env:GOPATH      = "$root\.tools\gopath"
$env:GOCACHE     = "$root\.tools\gocache"
$env:GOMODCACHE  = "$root\.tools\gopath\pkg\mod"
$env:GOPROXY     = 'https://goproxy.cn,direct'
$env:PATH        = "$root\.tools\go\bin;$root\.tools\gopath\bin;$git\usr\bin;$git\bin;$env:PATH"

Set-Location (Join-Path $PWD "desktop")   # 仓库根目录下的 desktop/
wails3 build
```

`wails3 build` 依次做：`go mod tidy` → 前端任务（已置空）→ 生成图标 → 生成 syso →
`go build -tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui"`。

### 为什么前端任务被置空

官方模板的前端步骤是 `npm install` + `npm run build` + `wails3 generate bindings`，
而本机没有 Node；这个项目的前端是手写静态页，不需要打包，也不需要绑定的 Go 服务。
所以 `build/Taskfile.yml` 里的 `build:frontend` 改成了 `deps: []` / `cmds: []`。

### 需要 Git 的 usr\bin 的原因

go-task 的部分任务会调 `uname` / `test` / `tail`（Git for Windows 自带一批 Unix 工具）。
没有它们时任务会以 exit 1 静默失败，且不打印原因——踩过一次，记录在这里。

---

## 3. 数据来源与降级

`api.js` 每秒 `fetch('http://127.0.0.1:7788/api/v1/status')`（超时 3 秒）：

- 拿到数据 → 标题栏右侧显示绿色「实时」，端口列表/表格/曲线全部来自守护进程；
- 连续 2 次失败 → 退回 `mock.js` 的演示数据，显示橙色「演示数据」，日志只记一次；
- 一次成功即切回实时。

守护进程侧不需要改：`caulinkd` 的 `/api/v1/status` 已带
`Access-Control-Allow-Origin: *`，WebView 里可以直接跨源读取。

字段对应关系（契约见仓库 `docs/status-api.md`）：

| 界面 | JSON |
| --- | --- |
| 端口名 / 副标题 | `interface` / `model`，缺省「未知设备」 |
| IP | `source_ip` |
| 正常 / 认证中 / 认证失败 / 未连接 | `auth_status` + `schedulable` + `errors[0]` |
| 下载速度 | `rate_down_bps`（字节/秒） |
| 延迟 | `rtt_ms` |
| 丢包率 | 主用端口 `loss_rate` |
| 主用端口 | `scheduler.sticky_id` |

---

## 4. 调试

设 `CAU_DEBUG_PORT` 会给 WebView2 开 CDP 端口（不设则完全关闭，与生产行为一致）：

```powershell
$env:CAU_DEBUG_PORT = '9333'
.\bin\caulink-desktop.exe
# 然后浏览器打开 http://127.0.0.1:9333/json 拿 webSocketDebuggerUrl，
# 或直接用 DevTools 协议点 DOM、读 window.wails
```

没有这个开关时，`window.wails` 为空对象——因为运行时脚本是 ES module，
必须写成 `<script type="module" src="/wails/runtime.js">`，且 `main.go` 里要用
`application.BundledAssetFileServer`（不是 `AssetFileServerFS`）才会提供这个路径。

---

## 5. 已实测

- 演示数据 / 实时数据两种模式切换正常，标题栏徽标正确；
- 最小化、最大化/还原、关闭三个按钮均生效（通过 CDP 派发真实鼠标事件验证）；
- 双击标题栏空白处 → 最大化/还原；
- 拖动手感依赖 `.titlebar { --wails-draggable: drag }` + 按钮上的 `no-drag`；
- 1024×700 最小窗口下五张统计卡自动折成 3+2，标签列隐藏。

## 6. 已知限制

- 窗口拖动只做了 CSS 声明式实现，未写自定义 WndProc；
- 曲线在采集初期（不足 60 秒）显示「正在采集 n/60 秒」，点数 < 8 时不画面积；
- 登录/清凭据仍是本地演示行为，没有写接口（契约 v1 是只读的）；
- `bin/` 未纳入版本库，换机需重新 `wails3 build`。

## 双击没反应 / WebView2 报 8000ffff

发布构建是 `-H windowsgui`，没有控制台，崩溃信息默认无处可看。这版已做三件事：

1. **日志落盘**：`%LOCALAPPDATA%\caulink-desktop\desktop.log`；该目录不可写时
   依次回退到 `%APPDATA%`、exe 所在目录、`%TEMP%`。
2. **致命错误弹窗**：`app.Run()` 返回错误时弹系统对话框并给出日志路径。
3. **WebView2 用户数据目录自动选址**：默认是 `%APPDATA%\<exe名>`，
   受限环境（沙箱、漫游配置异常、只读用户目录）里写不进去，
   表现就是 `error creating controller with 8000ffff: Catastrophic failure`——
   错误码毫无提示性。现在会挑一个**真实可写**的目录（写探针文件确认），
   依次尝试 `%LOCALAPPDATA%` → `%APPDATA%` → exe 目录 → `%TEMP%`。

> 这个 8000ffff 踩过一次：最初的版本在受限会话里双击毫无反应，
> 加日志后才发现是 WebView2 缓存目录写不进去。日志文件路径会打在每行开头。

## 设置页：重启 Steam 以支持多 IP 下载

设置页「游戏下载」分组里的开关，名字是
**「下载游戏时打开：重启 Steam 以使其支持多 IP 并行下载」**。

打开后桌面端会（走本地接口 `POST /api/steam-mode`）：

1. 把系统代理写成 `127.0.0.1:8080`（caulinkd 的 HTTP 代理）
2. 强制结束 `steam.exe`
3. 重新启动 Steam
4. **持续按住代理 90 秒**——Steam 会在启动后几十秒内才真正读取代理，
   而本地 VPN 客户端每 8 秒会把代理改回自己，单次设置必被抢走（实测：
   只设一次 → Steam 32 条连接全走 VPN；全程按住 → 26 条全走我们）
5. 每 1.5 秒把进度回显在开关下方

关闭开关 = 取消按住（不再抢注）。

接口安全：两个接口都要求 `X-Caulink-Action: 1` 头，避免本地其它程序误触；
非 Windows 平台返回 501。

对应的一次性脚本版本见 `scripts/start-steam-with-caulink.ps1`。