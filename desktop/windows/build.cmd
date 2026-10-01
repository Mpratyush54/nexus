@echo off
REM Double-click or run from cmd/PowerShell without opening Notepad.
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0build.ps1" %*
