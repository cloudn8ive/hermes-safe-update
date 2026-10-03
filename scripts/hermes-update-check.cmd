@echo off
rem Look-only update check (used by the Start menu and the desktop palette). Updates and closes nothing; may fetch commits like git fetch.
call "%~dp0hermes-safe-update.cmd" --check
