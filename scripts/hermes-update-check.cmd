@echo off
rem Read-only update check (used by the Start menu and the desktop palette). Changes nothing.
call "%~dp0hermes-safe-update.cmd" --check
