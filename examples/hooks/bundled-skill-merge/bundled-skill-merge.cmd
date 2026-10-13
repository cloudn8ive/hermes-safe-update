@echo off
rem hermes-safe-update hook wrapper (Windows) for bundled_skill_merge.py.
rem Runs the script with the Python that hermes-safe-update found for Hermes: the variable
rem HERMES_SAFE_UPDATE_PYTHON, looked up when the hook starts (never a fixed path: the folder name
rem of Hermes' Python changes on updates). HERMES_SAFE_UPDATE_HOME and _CHECKOUT are read by the script.
rem
rem   no argument      = merge (what the hook runs)
rem   list | keep NAME | unkeep NAME | merge --dry-run   = manual use, see README.md
rem
rem Outside a hook set HERMES_SAFE_UPDATE_PYTHON and HERMES_SAFE_UPDATE_HOME first.
rem The script's LAST output line becomes the summary-card row; exit code 1 = a warning on the card.
setlocal
set "PYTHONUTF8=1"
if not defined HERMES_SAFE_UPDATE_PYTHON goto nopython
if not exist "%HERMES_SAFE_UPDATE_PYTHON%" goto nopython
if "%~1"=="" goto merge
"%HERMES_SAFE_UPDATE_PYTHON%" -W ignore "%~dp0bundled_skill_merge.py" %*
exit /b %ERRORLEVEL%
:merge
"%HERMES_SAFE_UPDATE_PYTHON%" -W ignore "%~dp0bundled_skill_merge.py" merge
exit /b %ERRORLEVEL%
:nopython
echo bundled skills: skipped - HERMES_SAFE_UPDATE_PYTHON is not set to an existing Python (run this as a hermes-safe-update hook, or set it yourself)
exit /b 1
