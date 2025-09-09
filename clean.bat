@echo off
echo 正在清理编译文件...

if exist everything-web-server.exe (
    del everything-web-server.exe
    echo 已删除 everything-web-server.exe
) else (
    echo 没有找到可执行文件
)

echo 清理完成！
pause 