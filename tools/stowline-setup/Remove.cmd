@echo off
setlocal
cd /d "%~dp0"

net session >nul 2>&1
if %errorlevel% NEQ 0 (
  echo Requesting Administrator via UAC...
  powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath '%~f0' -WorkingDirectory '%~dp0' -Verb RunAs"
  exit /b
)

set "SCRIPT=%~dp0payload\wizard\remove_stowline.ps1"
if not exist "%SCRIPT%" (
  echo ABORT: remove_stowline.ps1 missing from package.
  pause
  exit /b 1
)

powershell -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT%"
set ERR=%ERRORLEVEL%
if %ERR% NEQ 0 (
  echo Remove ended with error %ERR%
)
pause
exit /b %ERR%
