@echo off
rem ===========================================================
rem Close Steam (used by the "Steam multi-IP" toggle when turning off).
rem
rem Kept in a separate batch file so the desktop app can trigger it via
rem Explorer when the app process itself is blocked from spawning children.
rem ===========================================================
taskkill /F /IM steam.exe >nul 2>&1
if errorlevel 1 (
    exit /b 0
)
echo Steam closed.
