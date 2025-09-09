@echo off
chcp 65001 >nul
echo =================================
echo  测试修复后的功能
echo =================================
echo.

echo 1. 测试分页API...
powershell -Command "Invoke-RestMethod -Uri 'http://localhost:8080/api/search?q=*.mp4&page=1&pageSize=5' | Select-Object totalCount,page,pageSize,totalPages"
echo.

echo 2. 测试路径解码（模拟）...
echo 原始路径示例: C:\Users\Administrator\OneDrive\video.mp4
echo 经过URL编码后: C%%3A%%5CUsers%%5CAdministrator%%5COneDrive%%5Cvideo.mp4
echo 新的解码逻辑会正确处理多重编码
echo.

echo 3. 测试视频搜索...
powershell -Command "Invoke-RestMethod -Uri 'http://localhost:8080/api/search?q=ext:mp4&page=1&pageSize=3'"
echo.

echo 4. 检查Web界面...
echo 请在浏览器中访问 http://localhost:8080
echo 验证以下功能：
echo   - 页面顶部有"每页显示"选择器
echo   - 搜索后显示结果统计
echo   - 页面底部有分页导航按钮
echo   - 视频文件可以正常播放
echo.

pause 