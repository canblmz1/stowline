@echo off
setlocal
cd /d "%~dp0"

net session >nul 2>&1
if %errorlevel% NEQ 0 (
  echo Requesting Administrator via UAC...
  powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath '%~f0' -WorkingDirectory '%~dp0' -Verb RunAs"
  exit /b
)

set "PAYLOAD=%~dp0payload"
if not exist "%PAYLOAD%\runtime\python.exe" (
  echo ABORT: embedded Python runtime missing. This package is incomplete.
  pause
  exit /b 1
)
if not exist "%PAYLOAD%\bin\stowline-agent.exe" (
  echo ABORT: qualified agent missing from package.
  pause
  exit /b 1
)

set "STOWLINE_SETUP_ROOT=%PAYLOAD%"
set "STOWLINE_DEPLOY_JSON=%~dp0deploy.json"
REM Do not inherit developer toolchains. Windows + embedded runtime only.
set "PATH=%PAYLOAD%\runtime;%PAYLOAD%\runtime\Scripts;C:\Windows\System32;C:\Windows;C:\Windows\System32\Wbem"

"%PAYLOAD%\runtime\python.exe" "%PAYLOAD%\wizard\bootstrap.py" %*
set ERR=%ERRORLEVEL%
if %ERR% NEQ 0 (
  echo Setup ended with error %ERR%
  pause
)
exit /b %ERR%
