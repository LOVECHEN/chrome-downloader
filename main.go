// Chrome Multi-Channel Downloader
// Go 1.25+ | 纯标准库
//
// 从 Google 官方 API 查询 Chrome 各渠道最新版本号，
// 并从 dl.google.com 下载对应平台的离线安装包。
//
// 用法：
//   chrome-downloader -info                      # 仅查看版本（默认 mac）
//   chrome-downloader                            # 下载 macOS dmg（所有渠道）
//   chrome-downloader all                        # 全平台下载
//   chrome-downloader win64                      # 指定平台
//   chrome-downloader -channel stable            # 仅 stable 渠道（mac）
//   chrome-downloader -channel beta all          # beta 渠道 + 全平台

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─────────────────── 终端颜色 ───────────────────

const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cRed    = "\033[31m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cCyan   = "\033[36m"
	cDim    = "\033[2m"
)

// ─────────────────── 数据类型 ───────────────────

type Platform struct {
	ID   string // CLI 标识: win64, win32, mac, linux-deb, linux-rpm
	Name string // 显示名称
	API  string // versionhistory API 平台名
}

type Channel struct {
	ID   string // CLI 标识: stable, beta, dev
	Name string // 显示名称
}

type VersionRelease struct {
	Version string `json:"version"`
	Serving struct {
		StartTime string `json:"startTime"`
	} `json:"serving"`
	Fraction float64 `json:"fraction"`
	Pinnable bool    `json:"pinnable"`
}

type VersionResponse struct {
	Releases []VersionRelease `json:"releases"`
}

type DownloadResult struct {
	Platform string
	Channel  string
	Version  string
	Filename string
	Size     int64
	Err      error
}

// ─────────────────── 常量配置 ───────────────────

var platforms = []Platform{
	{ID: "win64", Name: "Windows 64-bit", API: "win64"},
	{ID: "win32", Name: "Windows 32-bit", API: "win"},
	{ID: "mac", Name: "macOS (Universal)", API: "mac"},
	{ID: "linux-deb", Name: "Linux (.deb)", API: "linux"},
	{ID: "linux-rpm", Name: "Linux (.rpm)", API: "linux"},
}

var channels = []Channel{
	{ID: "stable", Name: "正式版 (Stable)"},
	{ID: "beta", Name: "测试版 (Beta)"},
	{ID: "dev", Name: "开发版 (Dev)"},
}

// 下载 URL 映射: downloadURLs[channel][platform]
var downloadURLs = map[string]map[string]string{
	"stable": {
		"win64":     "https://dl.google.com/dl/chrome/install/googlechromestandaloneenterprise64.msi",
		"win32":     "https://dl.google.com/dl/chrome/install/googlechromestandaloneenterprise.msi",
		"mac":       "https://dl.google.com/chrome/mac/universal/stable/GGRO/googlechrome.dmg",
		"linux-deb": "https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb",
		"linux-rpm": "https://dl.google.com/linux/direct/google-chrome-stable_current_x86_64.rpm",
	},
	"beta": {
		"win64":     "https://dl.google.com/chrome/install/beta/googlechromebetastandaloneenterprise64.msi",
		"win32":     "https://dl.google.com/chrome/install/beta/googlechromebetastandaloneenterprise.msi",
		"mac":       "https://dl.google.com/chrome/mac/universal/beta/googlechromebeta.dmg",
		"linux-deb": "https://dl.google.com/linux/direct/google-chrome-beta_current_amd64.deb",
		"linux-rpm": "https://dl.google.com/linux/direct/google-chrome-beta_current_x86_64.rpm",
	},
	"dev": {
		"win64":     "https://dl.google.com/chrome/install/dev/googlechromedevstandaloneenterprise64.msi",
		"win32":     "https://dl.google.com/chrome/install/dev/googlechromedevstandaloneenterprise.msi",
		"mac":       "https://dl.google.com/chrome/mac/universal/dev/googlechromedev.dmg",
		"linux-deb": "https://dl.google.com/linux/direct/google-chrome-unstable_current_amd64.deb",
		"linux-rpm": "https://dl.google.com/linux/direct/google-chrome-unstable_current_x86_64.rpm",
	},
}

// ─────────────────── 版本 API ───────────────────

const versionAPIBase = "https://versionhistory.googleapis.com/v1/chrome/platforms/%s/channels/%s/versions/all/releases?filter=endtime=none&order_by=version%%20desc"

var httpClient = &http.Client{Timeout: 30 * time.Second}

