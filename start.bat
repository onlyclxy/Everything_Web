@echo off
chcp 65001 >nul
title Everything Web Server
echo =================================
echo  Everything Web Server 启动器
echo =================================
echo.

REM 检查es.exe是否存在
if not exist Everything64.dll (
    echo 错误：没有找到Everything64.dll文件！
    echo 请确保Everything的Everything64.dll命令行工具在当前目录
    pause
    exit /b 1
)

REM 检查可执行文件是否存在
if not exist everything-web-server.exe (
    echo 没有找到可执行文件，正在编译...
    call build.bat
)

echo 启动Everything Web Server...
echo 新功能：✅ 分页显示 ✅ 详细日志 ✅ 独立视频播放器
echo 服务地址: http://localhost:8080
echo.
echo 功能说明：
echo • 支持分页显示（每页20-200条结果）
echo • 实时日志记录和错误诊断
echo • 独立视频播放页面（新窗口）
echo • 视频播放状态监控
echo.
echo 按 Ctrl+C 停止服务器
echo =================================
echo.

everything-web-server.exe 
pause