# 发布包启动器（`CAU WIFI HELPER.exe`）

发布包根目录那个可执行文件就是这里编出来的。它不是界面、也不是守护进程，只做三件事：

1. **校正源列表**：`caulinkd` 的 `sources` 记的是「网卡名 + 源 IP」，换机器、DHCP 换租约就失效；界面上的「重新扫描」只改内存、不回写配置。启动器在拉起之前按网卡现状重写一遍，并补上该 IP 对应的 `auth` 段。
2. **腾出 8080**：HTTP 代理固定用 `127.0.0.1:8080`（桌面端把地址写死了），被占用时 `caulinkd` 会当成致命错误直接退出。若占用者是 NI 的 Web 服务（`NIWebServiceContainer` 等，装过 LabVIEW / 制图软件的机器上常驻），弹一次确认后用 `runas` 提权 `sc stop` + 设为手动启动；停不掉就关掉 `http_proxy.listen`，其余功能照常，下次启动还能自动恢复。
3. **拉起界面**：用 `CAULINK_DAEMON` / `CAULINK_CONFIG` 传绝对路径，所以三个 exe 可以都放在 `app\` 子目录里，根目录只留一个带图标的可点文件。

## 构建

只能在 Windows 上构建：代码里用了 `syscall.NewLazyDLL`（查进程名）和 `runas`（提权停服务），非 Windows 会直接编译失败。

需要 Go 与 `wails3` CLI（与 `desktop/` 用的是同一个）。图标、版本信息、清单都从本目录的文件注入：

```bash
cd tools/launcher

wails3 generate syso -arch amd64 \
    -icon ../../desktop/build/windows/icon.ico \
    -manifest launcher.manifest \
    -info info.json \
    -out launcher.syso

go build -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui" -o "CAU WIFI HELPER.exe" .
rm launcher.syso
```

- `launcher.manifest`：`asInvoker`（不自带管理员权限，只在需要停服务时提权一次，避免每次启动都弹 UAC）+ 常见控件版本 + 高 DPI。
- `info.json`：`FileDescription` / `ProductName` / `FileVersion` 等，`wails3 generate syso` 会把它们写进 exe 的版本资源。任务管理器里看到的名字就是这里的 `FileDescription`。

## 运行时目录

```
CAU WIFI HELPER.exe          ← 本文这个
app/caulink-ui.exe           ← 桌面端（Wails）
app/caulinkd.exe             ← 守护进程
app/caulink-auth.exe         ← Dr.COM 认证器
configs/caulinkd.local.json  ← 首次运行自动生成（账号密码留空，界面里填）
data/                        ← 运行时状态、日志、凭据
```

`configs/caulinkd.local.json` 缺失时会写一份能直接跑的默认配置（`data_dir=data`、`socks5=127.0.0.1:1080`、`status=127.0.0.1:7788`、`http_proxy=127.0.0.1:8080`），不需要用户手写。

## 测试

```bash
go test ./...
```

五个用例都是纯函数级别的，不弹窗、不联网：`ownerOf`（对自身监听端口）、`isNIProcess`（精确匹配，防 `wininit.exe` 这类误伤）、`canBind`、`applyProxySetting`（关闭/恢复/幂等/缺段）。