// 查询指定平台+渠道的最新版本号
func fetchVersion(apiPlatform, channel string) (string, string, error) {
	url := fmt.Sprintf(versionAPIBase, apiPlatform, channel)
	resp, err := httpClient.Get(url)
	if err != nil {
		return "", "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var vr VersionResponse
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return "", "", fmt.Errorf("解析响应失败: %w", err)
	}

	if len(vr.Releases) == 0 {
		return "", "", fmt.Errorf("未找到版本信息")
	}

	// 优先选 fraction=1 && pinnable=true 的版本（全量推送）
	for _, r := range vr.Releases {
		if r.Fraction >= 1 && r.Pinnable {
			date := ""
			if t, err := time.Parse(time.RFC3339Nano, r.Serving.StartTime); err == nil {
				date = t.Format("2006-01-02")
			}
			return r.Version, date, nil
		}
	}
	// fallback: 取第一个
	r := vr.Releases[0]
	date := ""
	if t, err := time.Parse(time.RFC3339Nano, r.Serving.StartTime); err == nil {
		date = t.Format("2006-01-02")
	}
	return r.Version, date, nil
}

// 批量查询版本，返回 map[channel][platform] -> version
type VersionInfo struct {
	Version string
	Date    string
}

func fetchAllVersions(selectedPlatforms []Platform, selectedChannels []Channel) map[string]map[string]VersionInfo {
	result := make(map[string]map[string]VersionInfo)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// 去重：linux-deb 和 linux-rpm 使用相同 API platform
	type query struct {
		channel     string
		apiPlatform string
		platforms   []string // 关联的 platform IDs
	}

	var queries []query
	for _, ch := range selectedChannels {
		seen := make(map[string][]string)
		for _, p := range selectedPlatforms {
			seen[p.API] = append(seen[p.API], p.ID)
		}
		for api, pids := range seen {
			queries = append(queries, query{channel: ch.ID, apiPlatform: api, platforms: pids})
		}
	}

	for _, q := range queries {
		wg.Add(1)
		go func(q query) {
			defer wg.Done()
			ver, date, err := fetchVersion(q.apiPlatform, q.channel)
			if err != nil {
				fmt.Fprintf(os.Stderr, cYellow+"⚠ 查询 %s/%s 失败: %v\n"+cReset, q.channel, q.apiPlatform, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if result[q.channel] == nil {
				result[q.channel] = make(map[string]VersionInfo)
			}
			for _, pid := range q.platforms {
				result[q.channel][pid] = VersionInfo{Version: ver, Date: date}
			}
		}(q)
	}
	wg.Wait()
	return result
}

// ─────────────────── 下载引擎 ───────────────────

type progressWriter struct {
	total      int64
	written    int64
	label      string
	lastUpdate time.Time
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.written += int64(n)
	now := time.Now()
	if now.Sub(pw.lastUpdate) >= 200*time.Millisecond || pw.written == pw.total {
		pw.lastUpdate = now
		if pw.total > 0 {
			pct := float64(pw.written) / float64(pw.total) * 100
			bar := renderBar(pct, 30)
			fmt.Fprintf(os.Stderr, "\r   %s %s %5.1f%% [%s / %s]",
				pw.label, bar, pct,
				humanSize(pw.written), humanSize(pw.total))
		} else {
			fmt.Fprintf(os.Stderr, "\r   %s %s 已下载",
				pw.label, humanSize(pw.written))
		}
	}
	return n, nil
}

func renderBar(pct float64, width int) string {
	filled := int(pct / 100 * float64(width))
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func humanSize(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func downloadFile(url, destPath, label string) (int64, error) {
	// 创建目录
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return 0, err
	}

	dlClient := &http.Client{Timeout: 30 * time.Minute}

	// 本地文件已存在时，通过 HEAD 请求对比文件大小
	if localInfo, err := os.Stat(destPath); err == nil && localInfo.Size() > 0 {
		remoteSize := getRemoteSize(dlClient, url)
		if remoteSize > 0 && localInfo.Size() == remoteSize {
			fmt.Fprintf(os.Stderr, "   %s 已存在且大小一致 (%s)，跳过\n", label, humanSize(localInfo.Size()))
			return localInfo.Size(), nil
		}
		if remoteSize > 0 {
			fmt.Fprintf(os.Stderr, "   %s 大小不一致 (本地 %s / 远程 %s)，重新下载\n",
				label, humanSize(localInfo.Size()), humanSize(remoteSize))
		}
		// remoteSize <= 0 说明 HEAD 请求未返回 Content-Length，继续下载覆盖
	}

	resp, err := dlClient.Get(url)
	if err != nil {
		return 0, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("HTTP %d (URL 可能无效)", resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	pw := &progressWriter{
		total: resp.ContentLength,
		label: label,
	}

	written, err := io.Copy(f, io.TeeReader(resp.Body, pw))
	fmt.Fprintln(os.Stderr) // 换行
	return written, err
}

// HEAD 请求获取远程文件大小
func getRemoteSize(client *http.Client, url string) int64 {
	resp, err := client.Head(url)
	if err != nil || resp.StatusCode != 200 {
		return -1
	}
	resp.Body.Close()
	return resp.ContentLength
}

// ─────────────────── CLI 主逻辑 ───────────────────

func main() {
	channelFlag := flag.String("channel", "all", "渠道: stable, beta, dev, all")
	outputFlag := flag.String("output", "./downloads", "下载目录")
	infoFlag := flag.Bool("info", false, "仅显示版本信息，不下载")
	workersFlag := flag.Int("workers", 3, "并发下载数")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "用法: chrome-downloader [选项] [平台|命令]\n\n")
		fmt.Fprintf(os.Stderr, "平台: win64, win32, mac, linux-deb, linux-rpm, all (默认: mac)\n\n")
		fmt.Fprintf(os.Stderr, "命令:\n")
		fmt.Fprintf(os.Stderr, "  clean   清除下载目录中所有非 .dmg 文件\n")
		fmt.Fprintf(os.Stderr, "  help    显示帮助\n\n")
		fmt.Fprintf(os.Stderr, "选项:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	// 平台从位置参数获取，默认 mac
	platformArg := "mac"
	if flag.NArg() > 0 {
		platformArg = flag.Arg(0)
	}

	switch platformArg {
	case "help":
		flag.Usage()
		os.Exit(0)
	case "clean":
		cleanDownloads(*outputFlag)
		os.Exit(0)
	}

	// 解析选择
	selectedPlatforms := filterPlatforms(platformArg)
	selectedChannels := filterChannels(*channelFlag)

	if len(selectedPlatforms) == 0 {
		fatal("未知平台: %s\n可选: win64, win32, mac, linux-deb, linux-rpm, all", platformArg)
	}
	if len(selectedChannels) == 0 {
		fatal("未知渠道: %s\n可选: stable, beta, dev, all", *channelFlag)
	}

	// Banner
	fmt.Println()
	fmt.Println(cCyan + cBold + "╔══════════════════════════════════════════════╗" + cReset)
	fmt.Println(cCyan + cBold + "║    Chrome Multi-Channel Downloader           ║" + cReset)
	fmt.Println(cCyan + cBold + "╚══════════════════════════════════════════════╝" + cReset)
	fmt.Println()

	// 查询版本
	fmt.Println(cDim + "⏳ 正在查询最新版本号..." + cReset)
	versions := fetchAllVersions(selectedPlatforms, selectedChannels)
	fmt.Println()

	if len(versions) == 0 {
		fatal("未能获取任何版本信息")
	}

	// 显示版本信息表
	printVersionTable(versions, selectedPlatforms, selectedChannels)

	if *infoFlag {
		return
	}

	// 构建下载任务
	type task struct {
		channel  Channel
		platform Platform
		version  string
		url      string
		dest     string
		label    string
	}

	var tasks []task
	for _, ch := range selectedChannels {
		chVersions, ok := versions[ch.ID]
		if !ok {
			continue
		}
		for _, p := range selectedPlatforms {
			vi, ok := chVersions[p.ID]
			if !ok {
				continue
			}
			url, ok := downloadURLs[ch.ID][p.ID]
			if !ok {
				continue
			}
			// 确定文件名和路径
			ext := filepath.Ext(url)
			filename := fmt.Sprintf("chrome-%s-%s%s", ch.ID, p.ID, ext)
			dirName := fmt.Sprintf("%s-%s", ch.ID, vi.Version)
			dest := filepath.Join(*outputFlag, dirName, filename)
			label := fmt.Sprintf("[%s/%s]", ch.ID, p.ID)

			tasks = append(tasks, task{
				channel:  ch,
				platform: p,
				version:  vi.Version,
				url:      url,
				dest:     dest,
				label:    label,
			})
		}
	}

	if len(tasks) == 0 {
		fatal("无可下载的目标")
	}

	fmt.Printf(cBold+"📦 共 %d 个下载任务，并发数 %d\n\n"+cReset, len(tasks), *workersFlag)

	// 并发下载
	sem := make(chan struct{}, *workersFlag)
	var wg sync.WaitGroup
	var successCount, failCount atomic.Int32
	results := make([]DownloadResult, len(tasks))

	for i, t := range tasks {
		wg.Add(1)
		go func(idx int, t task) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			fmt.Fprintf(os.Stderr, cCyan+"▶ 开始下载 %s %s\n"+cReset, t.label, t.version)
			size, err := downloadFile(t.url, t.dest, t.label)
			if err != nil {
				failCount.Add(1)
				fmt.Fprintf(os.Stderr, cRed+"   ✗ %s 失败: %v\n"+cReset, t.label, err)
			} else {
				successCount.Add(1)
				fmt.Fprintf(os.Stderr, cGreen+"   ✓ %s 完成\n"+cReset, t.label)
			}
			results[idx] = DownloadResult{
				Platform: t.platform.ID,
				Channel:  t.channel.ID,
				Version:  t.version,
				Filename: t.dest,
				Size:     size,
				Err:      err,
			}
		}(i, t)
	}
	wg.Wait()

	// 打印摘要
	fmt.Println()
	fmt.Println(cBold + "═══════════════════ 下载摘要 ═══════════════════" + cReset)
	fmt.Println()

	var totalSize int64
	for _, r := range results {
		status := cGreen + "✓" + cReset
		sizeStr := humanSize(r.Size)
		if r.Err != nil {
			status = cRed + "✗" + cReset
			sizeStr = r.Err.Error()
		} else {
			totalSize += r.Size
		}
		fmt.Printf("  %s  %-8s %-10s %-20s %s\n",
			status, r.Channel, r.Platform, r.Version, sizeStr)
	}

	fmt.Println()
	s := successCount.Load()
	f := failCount.Load()
	fmt.Printf("  成功: %s%d%s  失败: %s%d%s  总大小: %s\n",
		cGreen, s, cReset,
		cRed, f, cReset,
		humanSize(totalSize))
	fmt.Printf("  输出目录: %s\n\n", *outputFlag)
}

// ─────────────────── 辅助函数 ───────────────────

func filterPlatforms(id string) []Platform {
	if id == "all" {
		return platforms
	}
	for _, p := range platforms {
		if p.ID == id {
			return []Platform{p}
		}
	}
	return nil
}

func filterChannels(id string) []Channel {
	if id == "all" {
		return channels
	}
	for _, c := range channels {
		if c.ID == id {
			return []Channel{c}
		}
	}
	return nil
}

func printVersionTable(versions map[string]map[string]VersionInfo, ps []Platform, cs []Channel) {
	// 表头
	fmt.Printf("  %-18s", "平台")
	for _, ch := range cs {
		fmt.Printf("  %-24s", ch.Name)
	}
	fmt.Println()

	fmt.Printf("  %-18s", strings.Repeat("─", 16))
	for range cs {
		fmt.Printf("  %-24s", strings.Repeat("─", 22))
	}
	fmt.Println()

	// 数据行
	for _, p := range ps {
		fmt.Printf("  %-18s", p.Name)
		for _, ch := range cs {
			if vi, ok := versions[ch.ID][p.ID]; ok {
				cell := fmt.Sprintf("%s (%s)", vi.Version, vi.Date)
				fmt.Printf("  %-24s", cell)
			} else {
				fmt.Printf("  %-24s", cDim+"—"+cReset)
			}
		}
		fmt.Println()
	}
	fmt.Println()
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, cRed+"❌ "+format+"\n"+cReset, args...)
	os.Exit(1)
}

// cleanDownloads 清除下载目录中所有非 .dmg 文件
func cleanDownloads(dir string) {
	var removed []string
	var totalSize int64

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".dmg" {
			size := info.Size()
			if err := os.Remove(path); err != nil {
				fmt.Fprintf(os.Stderr, cRed+"  ✗ 删除失败: %s (%v)\n"+cReset, path, err)
			} else {
				removed = append(removed, path)
				totalSize += size
				fmt.Printf("  🗑  %s (%s)\n", filepath.Base(path), humanSize(size))
			}
		}
		return nil
	})

	if err != nil {
		fatal("遍历目录失败: %v", err)
	}

	// 清理空目录
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() || path == dir {
			return nil
		}
		entries, _ := os.ReadDir(path)
		if len(entries) == 0 {
			os.Remove(path)
		}
		return nil
	})

	fmt.Println()
	if len(removed) == 0 {
		fmt.Println(cGreen + "✓ 没有需要清理的文件" + cReset)
	} else {
		fmt.Printf(cGreen+"✓ 已清理 %d 个文件，释放 %s\n"+cReset, len(removed), humanSize(totalSize))
	}
}
