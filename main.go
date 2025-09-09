package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type SearchResult struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Type     string `json:"type"`
	IsDir    bool   `json:"isDir"`
}

type SearchResponse struct {
	Results    []SearchResult `json:"results"`
	Count      int            `json:"count"`
	TotalCount int            `json:"totalCount"`
	Query      string         `json:"query"`
	Page       int            `json:"page"`
	PageSize   int            `json:"pageSize"`
	TotalPages int            `json:"totalPages"`
}

type BrowseResponse struct {
	Results     []SearchResult `json:"results"`
	Count       int            `json:"count"`
	CurrentPath string         `json:"currentPath"`
	ParentPath  string         `json:"parentPath"`
	PathParts   []PathPart     `json:"pathParts"`
	CanGoUp     bool           `json:"canGoUp"`
}

type PathPart struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// 搜索缓存结构
type SearchCache struct {
	Paths     []string
	Timestamp time.Time
}

// 全局搜索缓存
var (
	searchCache     = make(map[string]*SearchCache)
	cacheMutex      sync.RWMutex
	cacheExpiry     = 10 * time.Minute // 缓存10分钟过期
	ffmpegAvailable = false            // ffmpeg是否可用
)

const (
	DefaultPageSize = 50  // 默认每页显示50条结果
	MaxPageSize     = 200 // 最大每页显示200条结果
)

// Everything SDK Windows API 定义
var (
	everythingDLL                   *syscall.LazyDLL
	everythingSetSearch             *syscall.LazyProc
	everythingQuery                 *syscall.LazyProc
	everythingGetNumResults         *syscall.LazyProc
	everythingGetResultFullPath     *syscall.LazyProc
	everythingGetResultSize         *syscall.LazyProc
	everythingGetResultDateModified *syscall.LazyProc
	everythingIsFolder              *syscall.LazyProc
	everythingReset                 *syscall.LazyProc
	everythingSetMax                *syscall.LazyProc
	everythingSetOffset             *syscall.LazyProc
	everythingGetLastError          *syscall.LazyProc
	everythingInitialized           = false
)

// 初始化Everything SDK
func initEverythingSDK() error {
	if everythingInitialized {
		return nil
	}

	// 尝试不同的DLL位置
	dllPaths := []string{
		"Everything64.dll", // 当前目录
		"C:\\Program Files\\Everything\\Everything64.dll",       // 标准安装位置
		"C:\\Program Files (x86)\\Everything\\Everything64.dll", // x86安装位置
		"Everything.exe", // 如果有Everything.exe，尝试同目录的DLL
	}

	var lastErr error
	for _, path := range dllPaths {
		if path == "Everything.exe" {
			// 检查Everything进程是否在运行，获取其路径
			continue // 暂时跳过进程检测
		}

		if _, err := os.Stat(path); err == nil {
			log.Printf("找到Everything DLL: %s", path)
			everythingDLL = syscall.NewLazyDLL(path)

			// 测试加载
			if err := everythingDLL.Load(); err != nil {
				lastErr = err
				log.Printf("无法加载 %s: %v", path, err)
				continue
			}

			// 初始化所有函数指针
			everythingSetSearch = everythingDLL.NewProc("Everything_SetSearchW")
			everythingQuery = everythingDLL.NewProc("Everything_QueryW")
			everythingGetNumResults = everythingDLL.NewProc("Everything_GetNumResults")
			everythingGetResultFullPath = everythingDLL.NewProc("Everything_GetResultFullPathNameW")
			everythingGetResultSize = everythingDLL.NewProc("Everything_GetResultSize")
			everythingGetResultDateModified = everythingDLL.NewProc("Everything_GetResultDateModified")
			everythingIsFolder = everythingDLL.NewProc("Everything_IsFolderResult")
			everythingReset = everythingDLL.NewProc("Everything_Reset")
			everythingSetMax = everythingDLL.NewProc("Everything_SetMax")
			everythingSetOffset = everythingDLL.NewProc("Everything_SetOffset")
			everythingGetLastError = everythingDLL.NewProc("Everything_GetLastError")

			everythingInitialized = true
			log.Printf("Everything SDK初始化成功，使用: %s", path)
			return nil
		}
	}

	return fmt.Errorf("无法找到Everything64.dll，请确保Everything已安装。最后错误: %v", lastErr)
}

// Everything SDK 错误码
const (
	EVERYTHING_OK                    = 0
	EVERYTHING_ERROR_MEMORY          = 1
	EVERYTHING_ERROR_IPC             = 2
	EVERYTHING_ERROR_REGISTERCLASSEX = 3
	EVERYTHING_ERROR_CREATEWINDOW    = 4
	EVERYTHING_ERROR_CREATETHREAD    = 5
	EVERYTHING_ERROR_INVALIDINDEX    = 6
	EVERYTHING_ERROR_INVALIDCALL     = 7
)

// 使用Everything SDK搜索文件
func searchWithEverythingSDK(query string) ([]string, error) {
	log.Printf("使用Everything SDK搜索: %s", query)

	// 初始化Everything SDK
	if err := initEverythingSDK(); err != nil {
		return nil, err
	}

	// 重置搜索
	everythingReset.Call()

	// 设置搜索字符串（UTF-16）
	searchPtr, _ := syscall.UTF16PtrFromString(query)
	everythingSetSearch.Call(uintptr(unsafe.Pointer(searchPtr)))

	// 执行查询
	ret, _, _ := everythingQuery.Call(1) // TRUE for wait
	if ret == 0 {
		// 获取错误码
		errorCode, _, _ := everythingGetLastError.Call()
		return nil, fmt.Errorf("Everything查询失败，错误码: %d", errorCode)
	}

	// 获取结果数量
	numResults, _, _ := everythingGetNumResults.Call()
	log.Printf("Everything找到%d个结果", numResults)

	if numResults == 0 {
		return []string{}, nil
	}

	// 获取所有结果
	var paths []string
	for i := uintptr(0); i < numResults; i++ {
		// 获取文件路径
		pathBuffer := make([]uint16, 4096)
		everythingGetResultFullPath.Call(
			i,
			uintptr(unsafe.Pointer(&pathBuffer[0])),
			uintptr(len(pathBuffer)),
		)
		path := syscall.UTF16ToString(pathBuffer)
		if path != "" {
			paths = append(paths, path)
		}
	}

	log.Printf("Everything SDK返回%d个有效路径", len(paths))
	return paths, nil
}

// 回退方案：使用es.exe搜索文件（保留用于Everything SDK不可用时）
func searchWithESExe(query string) ([]string, error) {
	log.Printf("使用es.exe回退搜索: %s", query)

	cmd := exec.Command("./es.exe", query)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("执行es.exe失败: %v", err)
	}

	lines := strings.Split(string(output), "\n")
	var paths []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}

	log.Printf("es.exe返回%d个有效路径", len(paths))
	return paths, nil
}

// 获取本机所有IP地址
func getLocalIPs() []string {
	var ips []string

	interfaces, err := net.Interfaces()
	if err != nil {
		log.Printf("获取网络接口失败: %v", err)
		return ips
	}

	for _, iface := range interfaces {
		// 跳过虚拟网卡和未激活的接口
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			// 只获取IPv4地址，排除环回地址
			if ip == nil || ip.IsLoopback() {
				continue
			}

			if ip.To4() != nil {
				ips = append(ips, ip.String())
			}
		}
	}

	return ips
}

func main() {
	// 设置日志格式
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("正在启动Everything Web Server...")

	// 检测ffmpeg是否可用
	checkFFmpegAvailability()

	// 启动缓存清理协程
	go func() {
		ticker := time.NewTicker(5 * time.Minute) // 每5分钟清理一次
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cleanExpiredCache()
			}
		}
	}()

	// 设置静态文件服务
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/search", searchHandler)
	http.HandleFunc("/file/", fileHandler)
	http.HandleFunc("/stream/", streamHandler)
	http.HandleFunc("/transcode/", transcodeHandler)
	http.HandleFunc("/thumbnail/", thumbnailHandler)
	http.HandleFunc("/api/search", apiSearchHandler)
	http.HandleFunc("/api/browse", apiBrowseHandler)
	http.HandleFunc("/api/text", textPreviewHandler)
	http.HandleFunc("/api/cache-status", cacheStatusHandler)
	http.HandleFunc("/api/cache-clear", cacheClearHandler)
	http.HandleFunc("/video/", videoPlayerHandler)
	http.HandleFunc("/imageview/", imageViewerHandler)
	http.HandleFunc("/textview/", textViewerHandler)

	// 启动服务器
	port := "8080"

	// 获取本机IP地址
	localIPs := getLocalIPs()

	log.Printf("服务器启动在端口: %s", port)
	fmt.Printf("🚀 Everything Web Server 已启动！\n")
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("📍 访问地址：\n")
	fmt.Printf("   本地访问: http://127.0.0.1:%s\n", port)
	fmt.Printf("   本地访问: http://localhost:%s\n", port)

	for _, ip := range localIPs {
		fmt.Printf("   局域网访问: http://%s:%s\n", ip, port)
	}

	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("💡 如果局域网无法访问，请检查Windows防火墙设置\n")
	fmt.Printf("🔧 运行 'netsh advfirewall firewall add rule name=\"Everything Web Server\" dir=in action=allow protocol=TCP localport=%s' 添加防火墙规则\n", port)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// 首页处理器
