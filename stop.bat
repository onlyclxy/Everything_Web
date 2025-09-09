@echo off
chcp 65001 >nul
echo 正在停止Everything Web Server...

REM 查找占用8080端口的进程
for /f "tokens=5" %%a in ('netstat -ano ^| findstr :8080') do (
    set PID=%%a
    goto :found
)

echo 没有找到运行在8080端口的服务器
pause
exit /b 1

:found
echo 找到进程ID: %PID%

REM 检查是否是我们的服务器程序
tasklist | findstr %PID% | findstr "everything-web-server.exe main.exe go.exe"
if %ERRORLEVEL% equ 0 (
    taskkill /PID %PID% /F
    echo 服务器已停止
) else (
    echo 警告：端口8080被其他程序占用，请手动处理
)

pause 