# 把可执行文件的完整性级别修正为 Medium
#
# 为什么需要：如果 exe 被打上 "Low Mandatory Level" 的文件标签，
# 从这里启动的进程会以 **Low 完整性级别**运行，随后连
# HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings 都写不了，
# 报 "Access is denied"。
#
# 典型场景：文件由某个沙箱/受限环境创建或重新编译，继承了 Low 标签。
# 症状很有迷惑性——程序能正常启动、界面正常，只有"改系统代理"失败。
#
# 用法（仓库根目录）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/fix-integrity.ps1

[CmdletBinding()]
param(
    [switch]$Quiet
)

$ErrorActionPreference = 'Continue'
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)

$targets = @(
    (Join-Path $root 'bin'),
    (Join-Path $root 'desktop\bin')
)

$fixed = 0
foreach ($dir in $targets) {
    if (-not (Test-Path $dir)) { continue }

    # 目录本身也设成 Medium，这样之后新落进去的文件会继承
    & icacls $dir /setintegritylevel Medium 2>&1 | Out-Null

    Get-ChildItem $dir -Filter *.exe -File -ErrorAction SilentlyContinue | ForEach-Object {
        $before = (icacls $_.FullName 2>&1 | Select-String 'Mandatory Label').Line
        & icacls $_.FullName /setintegritylevel Medium 2>&1 | Out-Null
        $after = (icacls $_.FullName 2>&1 | Select-String 'Mandatory Label').Line

        if (-not $Quiet) {
            Write-Host ("{0}" -f $_.Name)
            Write-Host ("  前: {0}" -f (($before -replace '\s+', ' ').Trim()))
            Write-Host ("  后: {0}" -f (($after -replace '\s+', ' ').Trim()))
        }
        $fixed++
    }
}

if (-not $Quiet) {
    if ($fixed -eq 0) {
        Write-Host '没有找到需要处理的 exe（先构建一次）' -ForegroundColor Yellow
    } else {
        Write-Host ''
        Write-Host "已处理 $fixed 个可执行文件，完整性级别为 Medium。" -ForegroundColor Green
        Write-Host '现在它们（及其拉起的进程）可以正常读写用户级系统设置了。' -ForegroundColor DarkGray
    }
}
