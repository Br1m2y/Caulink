# 用 caulink 的多 IP 代理启动 Steam
#
# 为什么需要这个脚本：
#   0dcloud（VPN 客户端）连着的时候，会每 8 秒把 Windows 系统代理改回自己
#   （HKCU\...\Internet Settings\ProxyServer = 127.0.0.1:17891）。
#   而 Steam 只在**启动时读一次**代理，之后就一直用缓存的那个。
#
#   所以顺序很关键：
#     先把系统代理写成我们的 8080 → 立刻启动 Steam（抢在 0dcloud 改写之前）
#   Steam 于是缓存 8080，后续 0dcloud 再怎么改注册表都影响不到它。
#
# 效果：
#   - Steam 下载 → 我们的代理 → 校园网两个 IP 并行（约 2 倍带宽）
#   - Steam 社区/商店 → 我们的代理按域名分流 → 0dcloud(17891) → VPN
#
# 用法（仓库根目录或任意位置）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/start-steam-with-caulink.ps1
#   ... -DryRun     只看会做什么，不真的启动
#
# 还原：运行 stop 版本或手动把系统代理改回 17891。

[CmdletBinding()]
param(
    [string]$SteamExe = 'D:\Program Files (x86)\Steam\Steam.exe',
    [string]$ProxyServer = '127.0.0.1:8080',

    # 启动后要"按住"系统代理多少秒。
    #
    # 实测：Steam 不是一启动就读代理，而是在启动后的几十秒内才真正读取；
    # 而 0dcloud 每 8 秒会把注册表改回自己。只设一次必然被抢走，
    # 所以要在整个启动窗口内持续压着。90 秒足够覆盖 Steam 的初始化。
    [int]$HoldSeconds = 90,

    [switch]$DryRun
)

$ErrorActionPreference = 'Continue'
$key = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings'

function Write-Step($msg) { Write-Host "  $msg" -ForegroundColor DarkGray }

# 让 WinINET 立刻感知设置变化，否则有些程序仍读旧缓存
function Update-WinInet {
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public class CaulinkWinInet {
    [DllImport("wininet.dll", SetLastError = true)]
    public static extern bool InternetSetOption(IntPtr h, int opt, IntPtr buf, int len);
}
'@ -ErrorAction SilentlyContinue
    $null = [CaulinkWinInet]::InternetSetOption([IntPtr]::Zero, 39, [IntPtr]::Zero, 0)
    $null = [CaulinkWinInet]::InternetSetOption([IntPtr]::Zero, 37, [IntPtr]::Zero, 0)
}

Write-Host "`n[1/3] 检查 caulinkd" -ForegroundColor Cyan
$daemon = Get-Process caulinkd -ErrorAction SilentlyContinue
if (-not $daemon) {
    Write-Host "  caulinkd 没在运行，请先启动：bin\caulinkd.exe -config configs\caulinkd.local.json" -ForegroundColor Yellow
    Write-Host "  （本次仍会设置代理并启动 Steam，但下载不会聚合）" -ForegroundColor Yellow
} else {
    Write-Step "caulinkd 运行中（PID $($daemon.Id)）"
    $status = $null
    try { $status = Invoke-RestMethod -Uri 'http://127.0.0.1:7788/api/v1/status' -TimeoutSec 3 } catch { }
    if ($status) {
        $ok = ($status.ips | Where-Object { $_.schedulable }).Count
        Write-Step "状态接口正常，可调度源 IP：$ok 个"
    }
}

Write-Host "`n[2/3] 把系统代理指向 $ProxyServer" -ForegroundColor Cyan
$orig = (Get-ItemProperty $key).ProxyServer
Write-Step "原值：$orig"
if (-not $DryRun) {
    Set-ItemProperty -Path $key -Name ProxyServer -Value $ProxyServer
    Set-ItemProperty -Path $key -Name ProxyEnable -Value 1
    Update-WinInet
    Write-Step "已改为：$((Get-ItemProperty $key).ProxyServer)"
}