func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	log.Printf("访问首页，来源IP: %s", r.RemoteAddr)

	tmpl := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Everything Web Server</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; background: #f5f5f5; }
        .container { max-width: 1200px; margin: 0 auto; padding: 20px; }
        .header { background: white; padding: 20px; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); margin-bottom: 20px; }
        .logo-container { cursor: pointer; text-align: center; margin-bottom: 20px; transition: transform 0.2s ease; }
        .logo-container:hover { transform: translateY(-2px); }
        .logo { 
            font-size: 40px; 
            font-weight: 700; 
            background: linear-gradient(135deg, #4CAF50, #2196F3, #9C27B0); 
            -webkit-background-clip: text; 
            -webkit-text-fill-color: transparent; 
            background-clip: text;
            margin: 0;
            padding: 15px 0;
            letter-spacing: 3px;
        }
        .mode-indicator { 
            font-size: 14px; 
            color: #666; 
            margin-top: -10px; 
            font-weight: 400; 
            text-align: center; 
            transition: color 0.3s ease; 
        }
        .mode-indicator.browse-mode { 
            color: #2196F3; 
            font-weight: 500; 
        }
        .search-box { display: flex; gap: 10px; margin-bottom: 20px; }
        .search-input { flex: 1; padding: 12px; border: 2px solid #ddd; border-radius: 6px; font-size: 16px; }
        .search-input:focus { outline: none; border-color: #4CAF50; }
        .search-btn { padding: 12px 24px; background: #4CAF50; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 16px; }
        .search-btn:hover { background: #45a049; }
        .path-bar { margin-top: 15px; }
        .path-input-container { display: flex; gap: 10px; align-items: center; }
        .path-label { font-weight: 500; color: #666; min-width: 50px; }
        .path-input { flex: 1; padding: 12px; border: 2px solid #ddd; border-radius: 6px; font-size: 16px; }
        .path-input:focus { outline: none; border-color: #4CAF50; }
        .path-btn { padding: 12px 20px; background: #4CAF50; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 16px; }
        .path-btn:hover { background: #45a049; }
        .path-btn-secondary { padding: 12px 20px; background: #666; color: white; border: none; border-radius: 6px; cursor: pointer; font-size: 16px; }
        .path-btn-secondary:hover { background: #555; }
        .search-options { display: flex; gap: 20px; align-items: center; margin-bottom: 10px; }
        .search-options label { font-size: 14px; color: #666; }
        .search-options select, .search-options input { padding: 5px; border: 1px solid #ddd; border-radius: 4px; }
        .breadcrumb { margin-bottom: 20px; padding: 10px; background: white; border-radius: 6px; }
        .breadcrumb a { color: #4CAF50; text-decoration: none; margin-right: 5px; }
        .breadcrumb a:hover { text-decoration: underline; }
        .results { background: white; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
        .result-item { display: flex; align-items: center; padding: 15px; border-bottom: 1px solid #eee; transition: background 0.2s; }
        .result-item:hover { background: #f9f9f9; }
        .result-item:last-child { border-bottom: none; }
        .file-icon { width: 40px; height: 40px; margin-right: 15px; background: #4CAF50; border-radius: 4px; display: flex; align-items: center; justify-content: center; color: white; font-weight: bold; }
        .file-icon.video { background: #FF5722; }
        .file-icon.image { background: #2196F3; }
        .file-icon.folder { background: #FFC107; color: #333; }
        .file-info { flex: 1; }
        .file-name { font-weight: 500; color: #333; margin-bottom: 5px; cursor: pointer; }
        .file-name:hover { color: #4CAF50; }
        .file-meta { font-size: 14px; color: #666; }
        .file-actions { display: flex; gap: 10px; }
        .btn { padding: 6px 12px; border: none; border-radius: 4px; cursor: pointer; font-size: 14px; text-decoration: none; display: inline-block; }
        .btn-primary { background: #4CAF50; color: white; }
        .btn-secondary { background: #ddd; color: #333; }
        .btn-info { background: #2196F3; color: white; }
        .btn:hover { opacity: 0.8; }
        .loading { text-align: center; padding: 40px; color: #666; }
        .no-results { text-align: center; padding: 40px; color: #666; }
        .thumbnail { width: 60px; height: 60px; object-fit: cover; border-radius: 4px; margin-right: 15px; }
        .pagination { text-align: center; padding: 20px; }
        .pagination button { margin: 0 5px; padding: 8px 12px; border: 1px solid #ddd; background: white; cursor: pointer; border-radius: 4px; }
        .pagination button.active { background: #4CAF50; color: white; border-color: #4CAF50; }
        .pagination button:hover:not(.active) { background: #f5f5f5; }
        .pagination button:disabled { opacity: 0.5; cursor: not-allowed; }
        .search-stats { text-align: center; padding: 10px; color: #666; background: #f9f9f9; margin-bottom: 10px; }
        .cache-info { text-align: center; padding: 8px; background: #e3f2fd; color: #1976d2; font-size: 12px; margin-bottom: 10px; border-radius: 4px; }
        .cache-info.cached { background: #e8f5e8; color: #2e7d32; }
        .image-overlay { position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.9); z-index: 1000; display: none; justify-content: center; align-items: center; cursor: pointer; }
        .image-preview { max-width: 90%; max-height: 90%; border-radius: 8px; box-shadow: 0 4px 20px rgba(0,0,0,0.5); }
        .image-overlay .close-btn { position: absolute; top: 20px; right: 20px; color: white; font-size: 30px; cursor: pointer; background: rgba(0,0,0,0.5); width: 40px; height: 40px; border-radius: 50%; display: flex; align-items: center; justify-content: center; }
        .image-overlay .close-btn:hover { background: rgba(0,0,0,0.8); }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <div class="logo-container" onclick="resetSearch()">
                <h1 class="logo">Everything Web Server</h1>
                <div class="mode-indicator" id="modeIndicator">🔍 搜索模式</div>
            </div>
            <div class="search-options">
                <label>每页显示：
                    <select id="pageSize">
                        <option value="20">20条</option>
                        <option value="50" selected>50条</option>
                        <option value="100">100条</option>
                        <option value="200">200条</option>
                    </select>
                </label>
            </div>
            <div class="search-box">
                <input type="text" class="search-input" id="searchInput" placeholder="搜索文件和文件夹..." autocomplete="off">
                <button class="search-btn" onclick="performSearch()">搜索</button>
            </div>
            
            <!-- 路径栏 -->
            <div class="path-bar" id="pathBar" style="display: none;">
                <div class="path-input-container">
                    <span class="path-label">📂 路径:</span>
                    <input type="text" class="path-input" id="pathInput" placeholder="输入文件夹路径，如: C:\Users" autocomplete="off">
                    <button class="path-btn" onclick="navigateToPath()">进入</button>
                    <button class="path-btn-secondary" onclick="togglePathBar()">取消</button>
                </div>
            </div>
        </div>
        
        <div class="breadcrumb" id="breadcrumb" style="display: none;"></div>
        
        <div class="cache-info" id="cacheInfo" style="display: none;"></div>
        
        <div class="search-stats" id="searchStats" style="display: none;"></div>
        
        <div class="results" id="results">
            <div class="no-results">输入关键词开始搜索</div>
        </div>
        
        <div class="pagination" id="pagination" style="display: none;"></div>
    </div>
    
    <!-- 图片预览覆盖层 -->
    <div class="image-overlay" id="imageOverlay" onclick="closeImagePreview()">
        <div class="close-btn" onclick="closeImagePreview()">×</div>
        <img class="image-preview" id="imagePreview" onclick="event.stopPropagation()">
    </div>

    <script>
        let currentPage = 1;
        let currentQuery = '';
        let totalPages = 1;
        let currentMode = 'search'; // 'search' 或 'browse'
        let currentPath = '';
        let browseHistory = []; // 浏览历史
        
        document.getElementById('searchInput').addEventListener('keypress', function(e) {
            if (e.key === 'Enter') {
                performSearch();
            }
        });
        
        // 为搜索框添加点击时的智能行为
        document.getElementById('searchInput').addEventListener('focus', function() {
            if (currentMode === 'browse') {
                // 如果当前在浏览模式，提示用户可以搜索
                if (this.value === '') {
                    this.placeholder = '输入关键词搜索，或按Esc返回浏览...';
                }
            }
        });
        
        document.getElementById('searchInput').addEventListener('blur', function() {
            // 恢复默认占位符
            this.placeholder = '搜索文件和文件夹...';
        });
        
        document.getElementById('searchInput').addEventListener('keydown', function(e) {
            if (e.key === 'Escape' && currentMode === 'browse') {
                // 按Esc键时，如果在浏览模式且搜索框为空，则保持浏览模式
                if (this.value === '') {
                    this.blur();
                }
            }
        });
        
        async function performSearch(page = 1) {
            const searchInput = document.getElementById('searchInput');
            const pageSizeSelect = document.getElementById('pageSize');
            const resultsContainer = document.getElementById('results');
            const searchStats = document.getElementById('searchStats');
            const cacheInfo = document.getElementById('cacheInfo');
            const pagination = document.getElementById('pagination');
            
            // 检查DOM元素是否存在
            if (!searchInput || !pageSizeSelect || !resultsContainer) {
                console.error('必要的DOM元素不存在');
                return;
            }
            
            const query = searchInput.value;
            const pageSize = pageSizeSelect.value;
            
            if (!query.trim()) return;
            
            // 切换到搜索模式
            currentMode = 'search';
            currentQuery = query;
            currentPage = page;
            currentPath = '';
            
            // 更新模式指示器
            updateModeIndicator();
            
            // 隐藏面包屑导航
            const breadcrumbContainer = document.getElementById('breadcrumb');
            if (breadcrumbContainer) breadcrumbContainer.style.display = 'none';
            
            resultsContainer.innerHTML = '<div class="loading">搜索中...</div>';
            if (searchStats) searchStats.style.display = 'none';
            if (cacheInfo) cacheInfo.style.display = 'none';
            if (pagination) pagination.style.display = 'none';
            
            const startTime = Date.now();
            
            try {
                const response = await fetch('/api/search?q=' + encodeURIComponent(query) + '&page=' + page + '&pageSize=' + pageSize);
                
                if (!response.ok) {
                    throw new Error('搜索请求失败: ' + response.status);
                }
                
                const data = await response.json();
                
                // 检查API返回的数据格式
                if (!data) {
                    throw new Error('服务器返回空数据');
                }
                
                const endTime = Date.now();
                const responseTime = endTime - startTime;
                
                displayResults(data, responseTime);
            } catch (error) {
                console.error('搜索错误:', error);
                resultsContainer.innerHTML = '<div class="no-results">搜索出错: ' + error.message + '</div>';
                if (searchStats) searchStats.style.display = 'none';
                if (cacheInfo) cacheInfo.style.display = 'none';
                if (pagination) pagination.style.display = 'none';
            }
        }
        
        function displayResults(data, responseTime) {
            const container = document.getElementById('results');
            const statsContainer = document.getElementById('searchStats');
            const cacheContainer = document.getElementById('cacheInfo');
            const paginationContainer = document.getElementById('pagination');
            
            // 检查DOM元素是否存在
            if (!container || !statsContainer || !cacheContainer || !paginationContainer) {
                console.error('页面DOM元素缺失');
                return;
            }
            
            // 检查data和data.results是否存在
            if (!data || !data.results || data.results.length === 0) {
                container.innerHTML = '<div class="no-results">没有找到匹配的文件</div>';
                statsContainer.style.display = 'none';
                cacheContainer.style.display = 'none';
                paginationContainer.style.display = 'none';
                return;
            }
            
            // 显示缓存信息
            if (responseTime > 5000) {
                cacheContainer.innerHTML = '⏱️ 首次搜索完成 (' + (responseTime/1000).toFixed(1) + '秒)，结果已缓存，翻页将瞬间响应';
                cacheContainer.className = 'cache-info';
            } else {
                cacheContainer.innerHTML = '⚡ 从缓存读取 (' + responseTime + 'ms)，翻页体验已优化！';
                cacheContainer.className = 'cache-info cached';
            }
            cacheContainer.style.display = 'block';
            
            // 显示搜索统计
            const totalCount = data.totalCount || 0;
            const currentPage = data.page || 1;
            const totalPages = data.totalPages || 1;
            
            statsContainer.innerHTML = '找到 <strong>' + totalCount + '</strong> 个结果，当前显示第 <strong>' + currentPage + '</strong> 页，共 <strong>' + totalPages + '</strong> 页';
            statsContainer.style.display = 'block';
            
            // 显示结果
            let html = '';
            data.results.forEach(file => {
                // 检查file对象是否完整
                if (!file || !file.path) {
                    return; // 跳过无效的file对象
                }
                
                const icon = getFileIcon(file);
                const size = formatFileSize(file.size || 0);
                const actions = getFileActions(file);
                const fileName = file.name || '未知文件';
                const fileType = file.type || 'file';
                
                html += '<div class="result-item">';
                html += icon;
                html += '<div class="file-info">';
                html += '<div class="file-name" onclick="handleFileClick(\'' + file.path.replace(/'/g, "\\'").replace(/\\/g, "\\\\") + '\', \'' + fileType + '\', \'' + fileName.replace(/'/g, "\\'") + '\')">' + fileName + '</div>';
                html += '<div class="file-meta">' + file.path + ' • ' + size + ' • ' + (file.modified || '') + '</div>';
                html += '</div>';
                html += '<div class="file-actions">';
                html += actions;
                html += '</div>';
                html += '</div>';
            });
            
            container.innerHTML = html;
            
            // 显示分页
            displayPagination(data);
        }
        
        function displayPagination(data) {
            const container = document.getElementById('pagination');
            
            // 检查DOM元素是否存在
            if (!container) {
                console.error('分页容器DOM元素不存在');
                return;
            }
            
            // 检查data对象是否存在
            if (!data || !data.totalPages) {
                container.style.display = 'none';
                return;
            }
            
            totalPages = data.totalPages;
            
            if (totalPages <= 1) {
                container.style.display = 'none';
                return;
            }
            
            let html = '';
            
            // 上一页按钮
            html += '<button onclick="performSearch(' + (currentPage - 1) + ')" ' + (currentPage <= 1 ? 'disabled' : '') + '>上一页</button>';
            
            // 页码按钮
            const startPage = Math.max(1, currentPage - 2);
            const endPage = Math.min(totalPages, currentPage + 2);
            
            if (startPage > 1) {
                html += '<button onclick="performSearch(1)">1</button>';
                if (startPage > 2) {
                    html += '<span>...</span>';
                }
            }
            
            for (let i = startPage; i <= endPage; i++) {
                html += '<button onclick="performSearch(' + i + ')" ' + (i === currentPage ? 'class="active"' : '') + '>' + i + '</button>';
            }
            
            if (endPage < totalPages) {
                if (endPage < totalPages - 1) {
                    html += '<span>...</span>';
                }
                html += '<button onclick="performSearch(' + totalPages + ')">' + totalPages + '</button>';
            }
            
            // 下一页按钮
            html += '<button onclick="performSearch(' + (currentPage + 1) + ')" ' + (currentPage >= totalPages ? 'disabled' : '') + '>下一页</button>';
            
            container.innerHTML = html;
            container.style.display = 'block';
        }
        
        function getFileIcon(file) {
            if (file.isDir) {
                return '<div class="file-icon folder">📁</div>';
            }
            
            // 检查file.name是否存在
            if (!file.name) {
                return '<div class="file-icon">📄</div>';
            }
            
            const ext = file.name.toLowerCase().split('.').pop();
            if (['mp4', 'mkv', 'avi', 'mov', 'wmv', 'flv', 'webm'].includes(ext)) {
                return '<div class="file-icon video">🎬</div>';
            }
            if (['jpg', 'jpeg', 'png', 'gif', 'bmp', 'webp'].includes(ext)) {
                return '<img src="/thumbnail/' + encodeURIComponent(file.path) + '" class="thumbnail" onerror="this.style.display=\'none\'; this.nextElementSibling.style.display=\'flex\'"><div class="file-icon image" style="display:none">🖼️</div>';
            }
            return '<div class="file-icon">📄</div>';
        }
        
        function getFileActions(file) {
            if (file.isDir) {
                return '<a href="#" class="btn btn-primary" onclick="browseFolder(\'' + file.path.replace(/'/g, "\\'").replace(/\\/g, "\\\\") + '\')">打开</a>';
            }
            
            // 检查file.name是否存在
            if (!file.name) {
                return '<a href="/file/' + encodeURIComponent(file.path) + '?download=1" class="btn btn-secondary" download>下载</a>';
            }
            
            const ext = file.name.toLowerCase().split('.').pop();
            let actions = '<a href="/file/' + encodeURIComponent(file.path) + '?download=1" class="btn btn-secondary" download>下载</a>';
            
            // 视频文件
            if (['mp4', 'mkv', 'avi', 'mov', 'wmv', 'flv', 'webm'].includes(ext)) {
                actions = '<a href="/video/' + encodeURIComponent(file.path) + '" class="btn btn-primary" target="_blank">播放</a> ' + actions;
            }
            // 图片文件
            else if (['jpg', 'jpeg', 'png', 'gif', 'bmp', 'webp'].includes(ext)) {
                let encodedPath = encodeURIComponent(file.path)
                    .replace(/'/g, '%27').replace(/\(/g, '%28').replace(/\)/g, '%29')
                    .replace(/%5C/g, '%5C'); // 确保反斜杠被编码
                actions = '<button class="btn btn-primary" onclick="showImagePreview(\'' + file.path.replace(/'/g, "\\'").replace(/\\/g, "\\\\") + '\')">预览</button> <a href="/imageview/' + encodedPath + '" class="btn btn-info" target="_blank">新窗口</a> ' + actions;
            }
            // 文本文件
            else if (isTextFile(ext)) {
                let encodedPath = encodeURIComponent(file.path)
                    .replace(/'/g, '%27').replace(/\(/g, '%28').replace(/\)/g, '%29')
                    .replace(/%5C/g, '%5C'); // 确保反斜杠被编码
                actions = '<button class="btn btn-primary" onclick="showTextPreview(\'' + file.path.replace(/'/g, "\\'").replace(/\\/g, "\\\\") + '\')">预览</button> <a href="/textview/' + encodedPath + '" class="btn btn-info" target="_blank">新窗口</a> ' + actions;
            }
            
            return actions;
        }
        
        // 检查是否为文本文件
        function isTextFile(ext) {
            const textExts = [
                // 基本文本文件
                'txt', 'log', 'md', 'readme', 'conf', 'config', 'ini', 'cfg',
                // 编程语言文件
                'c', 'cpp', 'cc', 'cxx', 'h', 'hpp', 'hxx', 'cs', 'vb', 'fs',
                'java', 'kt', 'scala', 'groovy', 'js', 'ts', 'jsx', 'tsx', 'mjs', 'cjs',
                'py', 'pyw', 'pyi', 'pyx', 'pxd', 'rb', 'rake', 'php', 'phtml',
                'go', 'mod', 'sum', 'rs', 'toml', 'swift', 'm', 'mm', 'lua',
                'pl', 'pm', 't', 'sh', 'bash', 'zsh', 'fish', 'bat', 'cmd', 'ps1',
                'r', 'rmd', 'matlab',
                // 标记语言和数据格式
                'html', 'htm', 'xhtml', 'xml', 'xsl', 'xsd', 'json', 'jsonc',
                'yaml', 'yml', 'css', 'scss', 'sass', 'less', 'styl',
                'sql', 'mysql', 'psql', 'sqlite',
                // 配置和脚本文件
                'dockerfile', 'dockerignore', 'gitignore', 'gitattributes',
                'makefile', 'mk', 'cmake', 'ninja', 'gradle', 'maven', 'pom', 'ant',
                'properties', 'env', 'htaccess',
                // 其他常见文本格式
                'csv', 'tsv', 'sv', 'tex', 'bib', 'vim', 'vimrc', 'emacs',
                'reg', 'inf', 'desktop'
            ];
            
            return textExts.includes(ext);
        }
        
        function formatFileSize(bytes) {
            if (bytes === 0) return '0 B';
            const k = 1024;
            const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
            const i = Math.floor(Math.log(bytes) / Math.log(k));
            return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
        }
        
        function handleFileClick(path, type, name) {
            console.log('点击文件:', path, type, name);
            
            if (type === 'folder') {
                browseFolder(path);
            } else if (type === 'video') {
                window.open('/video/' + encodeURIComponent(path), '_blank');
            } else if (type === 'image') {
                showImagePreview(path);
            } else {
                // 检查是否为文本文件
                const ext = name.toLowerCase().split('.').pop();
                if (isTextFile(ext)) {
                    showTextPreview(path);
                } else {
                    // 其他文件类型，在新窗口中打开
                    window.open('/file/' + encodeURIComponent(path), '_blank');
                }
            }
        }
        
        function showImagePreview(path) {
            const overlay = document.getElementById('imageOverlay');
            const preview = document.getElementById('imagePreview');
            
            preview.src = '/file/' + encodeURIComponent(path);
            overlay.style.display = 'flex';
            
            // 添加ESC键关闭功能
            document.addEventListener('keydown', function escHandler(e) {
                if (e.key === 'Escape') {
                    closeImagePreview();
                    document.removeEventListener('keydown', escHandler);
                }
            });
        }
        
        function closeImagePreview() {
            document.getElementById('imageOverlay').style.display = 'none';
        }
        
        // 文本预览功能
        async function showTextPreview(path) {
            console.log('文本预览请求:', path);
            
            try {
                const response = await fetch('/api/text?path=' + encodeURIComponent(path));
                
                if (!response.ok) {
                    throw new Error('文本预览请求失败: ' + response.status);
                }
                
                const data = await response.json();
                displayTextPreview(data);
            } catch (error) {
                console.error('文本预览错误:', error);
                alert('文本预览失败: ' + error.message);
            }
        }
        
        // 显示文本预览弹窗
        function displayTextPreview(data) {
            // 创建预览弹窗
            const overlay = document.createElement('div');
            overlay.id = 'textPreviewOverlay';
            overlay.style.cssText = 'position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.9); z-index: 2000; display: flex; justify-content: center; align-items: center; cursor: pointer;';
            
            const previewContainer = document.createElement('div');
            previewContainer.style.cssText = 'background: #1e1e1e; border-radius: 8px; max-width: 90%; max-height: 90%; display: flex; flex-direction: column; overflow: hidden; cursor: default;';
            
            // 预览内容截取（显示前500行）
            const lines = data.content.split('\n');
            const previewLines = lines.slice(0, 500);
            const isLongFile = lines.length > 500;
            const previewContent = previewLines.join('\n');
            
            previewContainer.innerHTML = '<div style="padding: 20px; border-bottom: 1px solid #333; color: white;">' +
                '<div style="display: flex; justify-content: space-between; align-items: center;">' +
                    '<div>' +
                        '<h3 style="color: #4FC3F7; margin: 0 0 5px 0;">' + data.name + '</h3>' +
                        '<div style="font-size: 12px; color: #888;">' +
                            '大小: ' + formatFileSize(data.size) + ' • ' +
                            '行数: ' + data.lines + ' • ' +
                            '编码: ' + data.encoding +
                            (isLongFile ? ' • 预览前500行' : '') +
                        '</div>' +
                    '</div>' +
                    '<div>' +
                        '<button onclick="openTextInNewWindow(\'' + data.path.replace(/\\/g, '\\\\').replace(/'/g, "\\'") + '\')" ' +
                                'style="padding: 8px 16px; background: #2196F3; color: white; border: none; border-radius: 4px; cursor: pointer; margin-right: 10px;">' +
                            '新窗口' +
                        '</button>' +
                        '<button onclick="closeTextPreview()" ' +
                                'style="padding: 8px 16px; background: #666; color: white; border: none; border-radius: 4px; cursor: pointer;">' +
                            '关闭' +
                        '</button>' +
                    '</div>' +
                '</div>' +
            '</div>' +
            '<div style="flex: 1; overflow: auto; padding: 20px; white-space: pre-wrap; font-family: monospace; font-size: 13px; color: #d4d4d4; line-height: 1.4; word-break: break-word; background: #1e1e1e;" id="previewContent">' + escapeHtml(previewContent) + '</div>' +
            (isLongFile ? '<div style="padding: 10px 20px; background: #333; color: #ccc; text-align: center; font-size: 12px;">文件较长，仅显示前500行。点击"新窗口"查看完整内容。</div>' : '');
            
            // 预览模式不需要行号，只显示内容即可
            
            overlay.appendChild(previewContainer);
            document.body.appendChild(overlay);
            
            // 点击背景关闭
            overlay.addEventListener('click', function(e) {
                if (e.target === overlay) {
                    closeTextPreview();
                }
            });
            
            // 阻止内容区域点击冒泡
            previewContainer.addEventListener('click', function(e) {
                e.stopPropagation();
            });
            
            // 添加ESC键关闭功能
            document.addEventListener('keydown', function escHandler(e) {
                if (e.key === 'Escape') {
                    closeTextPreview();
                    document.removeEventListener('keydown', escHandler);
                }
            });
        }
        
        // 关闭文本预览
        function closeTextPreview() {
            const overlay = document.getElementById('textPreviewOverlay');
            if (overlay) {
                overlay.remove();
            }
        }
        
        // 在新窗口中打开文本文件（正确处理URL编码）
        function openTextInNewWindow(filePath) {
            // 完整URL编码，包括反斜杠
            let encodedPath = encodeURIComponent(filePath);
            // 确保特殊字符都被正确编码
            encodedPath = encodedPath.replace(/'/g, '%27')
                                     .replace(/\(/g, '%28')
                                     .replace(/\)/g, '%29')
                                     .replace(/%5C/g, '%5C'); // 确保反斜杠编码
            const url = '/textview/' + encodedPath;
            console.log('打开新窗口:', url);
            window.open(url, '_blank');
        }
        
        // HTML转义函数
        function escapeHtml(text) {
            const div = document.createElement('div');
            div.textContent = text;
            return div.innerHTML;
        }
        
        function resetSearch() {
            // 获取DOM元素
            const searchInput = document.getElementById('searchInput');
            const pageSize = document.getElementById('pageSize');
            const results = document.getElementById('results');
            const searchStats = document.getElementById('searchStats');
            const cacheInfo = document.getElementById('cacheInfo');
            const pagination = document.getElementById('pagination');
            
            // 重置搜索输入框
            if (searchInput) searchInput.value = '';
            if (pageSize) pageSize.value = '50';
            
            // 清空结果显示
            if (results) results.innerHTML = '<div class="no-results">输入关键词开始搜索</div>';
            if (searchStats) searchStats.style.display = 'none';
            if (cacheInfo) cacheInfo.style.display = 'none';
            if (pagination) pagination.style.display = 'none';
            
            // 重置状态变量
            currentPage = 1;
            currentQuery = '';
            totalPages = 1;
            
            // 聚焦到搜索框
            if (searchInput) searchInput.focus();
            
            console.log('搜索已重置');
        }
        
        async function browseFolder(path) {
            console.log('浏览文件夹:', path);
            
            // 清空搜索框并切换到浏览模式
            const searchInput = document.getElementById('searchInput');
            if (searchInput) {
                searchInput.value = '';
            }
            
            currentMode = 'browse';
            currentPath = path;
            currentQuery = '';
            
            // 更新模式指示器
            updateModeIndicator();
            
            // 添加到浏览历史
            if (browseHistory.length === 0 || browseHistory[browseHistory.length - 1] !== path) {
                browseHistory.push(path);
            }
            
            const resultsContainer = document.getElementById('results');
            const searchStats = document.getElementById('searchStats');
            const cacheInfo = document.getElementById('cacheInfo');
            const pagination = document.getElementById('pagination');
            const breadcrumb = document.getElementById('breadcrumb');
            
            // 显示加载中
            if (resultsContainer) resultsContainer.innerHTML = '<div class="loading">加载文件夹内容...</div>';
            if (searchStats) searchStats.style.display = 'none';
            if (cacheInfo) cacheInfo.style.display = 'none';
            if (pagination) pagination.style.display = 'none';
            
            const startTime = Date.now();
            
            try {
                const response = await fetch('/api/browse?path=' + encodeURIComponent(path));
                
                if (!response.ok) {
                    throw new Error('浏览请求失败: ' + response.status);
                }
                
                const data = await response.json();
                const endTime = Date.now();
                const responseTime = endTime - startTime;
                
                displayBrowseResults(data, responseTime);
            } catch (error) {
                console.error('浏览错误:', error);
                if (resultsContainer) {
                    resultsContainer.innerHTML = '<div class="no-results">浏览失败: ' + error.message + '</div>';
                }
                if (searchStats) searchStats.style.display = 'none';
                if (cacheInfo) cacheInfo.style.display = 'none';
                if (pagination) pagination.style.display = 'none';
            }
        }
        
        function displayBrowseResults(data, responseTime) {
            const container = document.getElementById('results');
            const statsContainer = document.getElementById('searchStats');
            const cacheContainer = document.getElementById('cacheInfo');
            const breadcrumbContainer = document.getElementById('breadcrumb');
            const paginationContainer = document.getElementById('pagination');
            
            // 检查DOM元素是否存在
            if (!container || !statsContainer || !cacheContainer || !breadcrumbContainer) {
                console.error('页面DOM元素缺失');
                return;
            }
            
            // 显示面包屑导航
            displayBreadcrumb(data);
            
            // 显示文件夹信息
            cacheContainer.innerHTML = '📁 文件夹浏览 (' + responseTime + 'ms) - 当前位置: ' + data.currentPath;
            cacheContainer.className = 'cache-info';
            cacheContainer.style.display = 'block';
            
            // 显示文件夹统计
            statsContainer.innerHTML = '找到 <strong>' + data.count + '</strong> 个项目';
            statsContainer.style.display = 'block';
            
            // 隐藏分页（文件夹浏览不需要分页）
            if (paginationContainer) paginationContainer.style.display = 'none';
            
            // 检查data和data.results是否存在
            if (!data || !data.results || data.results.length === 0) {
                container.innerHTML = '<div class="no-results">此文件夹为空</div>';
                return;
            }
            
            // 显示结果
            let html = '';
            
            // 如果可以返回上级，添加返回上级按钮
            if (data.canGoUp && data.parentPath) {
                html += '<div class="result-item">';
                html += '<div class="file-icon folder">↩️</div>';
                html += '<div class="file-info">';
                html += '<div class="file-name" onclick="browseFolder(\'' + data.parentPath.replace(/'/g, "\\'").replace(/\\/g, "\\\\") + '\')">..</div>';
                html += '<div class="file-meta">返回上级目录</div>';
                html += '</div>';
                html += '<div class="file-actions">';
                html += '<button class="btn btn-primary" onclick="browseFolder(\'' + data.parentPath.replace(/'/g, "\\'").replace(/\\/g, "\\\\") + '\')">进入</button>';
                html += '</div>';
                html += '</div>';
            }
            
            // 先显示文件夹，再显示文件
            data.results.sort((a, b) => {
                if (a.isDir && !b.isDir) return -1;
                if (!a.isDir && b.isDir) return 1;
                return a.name.localeCompare(b.name, 'zh-CN');
            });
            
            data.results.forEach(file => {
                if (!file || !file.path) {
                    return;
                }
                
                const icon = getFileIcon(file);
                const size = formatFileSize(file.size || 0);
                const actions = getFileActions(file);
                const fileName = file.name || '未知文件';
                const fileType = file.type || 'file';
                
                html += '<div class="result-item">';
                html += icon;
                html += '<div class="file-info">';
                html += '<div class="file-name" onclick="handleFileClick(\'' + file.path.replace(/'/g, "\\'").replace(/\\/g, "\\\\") + '\', \'' + fileType + '\', \'' + fileName.replace(/'/g, "\\'") + '\')">' + fileName + '</div>';
                html += '<div class="file-meta">' + file.path + ' • ' + size + ' • ' + (file.modified || '') + '</div>';
                html += '</div>';
                html += '<div class="file-actions">';
                html += actions;
                html += '</div>';
                html += '</div>';
            });
            
            container.innerHTML = html;
        }
        
        function displayBreadcrumb(data) {
            const breadcrumbContainer = document.getElementById('breadcrumb');
            if (!breadcrumbContainer || !data.pathParts) {
                return;
            }
            
            let html = '<span style="margin-right: 10px;">📍 当前位置:</span>';
            
            data.pathParts.forEach((part, index) => {
                if (index > 0) {
                    html += ' / ';
                }
                
                // 如果是当前路径，不加链接
                if (part.path === data.currentPath) {
                    html += '<strong>' + part.name + '</strong>';
                } else {
                    html += '<a href="#" onclick="browseFolder(\'' + part.path.replace(/'/g, "\\'").replace(/\\/g, "\\\\") + '\')">' + part.name + '</a>';
                }
            });
            
            // 添加回到搜索和输入路径的按钮
            html += ' <button style="margin-left: 15px; padding: 4px 8px; background: #2196F3; color: white; border: none; border-radius: 3px; cursor: pointer; font-size: 12px;" onclick="togglePathBar()">输入路径</button>';
            html += ' <button style="margin-left: 5px; padding: 4px 8px; background: #4CAF50; color: white; border: none; border-radius: 3px; cursor: pointer; font-size: 12px;" onclick="resetToSearch()">回到搜索</button>';
            
            breadcrumbContainer.innerHTML = html;
            breadcrumbContainer.style.display = 'block';
        }
        
        function resetToSearch() {
            currentMode = 'search';
            currentPath = '';
            currentQuery = '';
            browseHistory = [];
            
            // 更新模式指示器
            updateModeIndicator();
            
            const breadcrumbContainer = document.getElementById('breadcrumb');
            const searchInput = document.getElementById('searchInput');
            
            if (breadcrumbContainer) breadcrumbContainer.style.display = 'none';
            if (searchInput) searchInput.focus();
            
            resetSearch();
        }
        
        function updateModeIndicator() {
            const indicator = document.getElementById('modeIndicator');
            if (!indicator) return;
            
            if (currentMode === 'browse') {
                indicator.textContent = '📁 浏览模式 - ' + (currentPath.length > 50 ? '...' + currentPath.slice(-50) : currentPath);
                indicator.className = 'mode-indicator browse-mode';
            } else {
                indicator.textContent = '🔍 搜索模式';
                indicator.className = 'mode-indicator';
            }
        }
        
        function togglePathBar() {
            const pathBar = document.getElementById('pathBar');
            const pathInput = document.getElementById('pathInput');
            
            if (pathBar.style.display === 'none') {
                pathBar.style.display = 'block';
                if (pathInput) {
                    pathInput.value = currentPath || '';
                    pathInput.focus();
                    pathInput.select();
                }
            } else {
                pathBar.style.display = 'none';
            }
        }
        
        function navigateToPath() {
            const pathInput = document.getElementById('pathInput');
            if (!pathInput) return;
            
            const path = pathInput.value.trim();
            if (!path) {
                alert('请输入有效的文件夹路径');
                return;
            }
            
            // 隐藏路径栏
            const pathBar = document.getElementById('pathBar');
            if (pathBar) pathBar.style.display = 'none';
            
            // 浏览指定路径
            browseFolder(path);
        }
        
        // 为路径输入框添加回车键支持
        document.addEventListener('DOMContentLoaded', function() {
            const pathInput = document.getElementById('pathInput');
            if (pathInput) {
                pathInput.addEventListener('keypress', function(e) {
                    if (e.key === 'Enter') {
                        navigateToPath();
                    }
                    if (e.key === 'Escape') {
                        togglePathBar();
                    }
                });
            }
        });
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(tmpl))
}

// 视频播放器页面处理器
func videoPlayerHandler(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Path[7:] // 去掉 "/video/" 前缀

	// 多次URL解码以确保正确处理
	for i := 0; i < 3; i++ {
		if decoded, err := url.QueryUnescape(filePath); err == nil {
			filePath = decoded
		} else {
			break
		}
	}

	// 替换正斜杠为反斜杠（Windows路径）
	filePath = strings.ReplaceAll(filePath, "/", "\\")

	// 检测访问来源，决定音频策略
	referer := r.Header.Get("Referer")
	muteByDefault := true // 默认静音
	accessSource := "直接访问"

	if referer != "" {
		// 检查是否来自搜索页面
		if strings.Contains(referer, r.Host) && (strings.Contains(referer, "/?") || strings.Contains(referer, "/search") || referer == "http://"+r.Host+"/" || referer == "https://"+r.Host+"/") {
			muteByDefault = false // 从搜索页面来的，不静音
			accessSource = "搜索页面"
		}
	}

	log.Printf("请求播放视频: %s，来源IP: %s，访问来源: %s，静音策略: %t", filePath, r.RemoteAddr, accessSource, muteByDefault)

	// 检查文件是否存在
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("视频文件不存在: %s", filePath)
			http.Error(w, "视频文件不存在", http.StatusNotFound)
		} else {
			log.Printf("访问视频文件失败: %s, 错误: %v", filePath, err)
			http.Error(w, "访问文件失败: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// 检查是否为视频文件并判断兼容性
	ext := strings.ToLower(filepath.Ext(filePath))
	videoExts := []string{".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm"}

	isVideo := false
	for _, videoExt := range videoExts {
		if ext == videoExt {
			isVideo = true
			break
		}
	}

	if !isVideo {
		log.Printf("非视频文件: %s", filePath)
		http.Error(w, "不是视频文件", http.StatusBadRequest)
		return
	}

	log.Printf("开始播放视频: %s，文件大小: %d 字节，格式: %s", filePath, fileInfo.Size(), ext)

	fileName := filepath.Base(filePath)
	fileSizeMB := float64(fileInfo.Size()) / (1024 * 1024)

	// 根据格式和ffmpeg可用性智能选择播放方式
	// 浏览器原生支持良好：MP4, WebM
	// 需要转码处理：AVI, FLV, MKV, WMV (现代浏览器支持差)
	// 兼容性待测试：MOV (部分支持)
	webCompatibleFormats := []string{".mp4", ".webm", ".mkv", ".wmv"}
	needTranscodeFormats := []string{".avi", ".flv"}

	isWebCompatible := false
	needTranscode := false

	for _, compatFormat := range webCompatibleFormats {
		if ext == compatFormat {
			isWebCompatible = true
			break
		}
	}

	for _, transcodeFormat := range needTranscodeFormats {
		if ext == transcodeFormat {
			needTranscode = true
			break
		}
	}

	if needTranscode {
		if ffmpegAvailable {
			log.Printf("%s格式，使用ffmpeg转码播放: %s", strings.ToUpper(ext[1:]), filePath)
			generateTranscodeVideoPlayer(w, filePath, fileName, fileSizeMB, ext, muteByDefault, accessSource)
		} else {
			log.Printf("%s格式，ffmpeg不可用，显示兼容性警告: %s", strings.ToUpper(ext[1:]), filePath)
			generateIncompatibleVideoPlayer(w, filePath, fileName, fileSizeMB, ext, muteByDefault, accessSource)
		}
	} else if isWebCompatible {
		log.Printf("%s格式，浏览器兼容，直接播放: %s", strings.ToUpper(ext[1:]), filePath)
		generateCompatibleVideoPlayer(w, filePath, fileName, fileSizeMB, ext, muteByDefault, accessSource)
	} else {
		// MOV等格式：先尝试播放，失败时显示警告
		log.Printf("%s格式，尝试兼容播放: %s", strings.ToUpper(ext[1:]), filePath)

		generateCompatibleVideoPlayerWithFallback(w, filePath, fileName, fileSizeMB, ext, muteByDefault, accessSource)
	}
}

// 兼容格式的视频播放器
func generateCompatibleVideoPlayer(w http.ResponseWriter, filePath, fileName string, fileSizeMB float64, ext string, muteByDefault bool, accessSource string) {
	// 根据来源设置video标签属性
	muteAttribute := ""
	if muteByDefault {
		muteAttribute = " muted"
	}

	audioStatusInfo := "🔊 有声音模式"
	if muteByDefault {
		audioStatusInfo = "🔇 静音模式"
	}

	tmpl := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>视频播放器 - ` + fileName + `</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; background: #000; color: white; overflow-x: hidden; }
        .container { max-width: 1200px; margin: 0 auto; padding: 20px; }
        .header { background: rgba(255,255,255,0.1); padding: 15px 20px; border-radius: 8px; margin-bottom: 20px; display: flex; justify-content: space-between; align-items: center; }
        .video-info { flex: 1; }
        .video-title { font-size: 18px; font-weight: 500; margin-bottom: 5px; word-break: break-all; }
        .video-meta { font-size: 14px; color: #ccc; word-break: break-all; }
        .controls { display: flex; gap: 10px; }
        .btn { padding: 8px 16px; border: none; border-radius: 4px; cursor: pointer; text-decoration: none; display: inline-block; }
        .btn-primary { background: #4CAF50; color: white; }
        .btn-secondary { background: #666; color: white; }
        .btn:hover { opacity: 0.8; }
        .video-container { 
            position: relative; 
            width: 100%; 
            background: #000; 
            border-radius: 8px; 
            overflow: hidden; 
            display: flex;
            justify-content: center;
            align-items: center;
            max-height: 80vh;
        }
        .video-player { 
            width: 100%; 
            height: auto; 
            max-height: 80vh;
            display: block; 
            border-radius: 8px;
        }
        .fullscreen-btn {
            position: absolute;
            top: 10px;
            right: 10px;
            background: rgba(0,0,0,0.7);
            color: white;
            border: none;
            padding: 8px 12px;
            border-radius: 4px;
            cursor: pointer;
            font-size: 14px;
        }
        .fullscreen-btn:hover { background: rgba(0,0,0,0.9); }
        .video-logs { margin-top: 20px; padding: 15px; background: rgba(255,255,255,0.1); border-radius: 8px; font-family: monospace; font-size: 12px; max-height: 200px; overflow-y: auto; }
        .tips { margin-top: 10px; padding: 10px; background: rgba(255,255,255,0.05); border-radius: 4px; font-size: 12px; color: #ccc; }
        .format-info { margin-top: 10px; padding: 10px; background: rgba(76, 175, 80, 0.2); border-left: 4px solid #4CAF50; border-radius: 4px; font-size: 12px; color: #a5d6a7; }
        .access-info { margin-top: 10px; padding: 10px; background: rgba(33, 150, 243, 0.2); border-left: 4px solid #2196F3; border-radius: 4px; font-size: 12px; color: #90caf9; }
        @media (max-width: 768px) {
            .header { flex-direction: column; gap: 10px; }
            .video-title { font-size: 16px; }
            .video-meta { font-size: 12px; }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <div class="video-info">
                <div class="video-title">` + fileName + `</div>
                <div class="video-meta">文件大小: ` + fmt.Sprintf("%.1f MB", fileSizeMB) + ` • 路径: ` + filePath + `</div>
            </div>
            <div class="controls">
                <a href="/file/` + url.QueryEscape(filePath) + `?download=1" class="btn btn-primary" download>下载视频</a>
                <button class="btn btn-secondary" onclick="window.close()">关闭窗口</button>
            </div>
        </div>
        
        <div class="format-info">
            ✅ 兼容格式 (` + strings.ToUpper(ext[1:]) + `) - 浏览器原生支持，播放流畅
        </div>
        
        <div class="access-info">
            📍 访问来源: ` + accessSource + ` • ` + audioStatusInfo + `
        </div>
        
        <div class="video-container">
            <video class="video-player" controls autoplay` + muteAttribute + ` preload="metadata" onloadstart="logEvent('视频开始加载')" onloadedmetadata="logEvent('视频元数据加载完成，分辨率: ' + this.videoWidth + 'x' + this.videoHeight)" oncanplay="logEvent('视频可以播放')" onplay="logEvent('视频开始播放')" onpause="logEvent('视频暂停')" onerror="showCompatibilityWarning(this)" onstalled="logEvent('视频加载停滞')" onabort="logEvent('视频加载中止')">
                <source src="/stream/` + url.QueryEscape(filePath) + `" type="video/mp4">
                <p class="error">您的浏览器不支持视频播放。</p>
            </video>
            <button class="fullscreen-btn" onclick="toggleFullscreen()">全屏</button>
        </div>
        
        <!-- 动态兼容性警告（默认隐藏） -->
        <div id="compatibilityWarning" class="warning-box" style="display: none;">
            <div class="warning-icon">⚠️</div>
            <div class="warning-title">播放遇到问题</div>
            <div class="warning-text">
                检测到 ` + strings.ToUpper(ext[1:]) + ` 格式播放异常，可能是编码兼容性问题。<br>
                建议下载文件后使用专业视频播放器观看。
            </div>
            <div class="alternative-options" style="justify-content: center; margin-top: 15px;">
                <a href="/file/` + url.QueryEscape(filePath) + `?download=1" class="btn btn-primary" download>
                    📥 下载文件
                </a>
                <button class="btn btn-warning" onclick="retryPlay()">
                    🔄 重新尝试
                </button>
            </div>
        </div>
        
        <div class="tips">
            💡 提示：视频高度限制在80%屏幕高度，可点击"全屏"按钮或双击视频进入全屏模式<br>
            🎵 音频策略：从搜索页面进入默认有声音，直接访问URL默认静音
        </div>
        
        <div class="video-logs" id="logs">
            <div>[ ` + time.Now().Format("15:04:05") + ` ] 视频播放器初始化完成 (来源: ` + accessSource + `)</div>
        </div>
    </div>

    <script>
        function logEvent(message) {
            const logs = document.getElementById('logs');
            const time = new Date().toLocaleTimeString();
            logs.innerHTML += '<div>[ ' + time + ' ] ' + message + '</div>';
            logs.scrollTop = logs.scrollHeight;
            console.log('[VideoPlayer] ' + message);
        }
        
        function logError(video) {
            const error = video.error;
            let errorMsg = '视频播放出错';
            if (error) {
                switch(error.code) {
                    case error.MEDIA_ERR_ABORTED:
                        errorMsg += ': 播放被中止';
                        break;
                    case error.MEDIA_ERR_NETWORK:
                        errorMsg += ': 网络错误';
                        break;
                    case error.MEDIA_ERR_DECODE:
                        errorMsg += ': 解码错误';
                        break;
                    case error.MEDIA_ERR_SRC_NOT_SUPPORTED:
                        errorMsg += ': 格式不支持';
                        break;
                    default:
                        errorMsg += ': 未知错误 (code: ' + error.code + ')';
                }
            }
            logEvent(errorMsg);
        }
        
        function toggleFullscreen() {
            const video = document.querySelector('.video-player');
            if (video.requestFullscreen) {
                video.requestFullscreen();
            } else if (video.webkitRequestFullscreen) {
                video.webkitRequestFullscreen();
            } else if (video.mozRequestFullScreen) {
                video.mozRequestFullScreen();
            }
            logEvent('请求进入全屏模式');
        }
        
        // 记录视频播放进度
        const video = document.querySelector('.video-player');
        let lastProgress = -1;
        
        video.addEventListener('timeupdate', function() {
            if (this.duration && !isNaN(this.duration)) {
                const progress = Math.floor(this.currentTime / this.duration * 100);
                // 每10%记录一次进度
                if (progress % 10 === 0 && progress !== lastProgress) {
                    logEvent('播放进度: ' + progress + '%');
                    lastProgress = progress;
                }
            }
        });
        
        video.addEventListener('ended', function() {
            logEvent('视频播放完成');
        });
        
        // 双击进入全屏
        video.addEventListener('dblclick', toggleFullscreen);
        
        // 页面加载完成
        window.onload = function() {
            logEvent('页面加载完成，准备播放视频');
            ` + func() string {
		if muteByDefault {
			return `logEvent('默认静音模式：直接访问URL');`
		} else {
			return `logEvent('默认有声模式：从搜索页面访问');`
		}
	}() + `
            
            // 检测视频尺寸并调整
            video.addEventListener('loadedmetadata', function() {
                const aspectRatio = this.videoWidth / this.videoHeight;
                logEvent('视频宽高比: ' + aspectRatio.toFixed(2) + ' (' + (aspectRatio < 1 ? '竖屏' : '横屏') + ')');
                
                if (aspectRatio < 0.8) { // 竖屏视频
                    this.style.maxWidth = '60vh';
                    logEvent('检测到竖屏视频，已限制最大宽度');
                }
            });
        };
        
        function showCompatibilityWarning(video) {
            const warningBox = document.getElementById('compatibilityWarning');
            warningBox.style.display = 'block';
            
            // 记录错误详情
            const error = video.error;
            let errorMsg = '检测到视频播放错误';
            if (error) {
                switch(error.code) {
                    case error.MEDIA_ERR_ABORTED:
                        errorMsg += ': 播放被中止';
                        break;
                    case error.MEDIA_ERR_NETWORK:
                        errorMsg += ': 网络错误';
                        break;
                    case error.MEDIA_ERR_DECODE:
                        errorMsg += ': 解码错误';
                        break;
                    case error.MEDIA_ERR_SRC_NOT_SUPPORTED:
                        errorMsg += ': 格式不支持';
                        break;
                    default:
                        errorMsg += ': 未知错误 (code: ' + error.code + ')';
                }
            }
            logEvent(errorMsg + '，已显示兼容性提示');
        }
        
        function retryPlay() {
            const warningBox = document.getElementById('compatibilityWarning');
            const video = document.querySelector('.video-player');
            
            warningBox.style.display = 'none';
            logEvent('用户选择重新尝试播放');
            
            // 重新加载视频
            video.load();
            video.play().catch(function(error) {
                logEvent('重新播放失败: ' + error.message);
                setTimeout(function() {
                    showCompatibilityWarning(video);
                }, 1000);
            });
        }
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(tmpl))
}

// 不兼容格式的视频播放器
func generateIncompatibleVideoPlayer(w http.ResponseWriter, filePath, fileName string, fileSizeMB float64, ext string, muteByDefault bool, accessSource string) {
	// 根据来源设置video标签属性
	muteAttribute := ""
	if muteByDefault {
		muteAttribute = " muted"
	}

	audioStatusInfo := "🔊 有声音模式"
	if muteByDefault {
		audioStatusInfo = "🔇 静音模式"
	}

	tmpl := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>视频播放器 - ` + fileName + `</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; background: #000; color: white; overflow-x: hidden; }
        .container { max-width: 1200px; margin: 0 auto; padding: 20px; }
        .header { background: rgba(255,255,255,0.1); padding: 15px 20px; border-radius: 8px; margin-bottom: 20px; display: flex; justify-content: space-between; align-items: center; }
        .video-info { flex: 1; }
        .video-title { font-size: 18px; font-weight: 500; margin-bottom: 5px; word-break: break-all; }
        .video-meta { font-size: 14px; color: #ccc; word-break: break-all; }
        .controls { display: flex; gap: 10px; }
        .btn { padding: 8px 16px; border: none; border-radius: 4px; cursor: pointer; text-decoration: none; display: inline-block; }
        .btn-primary { background: #4CAF50; color: white; }
        .btn-secondary { background: #666; color: white; }
        .btn-warning { background: #ff9800; color: white; }
        .btn:hover { opacity: 0.8; }
        .warning-box { 
            background: rgba(255, 152, 0, 0.2); 
            border: 2px solid #ff9800; 
            border-radius: 8px; 
            padding: 20px; 
            margin: 20px 0; 
            text-align: center;
        }
        .warning-icon { font-size: 48px; margin-bottom: 15px; }
        .warning-title { font-size: 20px; font-weight: bold; margin-bottom: 10px; color: #ffb74d; }
        .warning-text { font-size: 14px; line-height: 1.6; margin-bottom: 20px; }
        .format-info { margin-top: 10px; padding: 10px; background: rgba(255, 152, 0, 0.2); border-left: 4px solid #ff9800; border-radius: 4px; font-size: 12px; color: #ffcc02; }
        .access-info { margin-top: 10px; padding: 10px; background: rgba(33, 150, 243, 0.2); border-left: 4px solid #2196F3; border-radius: 4px; font-size: 12px; color: #90caf9; }
        .video-player-placeholder {
            background: #333;
            border-radius: 8px;
            padding: 40px;
            text-align: center;
            margin: 20px 0;
            min-height: 300px;
            display: flex;
            flex-direction: column;
            justify-content: center;
            align-items: center;
        }
        .alternative-options { display: flex; gap: 15px; justify-content: center; flex-wrap: wrap; margin-top: 20px; }
        @media (max-width: 768px) {
            .header { flex-direction: column; gap: 10px; }
            .video-title { font-size: 16px; }
            .video-meta { font-size: 12px; }
            .alternative-options { flex-direction: column; align-items: center; }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <div class="video-info">
                <div class="video-title">` + fileName + `</div>
                <div class="video-meta">文件大小: ` + fmt.Sprintf("%.1f MB", fileSizeMB) + ` • 路径: ` + filePath + `</div>
            </div>
            <div class="controls">
                <a href="/file/` + url.QueryEscape(filePath) + `?download=1" class="btn btn-primary" download>下载视频</a>
                <button class="btn btn-secondary" onclick="window.close()">关闭窗口</button>
            </div>
        </div>
        
        <div class="format-info">
            ⚠️ 兼容性限制 (` + strings.ToUpper(ext[1:]) + `) - 浏览器支持有限，建议下载后使用专业播放器
        </div>
        
        <div class="access-info">
            📍 访问来源: ` + accessSource + ` • ` + audioStatusInfo + `
        </div>
        
        <div class="warning-box">
            <div class="warning-icon">🎬</div>
            <div class="warning-title">视频格式兼容性问题</div>
            <div class="warning-text">
                ` + strings.ToUpper(ext[1:]) + ` 格式在现代浏览器中支持有限，可能无法正常播放。<br>
                建议下载文件后使用专业视频播放器（如VLC、PotPlayer等）观看。
            </div>
            
            <div class="video-player-placeholder">
                <div style="font-size: 64px; margin-bottom: 20px; opacity: 0.3;">📹</div>
                <div style="font-size: 18px; margin-bottom: 10px;">无法直接播放</div>
                <div style="font-size: 14px; opacity: 0.7;">浏览器不支持 ` + strings.ToUpper(ext[1:]) + ` 格式的在线播放</div>
            </div>
            
            <div class="alternative-options">
                <a href="/file/` + url.QueryEscape(filePath) + `?download=1" class="btn btn-primary" download>
                    📥 下载文件
                </a>
                <button class="btn btn-warning" onclick="tryForcePlay()">
                    ⚡ 强制尝试播放
                </button>
            </div>
        </div>
        
        <div id="forcePlayer" style="display: none;">
            <div style="background: rgba(255,255,255,0.1); padding: 15px; border-radius: 8px; margin: 20px 0;">
                <strong>强制播放模式：</strong>可能无法正常工作，如遇问题请下载文件<br>
                <span style="color: #90caf9;">来源: ` + accessSource + ` • ` + audioStatusInfo + `</span>
            </div>
            <video id="videoElement" controls autoplay` + muteAttribute + ` preload="metadata" style="width: 100%; max-height: 60vh; border-radius: 8px;">
                <source src="/stream/` + url.QueryEscape(filePath) + `">
                <p style="color: #ff6b6b;">您的浏览器不支持此视频格式。</p>
            </video>
        </div>
    </div>

    <script>
        function tryForcePlay() {
            const placeholder = document.querySelector('.video-player-placeholder');
            const forcePlayer = document.getElementById('forcePlayer');
            
            placeholder.style.display = 'none';
            forcePlayer.style.display = 'block';
            
            const video = document.getElementById('videoElement');
            video.addEventListener('error', function() {
                alert('播放失败！此格式不被浏览器支持，请下载文件使用专业播放器观看。');
            });
            
            console.log('尝试强制播放 ` + ext + ` 格式视频 (来源: ` + accessSource + `)');
        }
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(tmpl))
}

// 带有强化错误检测的兼容播放器（用于MOV等不确定兼容性的格式）
func generateCompatibleVideoPlayerWithFallback(w http.ResponseWriter, filePath, fileName string, fileSizeMB float64, ext string, muteByDefault bool, accessSource string) {
	// 根据来源设置video标签属性
	muteAttribute := ""
	if muteByDefault {
		muteAttribute = " muted"
	}

	audioStatusInfo := "🔊 有声音模式"
	if muteByDefault {
		audioStatusInfo = "🔇 静音模式"
	}

	tmpl := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>视频播放器 - ` + fileName + `</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; background: #000; color: white; overflow-x: hidden; }
        .container { max-width: 1200px; margin: 0 auto; padding: 20px; }
        .header { background: rgba(255,255,255,0.1); padding: 15px 20px; border-radius: 8px; margin-bottom: 20px; display: flex; justify-content: space-between; align-items: center; }
        .video-info { flex: 1; }
        .video-title { font-size: 18px; font-weight: 500; margin-bottom: 5px; word-break: break-all; }
        .video-meta { font-size: 14px; color: #ccc; word-break: break-all; }
        .controls { display: flex; gap: 10px; }
        .btn { padding: 8px 16px; border: none; border-radius: 4px; cursor: pointer; text-decoration: none; display: inline-block; }
        .btn-primary { background: #4CAF50; color: white; }
        .btn-secondary { background: #666; color: white; }
        .btn-warning { background: #ff9800; color: white; }
        .btn:hover { opacity: 0.8; }
        .video-container { 
            position: relative; 
            width: 100%; 
            background: #000; 
            border-radius: 8px; 
            overflow: hidden; 
            display: flex;
            justify-content: center;
            align-items: center;
            max-height: 80vh;
        }
        .video-player { 
            width: 100%; 
            height: auto; 
            max-height: 80vh;
            display: block; 
            border-radius: 8px;
        }
        .fullscreen-btn {
            position: absolute;
            top: 10px;
            right: 10px;
            background: rgba(0,0,0,0.7);
            color: white;
            border: none;
            padding: 8px 12px;
            border-radius: 4px;
            cursor: pointer;
            font-size: 14px;
        }
        .fullscreen-btn:hover { background: rgba(0,0,0,0.9); }
        .video-logs { margin-top: 20px; padding: 15px; background: rgba(255,255,255,0.1); border-radius: 8px; font-family: monospace; font-size: 12px; max-height: 200px; overflow-y: auto; }
        .tips { margin-top: 10px; padding: 10px; background: rgba(255,255,255,0.05); border-radius: 4px; font-size: 12px; color: #ccc; }
        .format-info { margin-top: 10px; padding: 10px; background: rgba(76, 175, 80, 0.2); border-left: 4px solid #4CAF50; border-radius: 4px; font-size: 12px; color: #a5d6a7; }
        .access-info { margin-top: 10px; padding: 10px; background: rgba(33, 150, 243, 0.2); border-left: 4px solid #2196F3; border-radius: 4px; font-size: 12px; color: #90caf9; }
        .warning-box { 
            background: rgba(255, 152, 0, 0.2); 
            border: 2px solid #ff9800; 
            border-radius: 8px; 
            padding: 20px; 
            margin: 20px 0; 
            text-align: center;
            display: none;
        }
        .warning-icon { font-size: 48px; margin-bottom: 15px; }
        .warning-title { font-size: 20px; font-weight: bold; margin-bottom: 10px; color: #ffb74d; }
        .warning-text { font-size: 14px; line-height: 1.6; margin-bottom: 20px; }
        .alternative-options { display: flex; gap: 15px; justify-content: center; flex-wrap: wrap; margin-top: 20px; }
        @media (max-width: 768px) {
            .header { flex-direction: column; gap: 10px; }
            .video-title { font-size: 16px; }
            .video-meta { font-size: 12px; }
            .alternative-options { flex-direction: column; align-items: center; }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <div class="video-info">
                <div class="video-title">` + fileName + `</div>
                <div class="video-meta">文件大小: ` + fmt.Sprintf("%.1f MB", fileSizeMB) + ` • 路径: ` + filePath + `</div>
            </div>
            <div class="controls">
                <a href="/file/` + url.QueryEscape(filePath) + `?download=1" class="btn btn-primary" download>下载视频</a>
                <button class="btn btn-secondary" onclick="window.close()">关闭窗口</button>
            </div>
        </div>
        
        <div class="format-info">
            🎯 兼容性测试 (` + strings.ToUpper(ext[1:]) + `) - 正在尝试播放，如有问题会自动提示
        </div>
        
        <div class="access-info">
            📍 访问来源: ` + accessSource + ` • ` + audioStatusInfo + `
        </div>
        
        <div class="video-container">
            <video class="video-player" controls autoplay` + muteAttribute + ` preload="metadata" onloadstart="logEvent('视频开始加载')" onloadedmetadata="logEvent('视频元数据加载完成，分辨率: ' + this.videoWidth + 'x' + this.videoHeight)" oncanplay="logEvent('视频可以播放')" onplay="logEvent('视频开始播放')" onpause="logEvent('视频暂停')" onerror="showCompatibilityWarning(this)" onstalled="handleStalled(this)" onabort="handleAbort(this)" onwaiting="logEvent('视频缓冲中...')">
                <source src="/stream/` + url.QueryEscape(filePath) + `" type="video/mp4">
                <p class="error">您的浏览器不支持视频播放。</p>
            </video>
            <button class="fullscreen-btn" onclick="toggleFullscreen()">全屏</button>
        </div>
        
        <!-- 动态兼容性警告（默认隐藏） -->
        <div id="compatibilityWarning" class="warning-box">
            <div class="warning-icon">⚠️</div>
            <div class="warning-title">播放遇到问题</div>
            <div class="warning-text">
                检测到 ` + strings.ToUpper(ext[1:]) + ` 格式播放异常，可能是编码兼容性问题。<br>
                建议下载文件后使用专业视频播放器观看。
            </div>
            <div class="alternative-options">
                <a href="/file/` + url.QueryEscape(filePath) + `?download=1" class="btn btn-primary" download>
                    📥 下载文件
                </a>
                <button class="btn btn-warning" onclick="retryPlay()">
                    🔄 重新尝试
                </button>
            </div>
        </div>
        
        <div class="tips">
            💡 提示：视频高度限制在80%屏幕高度，可点击"全屏"按钮或双击视频进入全屏模式<br>
            🎵 音频策略：从搜索页面进入默认有声音，直接访问URL默认静音
        </div>
        
        <div class="video-logs" id="logs">
            <div>[ ` + time.Now().Format("15:04:05") + ` ] 兼容性测试播放器初始化完成 (来源: ` + accessSource + `)</div>
        </div>
    </div>

    <script>
        let errorDetectionTimer = null;
        let playbackStarted = false;
        
        function logEvent(message) {
            const logs = document.getElementById('logs');
            const time = new Date().toLocaleTimeString();
            logs.innerHTML += '<div>[ ' + time + ' ] ' + message + '</div>';
            logs.scrollTop = logs.scrollHeight;
            console.log('[FallbackPlayer] ' + message);
        }
        
        function showCompatibilityWarning(video) {
            const warningBox = document.getElementById('compatibilityWarning');
            const videoContainer = document.querySelector('.video-container');
            
            // 隐藏视频容器，显示警告
            videoContainer.style.display = 'none';
            warningBox.style.display = 'block';
            
            // 记录错误详情
            const error = video.error;
            let errorMsg = '检测到视频播放错误';
            if (error) {
                switch(error.code) {
                    case error.MEDIA_ERR_ABORTED:
                        errorMsg += ': 播放被中止';
                        break;
                    case error.MEDIA_ERR_NETWORK:
                        errorMsg += ': 网络错误';
                        break;
                    case error.MEDIA_ERR_DECODE:
                        errorMsg += ': 解码错误';
                        break;
                    case error.MEDIA_ERR_SRC_NOT_SUPPORTED:
                        errorMsg += ': 格式不支持';
                        break;
                    default:
                        errorMsg += ': 未知错误 (code: ' + error.code + ')';
                }
            }
            logEvent(errorMsg + '，已显示兼容性提示');
        }
        
        function handleStalled(video) {
            logEvent('视频加载停滞，可能是格式兼容性问题');
            // 如果长时间停滞，显示警告
            setTimeout(function() {
                if (!playbackStarted) {
                    logEvent('长时间无法播放，显示兼容性警告');
                    showCompatibilityWarning(video);
                }
            }, 10000); // 10秒后显示警告
        }
        
        function handleAbort(video) {
            logEvent('视频加载中止，可能是格式不支持');
            // 延迟一下再显示警告，给浏览器一些时间
            setTimeout(function() {
                if (!playbackStarted) {
                    showCompatibilityWarning(video);
                }
            }, 2000);
        }
        
        function retryPlay() {
            const warningBox = document.getElementById('compatibilityWarning');
            const videoContainer = document.querySelector('.video-container');
            const video = document.querySelector('.video-player');
            
            warningBox.style.display = 'none';
            videoContainer.style.display = 'flex';
            logEvent('用户选择重新尝试播放');
            
            playbackStarted = false;
            
            // 重新加载视频
            video.load();
            video.play().catch(function(error) {
                logEvent('重新播放失败: ' + error.message);
                setTimeout(function() {
                    showCompatibilityWarning(video);
                }, 1000);
            });
        }
        
        function toggleFullscreen() {
            const video = document.querySelector('.video-player');
            if (video.requestFullscreen) {
                video.requestFullscreen();
            } else if (video.webkitRequestFullscreen) {
                video.webkitRequestFullscreen();
            } else if (video.mozRequestFullScreen) {
                video.mozRequestFullScreen();
            }
            logEvent('请求进入全屏模式');
        }
        
        // 记录视频播放进度
        const video = document.querySelector('.video-player');
        let lastProgress = -1;
        
        video.addEventListener('timeupdate', function() {
            if (this.duration && !isNaN(this.duration)) {
                const progress = Math.floor(this.currentTime / this.duration * 100);
                // 每10%记录一次进度
                if (progress % 10 === 0 && progress !== lastProgress) {
                    logEvent('播放进度: ' + progress + '%');
                    lastProgress = progress;
                }
            }
        });
        
        video.addEventListener('ended', function() {
            logEvent('视频播放完成');
        });
        
        video.addEventListener('play', function() {
            playbackStarted = true;
            logEvent('视频开始播放，兼容性测试通过');
        });
        
        // 双击进入全屏
        video.addEventListener('dblclick', toggleFullscreen);
        
        // 页面加载完成
        window.onload = function() {
            logEvent('页面加载完成，开始兼容性测试');
            ` + func() string {
		if muteByDefault {
			return `logEvent('默认静音模式：直接访问URL');`
		} else {
			return `logEvent('默认有声模式：从搜索页面访问');`
		}
	}() + `
            
            // 设置超时检测
            errorDetectionTimer = setTimeout(function() {
                if (!playbackStarted) {
                    logEvent('播放超时，可能存在兼容性问题');
                    showCompatibilityWarning(video);
                }
            }, 15000); // 15秒超时
            
            // 检测视频尺寸并调整
            video.addEventListener('loadedmetadata', function() {
                const aspectRatio = this.videoWidth / this.videoHeight;
                logEvent('视频宽高比: ' + aspectRatio.toFixed(2) + ' (' + (aspectRatio < 1 ? '竖屏' : '横屏') + ')');
                
                if (aspectRatio < 0.8) { // 竖屏视频
                    this.style.maxWidth = '60vh';
                    logEvent('检测到竖屏视频，已限制最大宽度');
                }
            });
            
            video.addEventListener('canplay', function() {
                if (errorDetectionTimer) {
                    clearTimeout(errorDetectionTimer);
                    errorDetectionTimer = null;
                }
            });
        };
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(tmpl))
}

// API搜索处理器
func apiSearchHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "查询参数不能为空", http.StatusBadRequest)
		return
	}

	// 获取分页参数
	pageStr := r.URL.Query().Get("page")
	pageSizeStr := r.URL.Query().Get("pageSize")

	page := 1
	pageSize := DefaultPageSize

	if pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}

	if pageSizeStr != "" {
		if ps, err := strconv.Atoi(pageSizeStr); err == nil && ps > 0 && ps <= MaxPageSize {
			pageSize = ps
		}
	}

	log.Printf("搜索请求: query=%s, page=%d, pageSize=%d, IP=%s", query, page, pageSize, r.RemoteAddr)

	// 使用缓存优化的搜索函数
	results, totalCount, fromCache, err := searchFilesWithCache(query, page, pageSize)
	if err != nil {
		log.Printf("搜索失败: %v", err)
		http.Error(w, "搜索失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	totalPages := (totalCount + pageSize - 1) / pageSize

	response := SearchResponse{
		Results:    results,
		Count:      len(results),
		TotalCount: totalCount,
		Query:      query,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}

	if fromCache {
		log.Printf("搜索完成(从缓存): 总共%d条结果, 返回第%d页(%d条)", totalCount, page, len(results))
	} else {
		log.Printf("搜索完成(新查询): 总共%d条结果, 返回第%d页(%d条), 已缓存", totalCount, page, len(results))
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(response)
}

// 带缓存的搜索文件函数
func searchFilesWithCache(query string, page, pageSize int) ([]SearchResult, int, bool, error) {
	// 检查缓存
	cacheMutex.RLock()
	cache, exists := searchCache[query]
	cacheMutex.RUnlock()

	var allPaths []string
	fromCache := false

	if exists && time.Since(cache.Timestamp) < cacheExpiry {
		// 使用缓存
		allPaths = cache.Paths
		fromCache = true
		log.Printf("使用缓存结果: query=%s, 缓存了%d个路径", query, len(allPaths))
		for i, path := range allPaths {
			log.Printf("缓存路径[%d]: %s", i+1, path)
		}
	} else {
		// 执行新搜索 - 优先使用Everything SDK，如果失败则回退到es.exe
		var err error
		allPaths, err = searchWithEverythingSDK(query)
		if err != nil {
			log.Printf("Everything SDK搜索失败，回退到es.exe: %v", err)
			allPaths, err = searchWithESExe(query)
			if err != nil {
				return nil, 0, false, fmt.Errorf("搜索失败 - SDK错误: %v, es.exe错误: %v", err, err)
			}
		}

		log.Printf("总共%d个有效路径", len(allPaths))
		for i, path := range allPaths {
			log.Printf("搜索路径[%d]: %s", i+1, path)
		}

		// 更新缓存
		cacheMutex.Lock()
		searchCache[query] = &SearchCache{
			Paths:     allPaths,
			Timestamp: time.Now(),
		}
		cacheMutex.Unlock()

		log.Printf("已将搜索结果缓存: query=%s, 路径数=%d", query, len(allPaths))
	}

	totalCount := len(allPaths)

	if totalCount == 0 {
		return []SearchResult{}, 0, fromCache, nil
	}

	// 计算分页范围
	start := (page - 1) * pageSize
	end := start + pageSize
	if end > totalCount {
		end = totalCount
	}

	var results []SearchResult
	if start < totalCount {
		log.Printf("开始处理第%d页: %d-%d", page, start+1, end)

		for i := start; i < end; i++ {
			filePath := allPaths[i]
			log.Printf("处理文件路径[%d]: %s", i+1, filePath)

			// 获取文件信息
			info, err := os.Stat(filePath)
			if err != nil {
				log.Printf("无法访问文件[%d]: %s, 错误: %v", i+1, filePath, err)
				continue // 跳过无法访问的文件
			}
			log.Printf("文件[%d]访问成功: %s", i+1, filePath)

			result := SearchResult{
				Name:     filepath.Base(filePath),
				Path:     filePath,
				Size:     info.Size(),
				Modified: info.ModTime().Format("2006-01-02 15:04:05"),
				IsDir:    info.IsDir(),
			}

			// 确定文件类型
			if result.IsDir {
				result.Type = "folder"
			} else {
				ext := strings.ToLower(filepath.Ext(filePath))
				switch ext {
				case ".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm":
					result.Type = "video"
				case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp":
					result.Type = "image"
				default:
					result.Type = "file"
				}
			}

			results = append(results, result)
		}

		log.Printf("第%d页处理完成，返回%d条结果", page, len(results))
	}

	return results, totalCount, fromCache, nil
}

// 清理过期缓存的函数
func cleanExpiredCache() {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	for query, cache := range searchCache {
		if time.Since(cache.Timestamp) > cacheExpiry {
			delete(searchCache, query)
			log.Printf("清理过期缓存: %s", query)
		}
	}
}

// 优化的搜索文件函数（保持向后兼容）
func searchFilesOptimized(query string, page, pageSize int) ([]SearchResult, int, error) {
	results, totalCount, _, err := searchFilesWithCache(query, page, pageSize)
	return results, totalCount, err
}

// 使用es.exe搜索文件（保持向后兼容）
func searchFiles(query string) ([]SearchResult, error) {
	results, _, err := searchFilesOptimized(query, 1, 999999)
	return results, err
}

// 文件下载处理器
func fileHandler(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Path[6:] // 去掉 "/file/" 前缀

	// 多次URL解码以确保正确处理
	for i := 0; i < 3; i++ {
		if decoded, err := url.QueryUnescape(filePath); err == nil {
			filePath = decoded
		} else {
			break
		}
	}

	// 替换正斜杠为反斜杠（Windows路径）
	filePath = strings.ReplaceAll(filePath, "/", "\\")

	log.Printf("文件下载请求: %s，来源IP: %s", filePath, r.RemoteAddr)

	// 检查文件是否存在
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("文件不存在: %s", filePath)
			http.Error(w, "文件不存在", http.StatusNotFound)
		} else {
			log.Printf("访问文件失败: %s, 错误: %v", filePath, err)
			http.Error(w, "访问文件失败: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// 获取文件名
	fileName := filepath.Base(filePath)

	// 检查是否为下载请求（通过URL参数或来源判断）
	isDownload := r.URL.Query().Get("download") != "" ||
		r.Header.Get("Accept") != "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8"

	// 如果是下载请求，设置下载头
	if isDownload || r.URL.RawQuery != "" {
		// 设置下载响应头
		w.Header().Set("Content-Disposition", "attachment; filename=\""+fileName+"\"")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(fileInfo.Size(), 10))
		log.Printf("强制下载文件: %s (大小: %d 字节)", fileName, fileInfo.Size())
	} else {
		// 普通访问，设置适当的Content-Type
		ext := strings.ToLower(filepath.Ext(filePath))
		contentType := getContentType(ext)
		w.Header().Set("Content-Type", contentType)
		log.Printf("提供文件预览: %s (类型: %s)", fileName, contentType)
	}

	log.Printf("开始提供文件: %s", filePath)
	http.ServeFile(w, r, filePath)
}

// 获取文件的Content-Type
func getContentType(ext string) string {
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".avi":
		return "video/x-msvideo"
	case ".mkv":
		return "video/x-matroska"
	case ".mov":
		return "video/quicktime"
	case ".wmv":
		return "video/x-ms-wmv"
	case ".flv":
		return "video/x-flv"
	case ".webm":
		return "video/webm"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".zip":
		return "application/zip"
	case ".rar":
		return "application/x-rar-compressed"
	case ".7z":
		return "application/x-7z-compressed"
	default:
		return "application/octet-stream"
	}
}

// 视频流处理器
func streamHandler(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Path[8:] // 去掉 "/stream/" 前缀

	// 多次URL解码以确保正确处理
	for i := 0; i < 3; i++ {
		if decoded, err := url.QueryUnescape(filePath); err == nil {
			filePath = decoded
		} else {
			break
		}
	}

	// 替换正斜杠为反斜杠（Windows路径）
	filePath = strings.ReplaceAll(filePath, "/", "\\")

	log.Printf("视频流请求: %s，Range: %s，来源IP: %s", filePath, r.Header.Get("Range"), r.RemoteAddr)

	// 检查文件是否存在
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("视频文件不存在: %s", filePath)
			http.Error(w, "文件不存在", http.StatusNotFound)
		} else {
			log.Printf("访问视频文件失败: %s, 错误: %v", filePath, err)
			http.Error(w, "访问文件失败: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("无法打开视频文件: %s, 错误: %v", filePath, err)
		http.Error(w, "无法打开文件", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// 设置适当的Content-Type
	ext := strings.ToLower(filepath.Ext(filePath))
	contentType := "application/octet-stream"
	switch ext {
	case ".mp4":
		contentType = "video/mp4"
	case ".mkv":
		contentType = "video/x-matroska"
	case ".avi":
		contentType = "video/x-msvideo"
	case ".mov":
		contentType = "video/quicktime"
	case ".wmv":
		contentType = "video/x-ms-wmv"
	case ".flv":
		contentType = "video/x-flv"
	case ".webm":
		contentType = "video/webm"
	}

	log.Printf("视频文件信息: 大小=%d字节, 类型=%s", fileInfo.Size(), contentType)

	// 支持Range请求以实现视频拖拽
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		log.Printf("处理Range请求: %s", rangeHeader)
		serveRange(w, r, file, fileInfo.Size(), contentType)
	} else {
		log.Printf("提供完整视频文件")
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.FormatInt(fileInfo.Size(), 10))
		w.Header().Set("Accept-Ranges", "bytes")
		io.Copy(w, file)
	}
}

// 支持Range请求的视频流处理
func serveRange(w http.ResponseWriter, r *http.Request, file *os.File, fileSize int64, contentType string) {
	rangeHeader := r.Header.Get("Range")

	// 解析Range头
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		log.Printf("无效的Range头格式: %s", rangeHeader)
		http.Error(w, "无效的Range头", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	rangeSpec := rangeHeader[6:] // 去掉"bytes="
	rangeParts := strings.Split(rangeSpec, "-")
	if len(rangeParts) != 2 {
		log.Printf("无效的Range头格式: %s", rangeHeader)
		http.Error(w, "无效的Range头", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	var start, end int64
	var err error

	if rangeParts[0] != "" {
		start, err = strconv.ParseInt(rangeParts[0], 10, 64)
		if err != nil {
			log.Printf("无法解析Range起始位置: %s", rangeParts[0])
			http.Error(w, "无效的Range头", http.StatusRequestedRangeNotSatisfiable)
			return
		}
	}

	if rangeParts[1] != "" {
		end, err = strconv.ParseInt(rangeParts[1], 10, 64)
		if err != nil {
			log.Printf("无法解析Range结束位置: %s", rangeParts[1])
			http.Error(w, "无效的Range头", http.StatusRequestedRangeNotSatisfiable)
			return
		}
	} else {
		end = fileSize - 1
	}

	if start > end || start >= fileSize {
		log.Printf("无效的Range范围: start=%d, end=%d, fileSize=%d", start, end, fileSize)
		http.Error(w, "无效的Range头", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	contentLength := end - start + 1

	log.Printf("Range请求处理: %d-%d/%d (长度: %d)", start, end, fileSize, contentLength)

	// 设置响应头
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
	w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusPartialContent)

	// 移动到起始位置并复制数据
	file.Seek(start, 0)
	copied, err := io.CopyN(w, file, contentLength)
	if err != nil {
		log.Printf("Range请求数据传输错误: %v, 已传输: %d字节", err, copied)
	} else {
		log.Printf("Range请求完成: 传输了%d字节", copied)
	}
}

// 缩略图处理器
func thumbnailHandler(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Path[11:] // 去掉 "/thumbnail/" 前缀

	// 多次URL解码以确保正确处理
	for i := 0; i < 3; i++ {
		if decoded, err := url.QueryUnescape(filePath); err == nil {
			filePath = decoded
		} else {
			break
		}
	}

	// 替换正斜杠为反斜杠（Windows路径）
	filePath = strings.ReplaceAll(filePath, "/", "\\")

	log.Printf("缩略图请求: %s", filePath)

	// 检查文件是否存在
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.Printf("缩略图文件不存在: %s", filePath)
		http.Error(w, "文件不存在", http.StatusNotFound)
		return
	}

	// 检查是否为图片文件
	ext := strings.ToLower(filepath.Ext(filePath))
	if !isImageFile(ext) {
		log.Printf("非图片文件: %s", filePath)
		http.Error(w, "不是图片文件", http.StatusBadRequest)
		return
	}

	// 简单实现：直接返回原图片（在实际项目中可以生成缩略图）
	http.ServeFile(w, r, filePath)
}

func isImageFile(ext string) bool {
	imageExts := []string{".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp"}
	for _, imgExt := range imageExts {
		if ext == imgExt {
			return true
		}
	}
	return false
}

// 搜索处理器（保持兼容性）
func searchHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("search")
	if query == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	results, err := searchFiles(query)
	if err != nil {
		http.Error(w, "搜索失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 返回JSON格式的搜索结果
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"results": results,
		"count":   len(results),
		"query":   query,
	})
}

// 缓存状态API
func cacheStatusHandler(w http.ResponseWriter, r *http.Request) {
	cacheMutex.RLock()
	defer cacheMutex.RUnlock()

	status := make(map[string]interface{})
	status["cache_count"] = len(searchCache)
	status["cache_expiry_minutes"] = int(cacheExpiry.Minutes())

	var cacheInfo []map[string]interface{}
	for query, cache := range searchCache {
		info := map[string]interface{}{
			"query":       query,
			"path_count":  len(cache.Paths),
			"timestamp":   cache.Timestamp.Format("2006-01-02 15:04:05"),
			"age_minutes": int(time.Since(cache.Timestamp).Minutes()),
		}
		cacheInfo = append(cacheInfo, info)
	}
	status["caches"] = cacheInfo

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(status)
}

// 清除缓存API
func cacheClearHandler(w http.ResponseWriter, r *http.Request) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	oldCount := len(searchCache)
	searchCache = make(map[string]*SearchCache)

	log.Printf("清除了%d个搜索缓存", oldCount)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       true,
		"message":       fmt.Sprintf("已清除%d个缓存", oldCount),
		"cleared_count": oldCount,
	})
}

// 检测ffmpeg是否可用的函数
func checkFFmpegAvailability() {
	cmd := exec.Command("ffmpeg", "-version")
	err := cmd.Run()
	if err != nil {
		log.Printf("ffmpeg不可用: %v", err)
		ffmpegAvailable = false
	} else {
		log.Printf("ffmpeg可用")
		ffmpegAvailable = true
	}
}

// ffmpeg转码播放器页面
func generateTranscodeVideoPlayer(w http.ResponseWriter, filePath, fileName string, fileSizeMB float64, ext string, muteByDefault bool, accessSource string) {
	// 根据来源设置video标签属性
	muteAttribute := ""
	if muteByDefault {
		muteAttribute = " muted"
	}

	audioStatusInfo := "🔊 有声音模式"
	if muteByDefault {
		audioStatusInfo = "🔇 静音模式"
	}

	tmpl := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>视频播放器 - ` + fileName + `</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; background: #000; color: white; overflow-x: hidden; }
        .container { max-width: 1200px; margin: 0 auto; padding: 20px; }
        .header { background: rgba(255,255,255,0.1); padding: 15px 20px; border-radius: 8px; margin-bottom: 20px; display: flex; justify-content: space-between; align-items: center; }
        .video-info { flex: 1; }
        .video-title { font-size: 18px; font-weight: 500; margin-bottom: 5px; word-break: break-all; }
        .video-meta { font-size: 14px; color: #ccc; word-break: break-all; }
        .controls { display: flex; gap: 10px; }
        .btn { padding: 8px 16px; border: none; border-radius: 4px; cursor: pointer; text-decoration: none; display: inline-block; }
        .btn-primary { background: #4CAF50; color: white; }
        .btn-secondary { background: #666; color: white; }
        .btn:hover { opacity: 0.8; }
        .video-container { 
            position: relative; 
            width: 100%; 
            background: #000; 
            border-radius: 8px; 
            overflow: hidden; 
            display: flex;
            justify-content: center;
            align-items: center;
            max-height: 80vh;
        }
        .video-player { 
            width: 100%; 
            height: auto; 
            max-height: 80vh;
            display: block; 
            border-radius: 8px;
        }
        .fullscreen-btn {
            position: absolute;
            top: 10px;
            right: 10px;
            background: rgba(0,0,0,0.7);
            color: white;
            border: none;
            padding: 8px 12px;
            border-radius: 4px;
            cursor: pointer;
            font-size: 14px;
        }
        .fullscreen-btn:hover { background: rgba(0,0,0,0.9); }
        .video-logs { margin-top: 20px; padding: 15px; background: rgba(255,255,255,0.1); border-radius: 8px; font-family: monospace; font-size: 12px; max-height: 200px; overflow-y: auto; }
        .tips { margin-top: 10px; padding: 10px; background: rgba(255,255,255,0.05); border-radius: 4px; font-size: 12px; color: #ccc; }
        .format-info { margin-top: 10px; padding: 10px; background: rgba(76, 175, 80, 0.2); border-left: 4px solid #4CAF50; border-radius: 4px; font-size: 12px; color: #a5d6a7; }
        .access-info { margin-top: 10px; padding: 10px; background: rgba(33, 150, 243, 0.2); border-left: 4px solid #2196F3; border-radius: 4px; font-size: 12px; color: #90caf9; }
        @media (max-width: 768px) {
            .header { flex-direction: column; gap: 10px; }
            .video-title { font-size: 16px; }
            .video-meta { font-size: 12px; }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <div class="video-info">
                <div class="video-title">` + fileName + `</div>
                <div class="video-meta">文件大小: ` + fmt.Sprintf("%.1f MB", fileSizeMB) + ` • 路径: ` + filePath + `</div>
            </div>
            <div class="controls">
                <a href="/file/` + url.QueryEscape(filePath) + `?download=1" class="btn btn-primary" download>下载视频</a>
                <button class="btn btn-secondary" onclick="window.close()">关闭窗口</button>
            </div>
        </div>
        
        <div class="format-info">
            🔄 ffmpeg转码播放 (` + strings.ToUpper(ext[1:]) + ` → MP4) - 实时转码中，首次加载可能较慢
        </div>
        
        <div class="access-info">
            📍 访问来源: ` + accessSource + ` • ` + audioStatusInfo + `
        </div>
        
        <div class="video-container">
            <video class="video-player" controls autoplay` + muteAttribute + ` preload="metadata" onloadstart="logEvent('开始加载转码视频')" onloadedmetadata="logEvent('转码视频元数据加载完成，分辨率: ' + this.videoWidth + 'x' + this.videoHeight)" oncanplay="logEvent('转码视频可以播放')" onplay="logEvent('转码视频开始播放')" onpause="logEvent('转码视频暂停')" onerror="logTranscodeError(this)" onwaiting="logEvent('转码缓冲中...')" onprogress="logEvent('转码视频下载进度更新')">
                <source src="/transcode/` + url.QueryEscape(filePath) + `" type="video/mp4">
                <p class="error">您的浏览器不支持视频播放。</p>
            </video>
            <button class="fullscreen-btn" onclick="toggleFullscreen()">全屏</button>
        </div>
        
        <div class="tips">
            💡 提示：使用ffmpeg实时转码，首次播放需要等待转码启动。转码过程中可能出现短暂缓冲。<br>
            🎵 音频策略：从搜索页面进入默认有声音，直接访问URL默认静音
        </div>
        
        <div class="video-logs" id="logs">
            <div>[ ` + time.Now().Format("15:04:05") + ` ] ffmpeg转码播放器初始化完成 (来源: ` + accessSource + `)</div>
        </div>
    </div>

    <script>
        function logEvent(message) {
            const logs = document.getElementById('logs');
            const time = new Date().toLocaleTimeString();
            logs.innerHTML += '<div>[ ' + time + ' ] ' + message + '</div>';
            logs.scrollTop = logs.scrollHeight;
            console.log('[TranscodePlayer] ' + message);
        }
        
        function logTranscodeError(video) {
            const error = video.error;
            let errorMsg = 'ffmpeg转码播放出错';
            if (error) {
                switch(error.code) {
                    case error.MEDIA_ERR_ABORTED:
                        errorMsg += ': 转码被中止';
                        break;
                    case error.MEDIA_ERR_NETWORK:
                        errorMsg += ': 网络错误';
                        break;
                    case error.MEDIA_ERR_DECODE:
                        errorMsg += ': 转码解码错误';
                        break;
                    case error.MEDIA_ERR_SRC_NOT_SUPPORTED:
                        errorMsg += ': 转码格式错误';
                        break;
                    default:
                        errorMsg += ': 未知转码错误 (code: ' + error.code + ')';
                }
            }
            logEvent(errorMsg);
        }
        
        function toggleFullscreen() {
            const video = document.querySelector('.video-player');
            if (video.requestFullscreen) {
                video.requestFullscreen();
            } else if (video.webkitRequestFullscreen) {
                video.webkitRequestFullscreen();
            } else if (video.mozRequestFullScreen) {
                video.mozRequestFullScreen();
            }
            logEvent('请求进入全屏模式');
        }
        
        // 记录视频播放进度
        const video = document.querySelector('.video-player');
        let lastProgress = -1;
        
        video.addEventListener('timeupdate', function() {
            if (this.duration && !isNaN(this.duration)) {
                const progress = Math.floor(this.currentTime / this.duration * 100);
                // 每10%记录一次进度
                if (progress % 10 === 0 && progress !== lastProgress) {
                    logEvent('转码播放进度: ' + progress + '%');
                    lastProgress = progress;
                }
            }
        });
        
        video.addEventListener('ended', function() {
            logEvent('转码视频播放完成');
        });
        
        // 双击进入全屏
        video.addEventListener('dblclick', toggleFullscreen);
        
        // 页面加载完成
        window.onload = function() {
            logEvent('页面加载完成，准备播放转码视频');
            ` + func() string {
		if muteByDefault {
			return `logEvent('默认静音模式：直接访问URL');`
		} else {
			return `logEvent('默认有声模式：从搜索页面访问');`
		}
	}() + `
            
            // 检测视频尺寸并调整
            video.addEventListener('loadedmetadata', function() {
                const aspectRatio = this.videoWidth / this.videoHeight;
                logEvent('转码视频宽高比: ' + aspectRatio.toFixed(2) + ' (' + (aspectRatio < 1 ? '竖屏' : '横屏') + ')');
                
                if (aspectRatio < 0.8) { // 竖屏视频
                    this.style.maxWidth = '60vh';
                    logEvent('检测到竖屏视频，已限制最大宽度');
                }
            });
        };
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(tmpl))
}

// 转码处理器 - 使用ffmpeg实时转码视频
func transcodeHandler(w http.ResponseWriter, r *http.Request) {
	if !ffmpegAvailable {
		log.Printf("转码请求失败: ffmpeg不可用")
		http.Error(w, "ffmpeg不可用", http.StatusServiceUnavailable)
		return
	}

	filePath := r.URL.Path[11:] // 去掉 "/transcode/" 前缀

	// 多次URL解码以确保正确处理
	for i := 0; i < 3; i++ {
		if decoded, err := url.QueryUnescape(filePath); err == nil {
			filePath = decoded
		} else {
			break
		}
	}

	// 替换正斜杠为反斜杠（Windows路径）
	filePath = strings.ReplaceAll(filePath, "/", "\\")

	log.Printf("转码请求: %s，来源IP: %s", filePath, r.RemoteAddr)

	// 检查文件是否存在
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		log.Printf("转码文件不存在: %s", filePath)
		http.Error(w, "文件不存在", http.StatusNotFound)
		return
	}

	// 设置响应头
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "no-cache")

	// ffmpeg转码命令
	// -i: 输入文件
	// -c:v libx264: 视频编码器H.264
	// -c:a aac: 音频编码器AAC
	// -f mp4: 输出格式MP4
	// -movflags frag_keyframe+empty_moov: 支持流式播放
	// -: 输出到stdout
	cmd := exec.Command("ffmpeg",
		"-i", filePath,
		"-c:v", "libx264",
		"-c:a", "aac",
		"-preset", "fast", // 快速编码预设
		"-crf", "23", // 视频质量（越小质量越好）
		"-maxrate", "2M", // 最大码率2Mbps
		"-bufsize", "4M", // 缓冲区大小
		"-f", "mp4",
		"-movflags", "frag_keyframe+empty_moov",
		"-")

	// 设置命令的stdout为HTTP响应
	cmd.Stdout = w

	// 获取stderr用于错误日志
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("创建ffmpeg stderr管道失败: %v", err)
		http.Error(w, "转码初始化失败", http.StatusInternalServerError)
		return
	}

	log.Printf("开始ffmpeg转码: %s", filePath)

	// 启动转码进程
	if err := cmd.Start(); err != nil {
		log.Printf("启动ffmpeg转码失败: %v", err)
		http.Error(w, "转码启动失败", http.StatusInternalServerError)
		return
	}

	// 在goroutine中读取ffmpeg的错误输出
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				// 只记录关键的ffmpeg输出，避免日志过多
				output := string(buf[:n])
				if strings.Contains(output, "error") || strings.Contains(output, "Error") {
					log.Printf("ffmpeg转码错误: %s", strings.TrimSpace(output))
				}
			}
			if err != nil {
				break
			}
		}
	}()

	// 等待转码完成
	err = cmd.Wait()
	if err != nil {
		log.Printf("ffmpeg转码完成，退出状态: %v", err)
	} else {
		log.Printf("ffmpeg转码成功完成: %s", filePath)
	}
}

// 文件夹浏览API处理器
func apiBrowseHandler(w http.ResponseWriter, r *http.Request) {
	folderPath := r.URL.Query().Get("path")
	if folderPath == "" {
		http.Error(w, "路径参数不能为空", http.StatusBadRequest)
		return
	}

	log.Printf("文件夹浏览请求: path=%s, IP=%s", folderPath, r.RemoteAddr)

	// 检查路径是否存在且为目录
	fileInfo, err := os.Stat(folderPath)
	if os.IsNotExist(err) {
		log.Printf("文件夹不存在: %s", folderPath)
		http.Error(w, "文件夹不存在", http.StatusNotFound)
		return
	}

	if !fileInfo.IsDir() {
		log.Printf("路径不是文件夹: %s", folderPath)
		http.Error(w, "路径不是文件夹", http.StatusBadRequest)
		return
	}

	// 读取文件夹内容
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		log.Printf("读取文件夹失败: %s, 错误: %v", folderPath, err)
		http.Error(w, "读取文件夹失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var results []SearchResult
	for _, entry := range entries {
		entryPath := filepath.Join(folderPath, entry.Name())

		// 获取详细信息
		info, err := entry.Info()
		if err != nil {
			log.Printf("获取文件信息失败: %s, 跳过", entryPath)
			continue
		}

		result := SearchResult{
			Name:     entry.Name(),
			Path:     entryPath,
			Size:     info.Size(),
			Modified: info.ModTime().Format("2006-01-02 15:04:05"),
			IsDir:    entry.IsDir(),
		}

		// 确定文件类型
		if result.IsDir {
			result.Type = "folder"
		} else {
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			switch ext {
			case ".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm":
				result.Type = "video"
			case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp":
				result.Type = "image"
			default:
				result.Type = "file"
			}
		}

		results = append(results, result)
	}

	// 生成路径部分用于面包屑导航
	pathParts := generatePathParts(folderPath)

	// 获取父目录路径
	parentPath := filepath.Dir(folderPath)
	canGoUp := folderPath != filepath.VolumeName(folderPath) && parentPath != folderPath

	response := BrowseResponse{
		Results:     results,
		Count:       len(results),
		CurrentPath: folderPath,
		ParentPath:  parentPath,
		PathParts:   pathParts,
		CanGoUp:     canGoUp,
	}

	log.Printf("文件夹浏览完成: %s, 返回%d个项目", folderPath, len(results))

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(response)
}

// 生成路径部分用于面包屑导航
func generatePathParts(fullPath string) []PathPart {
	var parts []PathPart

	// 清理路径并分割
	cleanPath := filepath.Clean(fullPath)

	// 获取盘符（Windows）
	volume := filepath.VolumeName(cleanPath)
	if volume != "" {
		parts = append(parts, PathPart{
			Name: volume + "\\",
			Path: volume + "\\",
		})
		cleanPath = cleanPath[len(volume)+1:] // 移除盘符部分
	}

	// 分割剩余路径
	if cleanPath != "" && cleanPath != "." {
		pathElements := strings.Split(cleanPath, string(os.PathSeparator))
		currentPath := volume + "\\"

		for _, element := range pathElements {
			if element == "" {
				continue
			}
			currentPath = filepath.Join(currentPath, element)
			parts = append(parts, PathPart{
				Name: element,
				Path: currentPath,
			})
		}
	}

	return parts
}

// 文本预览API处理器
func textPreviewHandler(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		http.Error(w, "路径参数不能为空", http.StatusBadRequest)
		return
	}

	log.Printf("文本预览请求: path=%s, IP=%s", filePath, r.RemoteAddr)

	// 检查文件是否存在
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("文本文件不存在: %s", filePath)
			http.Error(w, "文件不存在", http.StatusNotFound)
		} else {
			log.Printf("访问文本文件失败: %s, 错误: %v", filePath, err)
			http.Error(w, "访问文件失败: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if fileInfo.IsDir() {
		http.Error(w, "不能预览文件夹", http.StatusBadRequest)
		return
	}

	// 检查文件大小，避免读取过大的文件
	const maxFileSize = 10 * 1024 * 1024 // 10MB
	if fileInfo.Size() > maxFileSize {
		http.Error(w, "文件过大，无法预览", http.StatusBadRequest)
		return
	}

	// 读取文件内容
	content, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("读取文本文件失败: %s, 错误: %v", filePath, err)
		http.Error(w, "读取文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 检测文件编码并转换为UTF-8
	contentStr := detectAndConvertEncoding(content)

	response := map[string]interface{}{
		"path":     filePath,
		"name":     filepath.Base(filePath),
		"size":     fileInfo.Size(),
		"modified": fileInfo.ModTime().Format("2006-01-02 15:04:05"),
		"content":  contentStr,
		"lines":    len(strings.Split(contentStr, "\n")),
		"encoding": detectEncoding(content),
	}

	log.Printf("文本预览成功: %s, 大小: %d字节, 行数: %d", filePath, fileInfo.Size(), response["lines"])

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(response)
}

// 检测文件编码并转换为UTF-8
func detectAndConvertEncoding(data []byte) string {
	// 简单的编码检测和转换
	// 1. 首先检查是否已经是有效的UTF-8
	if isValidUTF8(data) {
		return string(data)
	}

	// 2. 尝试GBK编码（中文Windows常用）
	if gbkStr := tryGBKDecode(data); gbkStr != "" {
		return gbkStr
	}

	// 3. 作为Latin-1处理（兼容ASCII）
	return string(data)
}

// 检测编码类型
func detectEncoding(data []byte) string {
	if isValidUTF8(data) {
		return "UTF-8"
	}
	if tryGBKDecode(data) != "" {
		return "GBK"
	}
	return "Unknown"
}

// 检查是否为有效的UTF-8编码
func isValidUTF8(data []byte) bool {
	return len(data) > 0 && strings.ToValidUTF8(string(data), "�") == string(data)
}

// 尝试GBK解码
func tryGBKDecode(data []byte) string {
	// 这里是简化版本，实际项目中可以使用 golang.org/x/text/encoding 包
	// 由于避免引入外部依赖，这里做简单处理

	// 检查是否可能是GBK编码（简单检测）
	hasHighByte := false
	for _, b := range data {
		if b > 127 {
			hasHighByte = true
			break
		}
	}

	if !hasHighByte {
		// 如果没有高位字节，直接作为ASCII处理
		return string(data)
	}

	// 简化的GBK处理，实际应该使用专门的编码库
	return ""
}

// 图片查看器页面处理器
func imageViewerHandler(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Path[11:] // 去掉 "/imageview/" 前缀

	// 多次URL解码以确保正确处理
	for i := 0; i < 3; i++ {
		if decoded, err := url.QueryUnescape(filePath); err == nil {
			filePath = decoded
		} else {
			break
		}
	}

	// 替换正斜杠为反斜杠（Windows路径）
	filePath = strings.ReplaceAll(filePath, "/", "\\")

	log.Printf("图片查看器请求: %s，来源IP: %s", filePath, r.RemoteAddr)

	// 检查文件是否存在
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("图片文件不存在: %s", filePath)
			http.Error(w, "图片文件不存在", http.StatusNotFound)
		} else {
			log.Printf("访问图片文件失败: %s, 错误: %v", filePath, err)
			http.Error(w, "访问文件失败: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// 检查是否为图片文件
	ext := strings.ToLower(filepath.Ext(filePath))
	if !isImageFile(ext) {
		log.Printf("非图片文件: %s", filePath)
		http.Error(w, "不是图片文件", http.StatusBadRequest)
		return
	}

	fileName := filepath.Base(filePath)
	fileSizeMB := float64(fileInfo.Size()) / (1024 * 1024)

	tmpl := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>图片查看器 - ` + fileName + `</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; background: #000; color: white; overflow: hidden; }
        .container { width: 100vw; height: 100vh; display: flex; flex-direction: column; }
        .header { background: rgba(0,0,0,0.8); padding: 15px 20px; position: fixed; top: 0; left: 0; right: 0; z-index: 1000; backdrop-filter: blur(10px); }
        .header-content { display: flex; justify-content: space-between; align-items: center; max-width: 1200px; margin: 0 auto; }
        .image-info { flex: 1; }
        .image-title { font-size: 16px; font-weight: 500; margin-bottom: 5px; word-break: break-all; }
        .image-meta { font-size: 12px; color: #ccc; word-break: break-all; }
        .controls { display: flex; gap: 10px; }
        .btn { padding: 8px 16px; border: none; border-radius: 4px; cursor: pointer; text-decoration: none; display: inline-block; font-size: 14px; }
        .btn-primary { background: #4CAF50; color: white; }
        .btn-secondary { background: #666; color: white; }
        .btn:hover { opacity: 0.8; }
        .image-container { 
            flex: 1; 
            display: flex; 
            justify-content: center; 
            align-items: center; 
            padding-top: 80px;
            position: relative;
        }
        .image-display { 
            max-width: calc(100vw - 40px); 
            max-height: calc(100vh - 120px); 
            object-fit: contain; 
            cursor: zoom-in;
            transition: transform 0.3s ease;
        }
        .image-display.zoomed { 
            cursor: zoom-out; 
            transform: scale(2); 
        }
        .status-bar { 
            position: fixed; 
            bottom: 0; 
            left: 0; 
            right: 0; 
            background: rgba(0,0,0,0.8); 
            padding: 10px 20px; 
            text-align: center; 
            font-size: 12px; 
            color: #ccc;
            backdrop-filter: blur(10px);
        }
        .loading { 
            position: absolute; 
            top: 50%; 
            left: 50%; 
            transform: translate(-50%, -50%); 
            font-size: 16px; 
        }
        @media (max-width: 768px) {
            .header-content { flex-direction: column; gap: 10px; text-align: center; }
            .image-title { font-size: 14px; }
            .image-meta { font-size: 11px; }
            .btn { padding: 6px 12px; font-size: 12px; }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <div class="header-content">
                <div class="image-info">
                    <div class="image-title">` + fileName + `</div>
                    <div class="image-meta">文件大小: ` + fmt.Sprintf("%.2f MB", fileSizeMB) + ` • 路径: ` + filePath + `</div>
                </div>
                <div class="controls">
                    <a href="/file/` + url.QueryEscape(filePath) + `?download=1" class="btn btn-primary" download>下载图片</a>
                    <button class="btn btn-secondary" onclick="window.close()">关闭窗口</button>
                </div>
            </div>
        </div>
        
        <div class="image-container">
            <div class="loading" id="loading">加载中...</div>
            <img class="image-display" id="imageDisplay" src="/file/` + url.QueryEscape(filePath) + `" 
                 alt="` + fileName + `" 
                 onload="imageLoaded()" 
                 onerror="imageError()"
                 onclick="toggleZoom()"
                 style="display: none;">
        </div>
        
        <div class="status-bar" id="statusBar">
            点击图片可以放大/缩小 • 使用ESC键关闭窗口
        </div>
    </div>

    <script>
        let isZoomed = false;
        
        function imageLoaded() {
            const img = document.getElementById('imageDisplay');
            const loading = document.getElementById('loading');
            const statusBar = document.getElementById('statusBar');
            
            loading.style.display = 'none';
            img.style.display = 'block';
            
            // 显示图片信息
            const naturalWidth = img.naturalWidth;
            const naturalHeight = img.naturalHeight;
            const displayWidth = img.clientWidth;
            const displayHeight = img.clientHeight;
            
            statusBar.innerHTML = '原始尺寸: ' + naturalWidth + ' × ' + naturalHeight + ' • 显示尺寸: ' + displayWidth + ' × ' + displayHeight + ' • 点击放大/缩小 • ESC键关闭';
            
            console.log('图片加载完成:', '` + filePath + `', naturalWidth + 'x' + naturalHeight);
        }
        
        function imageError() {
            const loading = document.getElementById('loading');
            loading.innerHTML = '图片加载失败';
            console.error('图片加载失败:', '` + filePath + `');
        }
        
        function toggleZoom() {
            const img = document.getElementById('imageDisplay');
            isZoomed = !isZoomed;
            
            if (isZoomed) {
                img.classList.add('zoomed');
            } else {
                img.classList.remove('zoomed');
            }
        }
        
        // 键盘事件处理
        document.addEventListener('keydown', function(e) {
            if (e.key === 'Escape') {
                window.close();
            }
            if (e.key === ' ' || e.key === 'Enter') {
                e.preventDefault();
                toggleZoom();
            }
        });
        
        // 阻止右键菜单（可选）
        document.addEventListener('contextmenu', function(e) {
            e.preventDefault();
        });
        
        console.log('图片查看器初始化完成:', '` + fileName + `');
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(tmpl))
}

// 文本查看器页面处理器
func textViewerHandler(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Path[10:] // 去掉 "/textview/" 前缀

	// 多次URL解码以确保正确处理
	for i := 0; i < 3; i++ {
		if decoded, err := url.QueryUnescape(filePath); err == nil {
			filePath = decoded
		} else {
			break
		}
	}

	// 替换正斜杠为反斜杠（Windows路径）
	filePath = strings.ReplaceAll(filePath, "/", "\\")

	log.Printf("文本查看器请求: %s，来源IP: %s", filePath, r.RemoteAddr)

	// 检查文件是否存在
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("文本文件不存在: %s", filePath)
			http.Error(w, "文本文件不存在", http.StatusNotFound)
		} else {
			log.Printf("访问文件失败: %s, 错误: %v", filePath, err)
			http.Error(w, "访问文件失败: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if fileInfo.IsDir() {
		http.Error(w, "不能查看文件夹", http.StatusBadRequest)
		return
	}

	// 检查文件是否为文本文件
	ext := strings.ToLower(filepath.Ext(filePath))
	if !isTextFile(ext) {
		log.Printf("非文本文件: %s", filePath)
		http.Error(w, "不是文本文件", http.StatusBadRequest)
		return
	}

	fileName := filepath.Base(filePath)
	fileSizeMB := float64(fileInfo.Size()) / (1024 * 1024)

	// 检查文件大小
	const maxFileSize = 10 * 1024 * 1024 // 10MB
	if fileInfo.Size() > maxFileSize {
		http.Error(w, "文件过大，无法查看", http.StatusBadRequest)
		return
	}

	// 读取文件内容
	content, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("读取文本文件失败: %s, 错误: %v", filePath, err)
		http.Error(w, "读取文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 检测编码并转换
	contentStr := detectAndConvertEncoding(content)
	encoding := detectEncoding(content)
	lines := strings.Split(contentStr, "\n")
	lineCount := len(lines)

	// 获取语法高亮的语言类型
	language := getLanguageFromExtension(ext)

	// 转义HTML内容
	escapedContent := escapeHtml(contentStr)

	tmpl := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>文本查看器 - ` + fileName + `</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: 'Consolas', 'Monaco', 'Courier New', monospace; background: #1e1e1e; color: #d4d4d4; line-height: 1.5; }
        .container { width: 100vw; height: 100vh; display: flex; flex-direction: column; }
        .header { background: rgba(30, 30, 30, 0.95); padding: 15px 20px; border-bottom: 1px solid #333; position: sticky; top: 0; z-index: 1000; }
        .header-content { display: flex; justify-content: space-between; align-items: center; max-width: 1200px; margin: 0 auto; }
        .file-info { flex: 1; }
        .file-title { font-size: 16px; font-weight: 500; margin-bottom: 5px; color: #4FC3F7; word-break: break-all; }
        .file-meta { font-size: 12px; color: #888; display: flex; gap: 20px; flex-wrap: wrap; }
        .controls { display: flex; gap: 10px; }
        .btn { padding: 8px 16px; border: none; border-radius: 4px; cursor: pointer; text-decoration: none; display: inline-block; font-size: 14px; }
        .btn-primary { background: #4CAF50; color: white; }
        .btn-secondary { background: #666; color: white; }
        .btn-info { background: #2196F3; color: white; }
        .btn:hover { opacity: 0.8; }
        .content-container { flex: 1; overflow: hidden; }
        .content-area { 
            flex: 1; 
            overflow: auto; 
            padding: 20px; 
            white-space: pre-wrap; 
            font-size: 14px;
            word-break: break-word;
        }
        .status-bar { 
            background: #007ACC; 
            color: white; 
            padding: 8px 20px; 
            text-align: center; 
            font-size: 12px; 
            display: flex;
            justify-content: space-between;
            align-items: center;
        }
        .language-info { font-weight: 500; }
        .search-box { 
            position: fixed; 
            top: 70px; 
            right: 20px; 
            background: #333; 
            padding: 10px; 
            border-radius: 4px; 
            display: none;
            box-shadow: 0 4px 12px rgba(0,0,0,0.5);
        }
        .search-input { 
            padding: 6px 10px; 
            border: 1px solid #555; 
            background: #2d2d2d; 
            color: white; 
            border-radius: 3px; 
            font-size: 14px;
        }
        .search-input:focus { outline: none; border-color: #007ACC; }
        .highlight { background-color: yellow; color: black; }
        
        @media (max-width: 768px) {
            .header-content { flex-direction: column; gap: 10px; }
            .file-meta { font-size: 11px; gap: 10px; }
            .btn { padding: 6px 12px; font-size: 12px; }
            .content-area { padding: 15px; font-size: 13px; }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <div class="header-content">
                <div class="file-info">
                    <div class="file-title">` + fileName + `</div>
                    <div class="file-meta">
                        <span>大小: ` + fmt.Sprintf("%.2f MB", fileSizeMB) + `</span>
                        <span>行数: ` + strconv.Itoa(lineCount) + `</span>
                        <span>编码: ` + encoding + `</span>
                        <span>语言: ` + language + `</span>
                    </div>
                </div>
                <div class="controls">
                    <button class="btn btn-info" onclick="toggleSearch()">搜索</button>
                    <button class="btn btn-secondary" onclick="selectAll()">全选</button>
                    <a href="/file/` + url.QueryEscape(filePath) + `?download=1" class="btn btn-primary" download>下载</a>
                    <button class="btn btn-secondary" onclick="window.close()">关闭</button>
                </div>
            </div>
        </div>
        
        <div class="search-box" id="searchBox">
            <input type="text" class="search-input" id="searchInput" placeholder="输入搜索内容..." onkeyup="performSearch()" oninput="performSearch()">
        </div>
        
        <div class="content-container">
            <div class="content-area" id="contentArea">` + escapedContent + `</div>
        </div>
        
        <div class="status-bar">
            <div class="language-info">` + language + ` • ` + encoding + `</div>
            <div>` + filePath + `</div>
            <div>` + strconv.Itoa(lineCount) + ` 行 • ` + fmt.Sprintf("%.2f MB", fileSizeMB) + `</div>
        </div>
    </div>

    <script>
        const originalContent = document.getElementById('contentArea').textContent;
        const lines = originalContent.split('\n');
        const lineCount = lines.length;
        
        // 行号功能已移除，专注于内容显示
        
        // 切换搜索框
        function toggleSearch() {
            const searchBox = document.getElementById('searchBox');
            const searchInput = document.getElementById('searchInput');
            
            if (searchBox.style.display === 'none' || !searchBox.style.display) {
                searchBox.style.display = 'block';
                searchInput.focus();
            } else {
                searchBox.style.display = 'none';
                clearHighlight();
            }
        }
        
        // 执行搜索
        function performSearch() {
            const searchInput = document.getElementById('searchInput');
            const contentArea = document.getElementById('contentArea');
            const query = searchInput.value.trim();
            
            if (!query) {
                clearHighlight();
                return;
            }
            
            if (query.length < 2) return;
            
            // 清除之前的高亮并添加新高亮
            const regex = new RegExp(escapeRegExp(query), 'gi');
            const highlightedContent = originalContent.replace(regex, '<span class="highlight">$&</span>');
            contentArea.innerHTML = highlightedContent;
        }
        
        // 清除高亮
        function clearHighlight() {
            const contentArea = document.getElementById('contentArea');
            contentArea.textContent = originalContent;
        }
        
        // 全选文本
        function selectAll() {
            const contentArea = document.getElementById('contentArea');
            const range = document.createRange();
            range.selectNodeContents(contentArea);
            const selection = window.getSelection();
            selection.removeAllRanges();
            selection.addRange(range);
        }
        
        // 转义正则表达式特殊字符
        function escapeRegExp(string) {
            return string.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
        }
        
        // 键盘快捷键
        document.addEventListener('keydown', function(e) {
            if (e.key === 'Escape') {
                const searchBox = document.getElementById('searchBox');
                if (searchBox.style.display === 'block') {
                    toggleSearch();
                } else {
                    window.close();
                }
            }
            if (e.ctrlKey && e.key === 'f') {
                e.preventDefault();
                toggleSearch();
            }
            if (e.ctrlKey && e.key === 'a') {
                e.preventDefault();
                selectAll();
            }
        });
        
        // 初始化
        window.onload = function() {
            console.log('文本查看器初始化完成:', '` + fileName + `', lineCount + ' 行');
        };
        
        // 滚动功能已简化
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(tmpl))
}

// 检查是否为文本文件
func isTextFile(ext string) bool {
	textExts := []string{
		// 基本文本文件
		".txt", ".log", ".md", ".readme", ".conf", ".config", ".ini", ".cfg",
		// 编程语言文件
		".c", ".cpp", ".cc", ".cxx", ".h", ".hpp", ".hxx",
		".cs", ".vb", ".fs",
		".java", ".kt", ".scala", ".groovy",
		".js", ".ts", ".jsx", ".tsx", ".mjs", ".cjs",
		".py", ".pyw", ".pyi", ".pyx", ".pxd",
		".rb", ".rake", ".gemfile",
		".php", ".phtml", ".php3", ".php4", ".php5", ".phps",
		".go", ".mod", ".sum",
		".rs", ".toml",
		".swift", ".m", ".mm",
		".lua", ".pl", ".pm", ".t",
		".sh", ".bash", ".zsh", ".fish", ".bat", ".cmd", ".ps1",
		".r", ".R", ".rmd",
		".matlab", ".m",
		// 标记语言和数据格式
		".html", ".htm", ".xhtml", ".xml", ".xsl", ".xsd",
		".json", ".jsonc", ".yaml", ".yml", ".toml",
		".css", ".scss", ".sass", ".less", ".styl",
		".sql", ".mysql", ".psql", ".sqlite",
		// 配置和脚本文件
		".dockerfile", ".dockerignore", ".gitignore", ".gitattributes",
		".makefile", ".mk", ".cmake", ".ninja",
		".gradle", ".maven", ".pom", ".ant",
		".properties", ".env", ".htaccess",
		// 其他常见文本格式
		".csv", ".tsv", ".sv", ".tex", ".bib",
		".vim", ".vimrc", ".emacs",
		".reg", ".inf", ".desktop",
	}

	for _, textExt := range textExts {
		if ext == textExt {
			return true
		}
	}

	// 检查无扩展名的常见文件名
	fileName := strings.ToLower(filepath.Base(ext))
	commonTextFiles := []string{
		"makefile", "dockerfile", "jenkinsfile", "vagrantfile",
		"readme", "license", "changelog", "authors", "contributors",
		"install", "news", "todo", "copying", "manifest",
	}

	for _, name := range commonTextFiles {
		if fileName == name {
			return true
		}
	}

	return false
}

// 根据文件扩展名获取语言类型
func getLanguageFromExtension(ext string) string {
	languageMap := map[string]string{
		".c":   "C",
		".cpp": "C++", ".cc": "C++", ".cxx": "C++",
		".h": "C/C++", ".hpp": "C++", ".hxx": "C++",
		".cs":    "C#",
		".vb":    "Visual Basic",
		".fs":    "F#",
		".java":  "Java",
		".kt":    "Kotlin",
		".scala": "Scala",
		".js":    "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript",
		".ts":  "TypeScript",
		".jsx": "React", ".tsx": "React",
		".py": "Python", ".pyw": "Python", ".pyi": "Python",
		".rb":  "Ruby",
		".php": "PHP", ".phtml": "PHP",
		".go":    "Go",
		".rs":    "Rust",
		".swift": "Swift",
		".lua":   "Lua",
		".pl":    "Perl", ".pm": "Perl",
		".sh": "Shell", ".bash": "Bash", ".zsh": "Zsh",
		".bat": "Batch", ".cmd": "Batch",
		".ps1": "PowerShell",
		".r":   "R", ".R": "R",
		".html": "HTML", ".htm": "HTML", ".xhtml": "HTML",
		".xml": "XML", ".xsl": "XML", ".xsd": "XML",
		".css": "CSS", ".scss": "SCSS", ".sass": "Sass", ".less": "Less",
		".json": "JSON", ".jsonc": "JSON",
		".yaml": "YAML", ".yml": "YAML",
		".toml": "TOML",
		".sql":  "SQL", ".mysql": "SQL", ".psql": "SQL",
		".md":  "Markdown",
		".log": "Log",
		".txt": "Text",
		".ini": "INI", ".cfg": "Config", ".conf": "Config",
		".dockerfile": "Dockerfile",
		".makefile":   "Makefile", ".mk": "Makefile",
	}

	if lang, exists := languageMap[ext]; exists {
		return lang
	}

	return "Text"
}

// HTML转义函数
func escapeHtml(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&#x27;")
	return s
}
