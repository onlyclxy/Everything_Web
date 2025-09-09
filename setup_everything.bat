@echo off
echo Setting up Everything SDK...

REM Try to find Everything64.dll from common locations
set EVERYTHING_DLL=

REM Check current directory first
if exist "Everything64.dll" (
    echo Found Everything64.dll in current directory
    set EVERYTHING_DLL=Everything64.dll
    goto :found
)

REM Check Program Files
if exist "C:\Program Files\Everything\Everything64.dll" (
    echo Found Everything64.dll in Program Files
    copy "C:\Program Files\Everything\Everything64.dll" "." /Y
    set EVERYTHING_DLL=Everything64.dll
    goto :found
)

REM Check Program Files (x86)
if exist "C:\Program Files (x86)\Everything\Everything64.dll" (
    echo Found Everything64.dll in Program Files (x86)
    copy "C:\Program Files (x86)\Everything\Everything64.dll" "." /Y
    set EVERYTHING_DLL=Everything64.dll
    goto :found
)

REM Check if Everything is running and get its path from process
for /f "tokens=2" %%i in ('tasklist /fi "imagename eq Everything.exe" /fo csv ^| findstr "Everything.exe"') do (
    echo Everything is running, trying to locate DLL...
    REM This is a fallback, manual copy might be needed
    goto :manual
)

:manual
echo.
echo ERROR: Could not automatically locate Everything64.dll
echo.
echo Please manually copy Everything64.dll to this directory from your Everything installation.
echo Common locations:
echo   C:\Program Files\Everything\Everything64.dll
echo   C:\Program Files (x86)\Everything\Everything64.dll
echo.
pause
exit /b 1

:found
echo.
echo Everything64.dll is ready!
echo You can now run everything-web-server.exe
echo.
pause