Write-Host "`n[3/4] 关闭正在运行的 Steam" -ForegroundColor Cyan
# 必须真的关掉再开：Steam 只会用第一个实例启动时读到的代理。
# 若它已在运行，再 Start-Process 只会激活老窗口，代理设置不会生效。
# （这个坑踩过：脚本先跑、Steam 早已在运行，结果流量仍然走 VPN。）
$existing = Get-Process steam -ErrorAction SilentlyContinue
if ($existing) {
    Write-Step "发现 Steam 正在运行（PID $($existing.Id -join ',')），先结束它"
    taskkill /F /IM steam.exe 2>&1 | Out-Null
    for ($w = 0; $w -lt 15; $w++) {
        Start-Sleep -Milliseconds 500
        if (-not (Get-Process steam -ErrorAction SilentlyContinue)) { break }
    }
    if (Get-Process steam -ErrorAction SilentlyContinue) {
        Write-Host "  ! Steam 未能完全退出，继续启动可能失败" -ForegroundColor Yellow
    } else {
        Write-Step "Steam 已退出"
    }
} else {
    Write-Step "Steam 当前未运行"
}

Write-Host "`n[4/4] 启动 Steam（必须紧跟在改代理之后）" -ForegroundColor Cyan
if (-not (Test-Path $SteamExe)) {
    Write-Host "  找不到 Steam：$SteamExe" -ForegroundColor Red
    Write-Host "  用 -SteamExe 指定实际路径" -ForegroundColor Red
    exit 1
}
if ($DryRun) {
    Write-Step "[DryRun] 将启动 $SteamExe，并持续按住代理 $HoldSeconds 秒"
} else {
    # 直接启动 Steam。
    #
    # 本脚本的正常用法是由用户双击快捷方式触发（Explorer → bat → powershell），
    # 本来就在普通用户权限下，Steam 不会被提权。
    # 不要改成 explorer 交接：在提权上下文里 explorer 会以 0xc0000142 初始化失败。
    Start-Process -FilePath $SteamExe
    Write-Step "已启动：$SteamExe"

    Write-Host "`n[保持] 持续把系统代理按在 $ProxyServer（$HoldSeconds 秒）" -ForegroundColor Cyan
    Write-Host "  Steam 会在启动后几十秒内读取代理，期间不能让它被 VPN 抢走" -ForegroundColor DarkGray
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    $restored = 0
    while ($sw.Elapsed.TotalSeconds -lt $HoldSeconds) {
        Set-ItemProperty -Path $key -Name ProxyServer -Value $ProxyServer -ErrorAction SilentlyContinue
        Set-ItemProperty -Path $key -Name ProxyEnable -Value 1 -ErrorAction SilentlyContinue
        $restored++
        Start-Sleep -Milliseconds 700
        if ($restored % 14 -eq 0) {
            $left = [math]::Round($HoldSeconds - $sw.Elapsed.TotalSeconds)
            Write-Host "  还剩 $left 秒（已抢回 $restored 次）" -ForegroundColor DarkGray
        }
    }
    Write-Step "按住结束，共抢回 $restored 次；Steam 应已缓存 $ProxyServer"
}

# 验证：看 Steam 的连接落在哪个端口
if (-not $DryRun) {
    Start-Sleep -Seconds 3
    $steam = Get-Process steam -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($steam) {
        $conns = Get-NetTCPConnection -OwningProcess $steam.Id -State Established -ErrorAction SilentlyContinue
        $toOurs = (($conns | Where-Object { $_.RemotePort -eq 8080 }) | Measure-Object).Count
        $toVPN = (($conns | Where-Object { $_.RemotePort -eq 17891 }) | Measure-Object).Count
        Write-Host "`n验证：Steam → 我们(8080) $toOurs 条 / VPN(17891) $toVPN 条" -ForegroundColor Green
        if ($toOurs -eq 0 -and $toVPN -gt 0) {
            Write-Host "  ⚠ 仍在走 VPN：可以把 -HoldSeconds 调大（比如 150）再试" -ForegroundColor Yellow
        }
    }
}

Write-Host "`n验证方法：" -ForegroundColor Green
Write-Host "  1) Steam 里开始/恢复一个下载" -ForegroundColor DarkGray
Write-Host "  2) 跑下面这条看连接走向：" -ForegroundColor DarkGray
Write-Host "     Get-NetTCPConnection -OwningProcess (Get-Process steam).Id -State Established |" -ForegroundColor DarkGray
Write-Host "       Group-Object RemotePort | Select Count,Name" -ForegroundColor DarkGray
Write-Host "     远端端口应集中在 8080（我们），而不是 17891（VPN）" -ForegroundColor DarkGray
Write-Host "  3) 桌面面板应显示两个 IP 各约 5.9 MB/s、合计 11-12 MB/s`n" -ForegroundColor DarkGray
