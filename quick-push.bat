@echo off
chcp 65001 >nul
echo 🚀 快速Git推送脚本
echo ========================================

:: 添加所有更改
echo 📝 添加所有更改...
git add .

:: 提交更改
echo 📝 提交更改...
git commit -m "Update: %date% %time%"

:: 拉取远程更改
echo 🔄 拉取远程更改...
git pull origin main --allow-unrelated-histories

:: 推送更改
echo 🚀 推送更改...
git push origin main

echo ✅ 完成！
pause
