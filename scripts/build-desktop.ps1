# 构建桌面端（Wails v3）—— 一条命令出 exe
#
# 为什么要有这个脚本：wails3 build 依赖 Taskfile，而 Taskfile 会调 uname/test/tail
# （Git for Windows 自带）以及 wails3 自身。三者都不在系统 PATH 里时，报错是
# "executable file not found in $PATH" 或者更难懂的 exit status 127。
# 另外 Go 的 build cache / tmp 目录在受限环境下会 Access denied，所以这里把它们
# 指到仓库内的 .tools/（已在 .gitignore 里）。
#
# 用法（仓库根目录）：
#   pwsh -File scripts/build-desktop.ps1
#   pwsh -File scripts/build-desktop.ps1 -SkipInstall    # 已装过 wails3 时跳过下载
#
# 产物：desktop/bin/caulink-desktop.exe

[CmdletBinding()]
param(
    [switch]$SkipInstall,
    [string]$WailsVersion = 'v3.1.0'
)

# 原生程序（wails3 / go）会把进度与错误都写到 stderr，PowerShell 5.1 在 Stop 模式下
# 会把它当成终止性错误。这里统一用 Continue，靠显式的 $LASTEXITCODE 判断成败。
$ErrorActionPreference = 'Continue'

$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$tools = Join-Path $root '.tools'
$gopath = Join-Path $tools 'gopath'
$gobin = Join-Path $gopath 'bin'

Write-Host "仓库根目录: $root" -ForegroundColor Cyan

# ---------- 1) 工具链环境 ----------
$env:GOPATH = $gopath
$env:GOBIN = $gobin
$env:GOCACHE = Join-Path $tools 'gocache'
$env:GOTMPDIR = Join-Path $tools 'gotmp'
$env:GOPROXY = if ($env:GOPROXY) { $env:GOPROXY } else { 'https://goproxy.cn,direct' }
New-Item -ItemType Directory -Force -Path $gobin, $env:GOCACHE, $env:GOTMPDIR | Out-Null

# Git for Windows 的 usr\bin 提供 uname/test/tail；Taskfile 会用到。
$gitUsr = $null
foreach ($candidate in @(
        'C:\Program Files\Git\usr\bin',
        'E:\Program Files\Git\usr\bin',
        'C:\Program Files (x86)\Git\usr\bin',
        "$env:LOCALAPPDATA\Programs\Git\usr\bin")) {
    if (Test-Path (Join-Path $candidate 'uname.exe')) { $gitUsr = $candidate; break }
}
if (-not $gitUsr) {
    throw "找不到 Git for Windows 的 usr\bin（需要 uname.exe）。装 Git 后重试，或手动把它加进 PATH。"
}
$env:PATH = "$gobin;$gitUsr;$(Split-Path -Parent $gitUsr)\cmd;$env:PATH"
Write-Host "Git unix 工具: $gitUsr" -ForegroundColor DarkGray

# ---------- 2) 安装 wails3 ----------
$wails = Join-Path $gobin 'wails3.exe'
if (-not (Test-Path $wails)) {
    if ($SkipInstall) { throw "未找到 $wails，且指定了 -SkipInstall" }
    Write-Host "安装 wails3 $WailsVersion ..." -ForegroundColor Yellow
    & go install "github.com/wailsapp/wails/v3/cmd/wails3@$WailsVersion"
    if ($LASTEXITCODE -ne 0) { throw "wails3 安装失败" }
}
$wailsVer = ''
try { $wailsVer = ((& $wails version 2>&1 | Out-String).Trim() -split "`n")[0] } catch { $wailsVer = '（查询失败，忽略）' }
Write-Host "wails3: $wailsVer" -ForegroundColor DarkGray

# ---------- 3) 构建 ----------
Push-Location (Join-Path $root 'desktop')
try {
    Write-Host "开始构建（首次较慢：要下载依赖并生成图标/syso）..." -ForegroundColor Yellow

    # 不使用 `wails3 build`：它内部的任务运行器在部分受限环境里会
    # 以 "open bin/caulink-desktop.exe: Access is denied" 失败，
    # 而同样的 go build 命令手工执行却完全正常。
    # 这里把它的三个步骤拆开显式执行，行为可预期、出错也好定位。
    Write-Host "  [1/3] 生成图标" -ForegroundColor DarkGray
    & $wails generate icons -input appicon.png `
        -macfilename darwin/icons.icns `
        -windowsfilename windows/icon.ico `
        -iconcomposerinput appicon.icon `
        -macassetdir darwin 2>&1 | Out-Null

    Write-Host "  [2/3] 生成 syso（清单 + 图标 + 版本信息）" -ForegroundColor DarkGray
    & $wails generate syso -arch amd64 `
        -icon windows/icon.ico `
        -manifest windows/wails.exe.manifest `
        -info windows/info.json `
        -out wails_windows_amd64.syso 2>&1 | Out-Null
    if (-not (Test-Path 'wails_windows_amd64.syso')) {
        Write-Host "  ⚠ syso 未生成，将构建出没有清单/图标的 exe" -ForegroundColor Yellow
    }

    Write-Host "  [3/3] go build" -ForegroundColor DarkGray
    $out = Join-Path $root 'desktop\bin\caulink-desktop.exe'
    & go build -tags production -trimpath -buildvcs=false `
        -ldflags="-w -s -H windowsgui" -o $out .
    if ($LASTEXITCODE -ne 0) { throw "go build 失败，退出码 $LASTEXITCODE" }

    # 构建完删掉 syso，避免干扰下次增量
    Get-ChildItem -Filter '*.syso' -File -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
} finally {
    Pop-Location
}

# 修正完整性级别：若 exe 带 Low 标签，进程会以 Low IL 运行，
# 之后连改系统代理都会被拒绝（症状是"界面正常但开关无效"）。
& icacls (Join-Path $root 'desktop\bin') /setintegritylevel Medium 2>&1 | Out-Null
& icacls (Join-Path $root 'desktop\bin\caulink-desktop.exe') /setintegritylevel Medium 2>&1 | Out-Null
Write-Host "已修正完整性级别为 Medium（否则改系统代理会被拒绝）" -ForegroundColor DarkGray

$exe = Join-Path $root 'desktop\bin\caulink-desktop.exe'
if (-not (Test-Path $exe)) { throw "构建结束但没找到产物 $exe" }

$size = [math]::Round((Get-Item $exe).Length / 1MB, 1)
Write-Host ""
Write-Host "构建成功：$exe（$size MB）" -ForegroundColor Green
Write-Host "双击运行即可；它读的是本机 http://127.0.0.1:7788/api/v1/status。" -ForegroundColor DarkGray
Write-Host "守护进程没起时会自动退回演示数据（标题栏显示「演示数据」）。" -ForegroundColor DarkGray
