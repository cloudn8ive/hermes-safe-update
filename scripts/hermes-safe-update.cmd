@echo off
rem Safe Hermes update: runs hermes-safe-update.exe outside the desktop app.
rem Wrapper for hermes-safe-update.exe:
rem   hermes-safe-update.cmd --check      (pre-flight only: no update; may fetch commits like git fetch)
rem   hermes-safe-update.cmd              (asks, then closes Hermes and updates)
rem The exe is looked up next to this file, then in
rem %LOCALAPPDATA%\Programs\hermes-safe-update, then on PATH.
setlocal
set "HERMES_HOME="
set "FOUND="
set "EXE=%~dp0hermes-safe-update.exe"
if not exist "%EXE%" set "EXE=%LOCALAPPDATA%\Programs\hermes-safe-update\hermes-safe-update.exe"
if not exist "%EXE%" for /f "delims=" %%P in ('where hermes-safe-update.exe 2^>nul') do if not defined FOUND set "FOUND=1" & set "EXE=%%P"
if not exist "%EXE%" (
  echo hermes-safe-update.exe not found next to %~nx0, in %LOCALAPPDATA%\Programs\hermes-safe-update or on PATH
  pause
  exit /b 2
)
if "%~1"=="--check" (title Hermes Update Check) else (title Hermes Safe Update)
"%EXE%" %*
set "RC=%ERRORLEVEL%"
echo.
echo Finished with exit code %RC%. Log: %LOCALAPPDATA%\hermes\logs\safe-update.log
pause
exit /b %RC%
