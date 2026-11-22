@echo off
rem ===========================================================
rem Start Steam through caulink multi-IP proxy.
rem
rem Why this wrapper exists: the desktop app itself may be blocked
rem from writing the system proxy by endpoint security policy.
rem Explorer-launched batch files (via PowerShell) are allowed.
rem
rem Args are passed to the PowerShell script, e.g.:
rem   start-steam-with-caulink.bat -HoldSeconds 120
rem ===========================================================
setlocal
set SCRIPT=%~dp0start-steam-with-caulink.ps1
echo Running: %SCRIPT%
powershell -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT%" %*
if errorlevel 1 (
    echo.
    echo Failed. See the error above.
    pause
)
endlocal