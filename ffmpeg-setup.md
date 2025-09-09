# FFmpeg 安装配置说明

## 下载FFmpeg

1. **官方下载**：
   - 访问：https://ffmpeg.org/download.html
   - 选择Windows版本
   - 推荐下载：ffmpeg-master-latest-win64-gpl.zip

2. **快速安装**：
   - 解压到 `C:\ffmpeg`
   - 将 `C:\ffmpeg\bin` 添加到系统PATH环境变量
   - 或者直接把 `ffmpeg.exe` 复制到项目目录

## 测试FFmpeg

打开命令行测试：
```cmd
ffmpeg -version
```

如果显示版本信息，说明安装成功。

## Everything Web Server FFmpeg功能

启动服务器后，服务器会自动检测FFmpeg：

- ✅ **FFmpeg可用**：AVI等格式会自动转码播放
- ❌ **FFmpeg不可用**：AVI等格式显示下载提示

## 转码功能特性

- **实时转码**：无需预处理，点击即播
- **流式播放**：支持拖拽进度条
- **格式支持**：AVI → MP4 实时转换
- **质量优化**：自动选择最优编码参数
- **缓冲控制**：智能码率控制，减少卡顿

## 使用体验

1. **AVI文件**：
   - 有FFmpeg：显示"🔄 ffmpeg转码播放"，实时转码
   - 无FFmpeg：显示"⚠️ 兼容性限制"，建议下载

2. **其他格式**：
   - MP4、WebM等：直接播放
   - 播放失败：自动显示兼容性提示

## 性能说明

- **首次播放**：需要等待转码启动（2-5秒）
- **转码速度**：取决于文件大小和电脑性能
- **内存使用**：转码过程会占用额外内存
- **CPU使用**：转码期间CPU使用率较高 