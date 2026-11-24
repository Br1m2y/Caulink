# CAU WIFI HELPER 速度诊断脚本
# 用法：在新电脑上用「管理员 PowerShell」运行；全程只读 + 少量下载到 NUL，不修改任何设置。
$ErrorActionPreference = 'Continue'
$url = 'https://mirrors.tuna.tsinghua.edu.cn/ubuntu-releases/22.04/ubuntu-22.04.5-desktop-amd64.iso'
$MB = 1048576

function Section($t) { Write-Host ""; Write-Host "=== $t ===" -ForegroundColor Cyan }

Section "1. 网卡与链路速率"
Get-NetAdapter | Where-Object { $_.Status -eq 'Up' } |
  Select-Object Name, LinkSpeed, MacAddress | Format-Table -AutoSize | Out-String -Width 200

Section "2. 无线链路详情（重点看两块网卡是否同一 BSSID / 同一信道）"
netsh wlan show interfaces |
  Select-String '名称|Name|状态|State|SSID|BSSID|信号|Signal|接收速率|Receive rate|传输速率|Transmit rate|信道|Channel|频段|Band' | Out-String -Width 200

Section "3. 各网卡的单连接速率（10 秒）"
$ips = Get-NetIPAddress -AddressFamily IPv4 |
  Where-Object { $_.IPAddress -notmatch '^(127\.|169\.254)' }
foreach ($i in $ips) {
  $sp = curl.exe -s -o NUL -w '%{speed_download}' --max-time 10 --interface $i.IPAddress $url 2>$null
  $v = 0.0; [double]::TryParse([string]$sp, [ref]$v) | Out-Null
  '{0,-14} {1,-15} 单连接 {2,6:N2} MB/s' -f $i.InterfaceAlias, $i.IPAddress, ($v / $MB)
}

Section "4. 各网卡的 4 并发速率（10 秒）"
foreach ($i in $ips) {
  $jobs = 1..4 | ForEach-Object {
    Start-Job -ScriptBlock {
      param($u, $ip)
      $s = curl.exe -s -o NUL -w '%{speed_download}' --max-time 10 --interface $ip $u 2>$null
      [double]($s)
    } -ArgumentList $url, $i.IPAddress
  }
  $sum = ($jobs | Wait-Job -Timeout 30 | Receive-Job | Measure-Object -Sum).Sum
  $jobs | Remove-Job -Force -ErrorAction SilentlyContinue
  '{0,-14} {1,-15} 4 并发 {2,6:N2} MB/s' -f $i.InterfaceAlias, $i.IPAddress, ($sum / $MB)
}

Section "5. 汇总：全部网卡合计（不绑源，走默认路由，8 并发）"
$jobs = 1..8 | ForEach-Object {
  Start-Job -ScriptBlock {
    param($u)
    [double](curl.exe -s -o NUL -w '%{speed_download}' --max-time 10 $u 2>$null)
  } -ArgumentList $url
}
$sum = ($jobs | Wait-Job -Timeout 30 | Receive-Job | Measure-Object -Sum).Sum
$jobs | Remove-Job -Force -ErrorAction SilentlyContinue
'默认路由 8 并发 {0,6:N2} MB/s' -f ($sum / $MB)

Section "6. CPU 与杀毒软件"
Get-CimInstance Win32_Processor | Select-Object LoadPercentage | Format-Table -AutoSize | Out-String -Width 200
$mp = Get-MpComputerStatus -ErrorAction SilentlyContinue
if ($mp) { 'Defender 实时保护: ' + $mp.RealTimeProtectionEnabled }
$pref = Get-MpPreference -ErrorAction SilentlyContinue
if ($pref) { '排除目录: ' + (($pref.ExclusionPath) -join '; ') }

Section "7. 磁盘写入速度（256 MB，写到加速器保存目录）"
$dir = 'C:\Users\Public\caulink-disktest'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$path = Join-Path $dir 't.bin'
$buf = New-Object byte[] (8 * 1024 * 1024)
$sw = [Diagnostics.Stopwatch]::StartNew()
try {
  $fs = [IO.File]::Create($path)
  for ($k = 0; $k -lt 32; $k++) { $fs.Write($buf, 0, $buf.Length) }
  $fs.Flush($true); $fs.Close()
  $sw.Stop()
  '写入 256 MB 用时 {0:N2} 秒 → {1:N0} MB/s' -f $sw.Elapsed.TotalSeconds, (256 / $sw.Elapsed.TotalSeconds)
} catch {
  '磁盘测试失败: ' + $_.Exception.Message
} finally {
  Remove-Item $path -Force -ErrorAction SilentlyContinue
  Remove-Item $dir -Force -ErrorAction SilentlyContinue
}

Section "8. 空闲时的后台流量（5 秒）"
$before = @{}
Get-NetAdapterStatistics | Where-Object { $_.ReceivedBytes -gt 0 } | ForEach-Object { $before[$_.Name] = $_.ReceivedBytes }
Start-Sleep -Seconds 5
Get-NetAdapterStatistics | Where-Object { $before.ContainsKey($_.Name) } | ForEach-Object {
  $delta = $_.ReceivedBytes - $before[$_.Name]
  '{0,-14} 5 秒接收 {1,8:N2} MB' -f $_.Name, ($delta / $MB)
}

Write-Host ""
Write-Host "诊断完成。把整段输出发给开发者即可。" -ForegroundColor Green
exit 0
