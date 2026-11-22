@echo off
rem ============================================================
rem 启动 caulinkd 守护进程（双击即可）
rem
rem 为什么要用这个而不是让别的程序代启：
rem   某些环境下，由其它程序（或受限会话）拉起的进程会继承一个受限令牌，
rem   之后连"改系统代理"都会被拒绝（Access is denied / PermissionDenied）。
rem   由你自己双击启动的进程带的是正常令牌，桌面端的"重启 Steam"开关才能生效。
rem
rem 关掉这个窗口 = 停止守护进程（代理会一起停）。
rem ============================================================

setlocal
cd /d "%~dp0.."

if not exist "bin\caulinkd.exe" (
    echo.
    echo 找不到 bin\caulinkd.exe，请先构建：
    echo   go build -o bin\caulinkd.exe .\cmd\caulinkd
    echo.
    pause
    exit /b 1
)

echo 正在启动 caulinkd（关闭此窗口即停止）...
echo.
"bin\caulinkd.exe" -config "configs\caulinkd.local.json" -v
if errorlevel 1 (
    echo.
    echo caulinkd 异常退出，错误码 %errorlevel%
    pause
)
endlocal
