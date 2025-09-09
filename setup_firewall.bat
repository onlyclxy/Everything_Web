@echo off
echo 配置Windows防火墙以允许Everything Web Server访问...
echo.

REM 检查管理员权限
net session >nul 2>&1
if %errorLevel% == 0 (
    echo ✅ 已获得管理员权限
) else (
    echo ❌ 需要管理员权限来配置防火墙
    echo 请右键点击此文件，选择"以管理员身份运行"
    pause
    exit /b 1
)

echo.
echo 正在添加防火墙规则...

REM 删除可能存在的旧规则
netsh advfirewall firewall delete rule name="Everything Web Server" >nul 2>&1

REM 添加新规则允许TCP端口8080
netsh advfirewall firewall add rule name="Everything Web Server" dir=in action=allow protocol=TCP localport=8080 >nul 2>&1

if %errorLevel% == 0 (
    echo ✅ 防火墙规则添加成功！
    echo.
    echo 现在您可以通过以下地址访问服务器：
    echo   - 本地：http://127.0.0.1:8080
    echo   - 本地：http://localhost:8080
    echo   - 局域网：http://您的IP地址:8080
    echo.
    echo 💡 提示：启动服务器后会自动显示您的具体IP地址
) else (
    echo ❌ 防火墙规则添加失败
    echo 请手动执行以下命令：
    echo netsh advfirewall firewall add rule name="Everything Web Server" dir=in action=allow protocol=TCP localport=8080
)

echo.
echo 其他可能的解决方案：
echo 1. 检查路由器端口转发设置
echo 2. 确保Everything服务正在运行
echo 3. 检查杀毒软件是否阻止了网络访问
echo 4. 尝试临时关闭Windows防火墙测试

echo.
pause

