@echo off
chcp 65001 >nul
echo ========================================
echo           Git 同步脚本
echo ========================================
echo.

:: 检查是否在Git仓库中
if not exist ".git" (
    echo ❌ 错误：当前目录不是Git仓库
    echo 请确保在Git仓库根目录中运行此脚本
    pause
    exit /b 1
)

:: 显示当前状态
echo 📊 当前Git状态：
git status --short
echo.

:: 询问是否添加所有更改
set /p addAll="是否添加所有更改到暂存区？(y/n): "
if /i "%addAll%"=="y" (
    echo 📝 添加所有更改...
    git add .
    echo ✅ 已添加所有更改
) else (
    echo ⏭️ 跳过添加更改
)
echo.

:: 检查是否有待提交的更改
git diff --cached --quiet
if %errorlevel% equ 0 (
    echo ℹ️ 没有待提交的更改
    goto :pull
)

:: 询问提交信息
set /p commitMsg="请输入提交信息 (留空使用默认信息): "
if "%commitMsg%"=="" (
    set commitMsg=Update: %date% %time%
)
echo 📝 提交更改: %commitMsg%
git commit -m "%commitMsg%"
echo ✅ 提交完成
echo.

:pull
:: 拉取远程更改
echo 🔄 拉取远程更改...
git pull origin main
if %errorlevel% neq 0 (
    echo ❌ 拉取失败，尝试合并不相关历史...
    git pull origin main --allow-unrelated-histories
    if %errorlevel% neq 0 (
        echo ❌ 拉取失败，请手动解决冲突
        pause
        exit /b 1
    )
)
echo ✅ 拉取完成
echo.

:: 推送更改
echo 🚀 推送更改到远程仓库...
git push origin main
if %errorlevel% neq 0 (
    echo ❌ 推送失败
    pause
    exit /b 1
)
echo ✅ 推送完成
echo.

:: 显示最终状态
echo 📊 最终状态：
git status --short
echo.

echo ========================================
echo ✅ Git同步完成！
echo ========================================
echo.
echo 🌐 仓库地址: https://github.com/onlyclxy/Everything_Web
echo.

pause
