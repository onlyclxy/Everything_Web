@echo off
echo 测试编译ffmpeg版本...
go build -o everything-web-server-ffmpeg.exe main.go
echo 编译完成，错误码: %errorlevel%
if %errorlevel% neq 0 (
    echo 编译失败，请检查语法
) else (
    echo 编译成功！新文件: everything-web-server-ffmpeg.exe
)
pause 