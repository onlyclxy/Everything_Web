@echo off
echo 正在编译最终优化版服务器...
echo.
echo ?? 功能特性:
echo - 智能搜索缓存: 翻页瞬间响应  
echo - 格式兼容性检测: 自动识别视频格式
echo - AVI/MKV等格式: 智能提示和备用方案
echo - 完整日志记录: 详细的运行状态
echo.

go build -o everything-web-server-final.exe main.go
if %errorlevel% equ 0 (
    echo ? 编译成功! 生成 everything-web-server-final.exe
    echo.
    echo ?? 测试建议:
    echo 1. 搜索 ext:png - 测试大量结果缓存
    echo 2. 搜索 ext:mp4 - 测试兼容格式播放  
    echo 3. 搜索 ext:avi - 测试格式兼容性提示
    echo 4. 访问 /api/cache-status - 查看缓存状态
    echo.
    echo ?? 现在可以运行: everything-web-server-final.exe
) else (
    pause
) 