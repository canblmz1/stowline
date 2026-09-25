@echo off
rem Stowline - backup key escrow helper. Double-click; it asks for administrator rights itself.
rem Read-only: no install, no service change, no key change.
net session >nul 2>&1
if errorlevel 1 goto elevate
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0Export-ResticKeyEscrow.ps1" %*
echo.
pause
exit /b
:elevate
powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
exit /b
