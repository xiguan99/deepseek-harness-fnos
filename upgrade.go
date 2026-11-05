package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	dshPackageName = "@deepseek-ai/dsh"
	nodeBinDir     = "/var/apps/nodejs_v24/target/bin"
)

func findBin(preferred, fallback string) string {
	if _, err := os.Stat(preferred); err == nil {
		return preferred
	}
	if p, err := exec.LookPath(fallback); err == nil {
		return p
	}
	return preferred
}

func nodeBin() string { return findBin(filepath.Join(nodeBinDir, "node"), "node") }
func npmBin() string  { return findBin(filepath.Join(nodeBinDir, "npm"), "npm") }
func pnpmBin() string { return filepath.Join(globalPnpmDir, "node_modules", ".bin", "pnpm") }

var pnpmInstallMu sync.Mutex

// installPnpm 确保插件管理所需的 pnpm 运行环境就绪
func installPnpm() error {
	if _, err := os.Stat(pnpmBin()); err == nil {
		return nil
	}
	pnpmInstallMu.Lock()
	defer pnpmInstallMu.Unlock()

	// 二次校验防并发重复初始化
	if _, err := os.Stat(pnpmBin()); err == nil {
		return nil
	}
	pnpmDir := globalPnpmDir
	_ = os.MkdirAll(pnpmDir, 0755)

	cfg := GetConfig()
	args := []string{"install", "pnpm", "--no-audit", "--no-fund", "--registry=" + cfg.GetNpmRegistry()}
	cmd := exec.Command(npmBin(), args...)
	cmd.Dir = pnpmDir
	cmd.Stdout = NewLogWriterInfo()
	cmd.Stderr = NewLogWriterWarn()
	if cfg.NetworkProxy != "" && strings.Contains(cfg.GetNpmRegistry(), "npmjs.org") {
		cmd.Env = append(os.Environ(),
			"HTTP_PROXY="+cfg.NetworkProxy,
			"HTTPS_PROXY="+cfg.NetworkProxy,
			"http_proxy="+cfg.NetworkProxy,
			"https_proxy="+cfg.NetworkProxy,
		)
	}
	LogInfo("[升级] 正在初始化 pnpm 环境...")
	if err := cmd.Run(); err != nil {
		LogWarning("[升级] 安装 pnpm 失败: %s", err)
		return fmt.Errorf("安装 pnpm 失败: %w", err)
	}
	LogInfo("[升级] pnpm 环境初始化成功: %s", pnpmBin())
	return nil
}

// CheckUpdateResult 检查更新返回结构
type CheckUpdateResult struct {
	HasUpdate      bool   `json:"has_update"`
	CurrentVersion string `json:"current_version"`
	RemoteVersion  string `json:"remote_version"`
	Message        string `json:"message"`
}

type npmPackageInfo struct {
	DistTags map[string]string `json:"dist-tags"`
	Time     map[string]string `json:"time"`
}

// fetchRemoteNpmInfo 查询包的发布时间与 Tag 元数据
func fetchRemoteNpmInfo(pkgName string) (*npmPackageInfo, error) {
	cfg := GetConfig()
	reg := strings.TrimRight(cfg.GetNpmRegistry(), "/")
	apiURL := fmt.Sprintf("%s/%s", reg, pkgName)

	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if cfg.NetworkProxy != "" && strings.Contains(reg, "npmjs.org") {
		if pURL, err := url.Parse(cfg.NetworkProxy); err == nil {
			transport.Proxy = http.ProxyURL(pURL)
		}
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "DeepSeek-Harness-FNOS")

	resp, err := client.Do(req)
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		var info npmPackageInfo
		if err := json.NewDecoder(resp.Body).Decode(&info); err == nil && (len(info.Time) > 0 || len(info.DistTags) > 0) {
			return &info, nil
		}
	}
	if resp != nil {
		_ = resp.Body.Close()
	}

	cmdArgs := []string{"view", pkgName, "dist-tags", "time", "--json", "--registry=" + reg}
	cmd := exec.Command(npmBin(), cmdArgs...)
	if cfg.NetworkProxy != "" && strings.Contains(reg, "npmjs.org") {
		cmd.Env = append(os.Environ(),
			"HTTP_PROXY="+cfg.NetworkProxy,
			"HTTPS_PROXY="+cfg.NetworkProxy,
			"http_proxy="+cfg.NetworkProxy,
			"https_proxy="+cfg.NetworkProxy,
		)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("获取 NPM 版本元数据失败: %w", err)
	}

	var info npmPackageInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("解析 NPM 版本元数据失败: %w", err)
	}
	return &info, nil
}

// resolveTargetVersion 依据更新渠道解析目标版本：
// - stable: 取 NPM 的 dist-tags.latest
// - next:   取 NPM 的 dist-tags.next（官方预览线；缺失时回落最高版本）
// - alpha:  取 NPM 的 dist-tags.alpha（内测线；缺失时回落最高版本）
// - latest: 取 NPM 所有版本中的最高版本（历史口径，等价预览线）
func resolveTargetVersion(info *npmPackageInfo, channel string) string {
	if info == nil {
		return ""
	}

	switch channel {
	case "stable":
		return distTagVersion(info, "latest")
	case "next":
		if v := distTagVersion(info, "next"); v != "" {
			return v
		}
	case "alpha":
		if v := distTagVersion(info, "alpha"); v != "" {
			return v
		}
	}

	var maxVer string
	for ver := range info.Time {
		if ver == "modified" || ver == "created" {
			continue
		}
		cleanVer := strings.TrimPrefix(strings.TrimSpace(ver), "v")
		if maxVer == "" || CompareSemver(cleanVer, maxVer) > 0 {
			maxVer = cleanVer
		}
	}
	return maxVer
}

// distTagVersion 读取指定 dist-tag 的版本号，缺失时返回空串。
func distTagVersion(info *npmPackageInfo, tag string) string {
	if info == nil || info.DistTags == nil {
		return ""
	}
	if v, ok := info.DistTags[tag]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimPrefix(strings.TrimSpace(v), "v")
	}
	return ""
}

func formatVersionTag(ver string) string {
	ver = strings.TrimPrefix(strings.TrimSpace(ver), "v")
	if ver != "" {
		return "v" + ver
	}
	return "-"
}

// CheckUpdate 轻量快速检查远程 NPM 是否有更新，带 10s 超时，绝不中断正在运行的服务
func CheckUpdate() (*CheckUpdateResult, error) {
	currentVersion := readVersion()
	tagCurrent := formatVersionTag(currentVersion)
	reg := strings.TrimRight(GetConfig().GetNpmRegistry(), "/")
	channel := GetConfig().GetUpdateChannel()
	LogInfo("[升级] 开始检查远程版本更新 (包名=%s, 当前版本=%s, 渠道=%s, 源=%s)", dshPackageName, tagCurrent, channel, reg)

	info, err := fetchRemoteNpmInfo(dshPackageName)
	if err != nil {
		LogWarning("[升级] 检查远程版本更新失败 (包名=%s): %s", dshPackageName, err)
		return nil, fmt.Errorf("检查更新失败: %w", err)
	}

	remoteVersion := resolveTargetVersion(info, channel)
	if remoteVersion == "" {
		remoteVersion = currentVersion
	}
	tagRemote := formatVersionTag(remoteVersion)

	hasUpdate := false
	if currentVersion == "" {
		hasUpdate = true
	} else if remoteVersion != "" && CompareSemver(remoteVersion, currentVersion) > 0 {
		hasUpdate = true
	}

	var msg string
	if hasUpdate {
		if currentVersion == "" {
			msg = fmt.Sprintf("发现可用版本 [ %s ]，需初始化安装", tagRemote)
		} else {
			msg = fmt.Sprintf("发现新版本 [ %s → %s ]", tagCurrent, tagRemote)
		}
		LogInfo("[升级] 检查更新完成: %s", msg)
	} else {
		msg = fmt.Sprintf("当前已是最新版本 [ %s ]", tagCurrent)
		LogInfo("[升级] 检查更新完成: %s", msg)
	}

	return &CheckUpdateResult{
		HasUpdate:      hasUpdate,
		CurrentVersion: currentVersion,
		RemoteVersion:  remoteVersion,
		Message:        msg,
	}, nil
}

// Upgrade 触发在线版本升级
func Upgrade() {
	state.SetStatus(StatusBuilding, "正在准备更新核心服务...")
	go update(false)
}

// Rebuild 触发强制重新部署 DSH
func Rebuild() {
	state.SetStatus(StatusBuilding, "正在准备重新部署核心服务...")
	go update(true)
}

// safeRemoveAll 安全递归删除文件与目录，遇到只读权限文件时自动解除只读以彻底清理
func safeRemoveAll(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			_ = os.Chmod(p, 0777)
		}
		return nil
	})
	return os.RemoveAll(path)
}

// RepairEnvironment 恢复出厂设置：清空第三方插件与配置，重新部署纯净运行环境
func RepairEnvironment(keepPlugins bool) {
	state.SetStatus(StatusBuilding, "正在准备重置运行环境...")
	go repairEnvironment(keepPlugins)
}

func repairEnvironment(keepPlugins bool) {
	tarPath := filepath.Join(globalAppDest, "deepseek-harness.tar.gz")
	if _, err := os.Stat(tarPath); err != nil {
		LogWarning("[升级] 恢复出厂设置失败: 未检测到内置离线安装包 (%s)", tarPath)
		state.SetStatus(StatusStopped, "恢复出厂设置失败: 未检测到内置离线安装包")
		return
	}

	stopAndWait()
	if !keepPlugins {
		ResetAllProfilePatches()
	}

	state.SetStatus(StatusBuilding, "正在清空工作区并还原运行环境...")
	LogInfo("[升级] 开始重置运行环境（清理第三方插件与挂载，保留服务凭据与配置）")

	zipVer := readAppDestVersion()
	deployBuiltinPackage(tarPath, zipVer, false)
}

// installDshFromNpm 通过 npm install 安装指定版本或最新版 DSH 运行时
func installDshFromNpm(targetVersion string) error {
	_ = os.MkdirAll(runtimeDir, 0755)

	pkgSpec := dshPackageName
	if targetVersion != "" {
		pkgSpec = fmt.Sprintf("%s@%s", dshPackageName, targetVersion)
	} else if info, err := fetchRemoteNpmInfo(dshPackageName); err == nil && info != nil {
		channel := GetConfig().GetUpdateChannel()
		if bestVer := resolveTargetVersion(info, channel); bestVer != "" {
			pkgSpec = fmt.Sprintf("%s@%s", dshPackageName, bestVer)
		}
	}

	cfg := GetConfig()
	args := []string{
		"install",
		pkgSpec,
		"--no-audit",
		"--no-fund",
		"--registry=" + cfg.GetNpmRegistry(),
	}
	if globalNpmCache != "" {
		_ = os.MkdirAll(globalNpmCache, 0755)
		args = append(args, "--cache="+globalNpmCache)
	}

	LogInfo("[升级] 执行包安装命令: npm %s", strings.Join(args, " "))
	cmd := exec.Command(npmBin(), args...)
	cmd.Dir = runtimeDir
	cmd.Stdout = NewLogWriterInfo()
	cmd.Stderr = NewLogWriterWarn()

	cmdEnv := append([]string{}, os.Environ()...)
	if cfg.NetworkProxy != "" && strings.Contains(cfg.GetNpmRegistry(), "npmjs.org") {
		cmdEnv = append(cmdEnv,
			"HTTP_PROXY="+cfg.NetworkProxy,
			"HTTPS_PROXY="+cfg.NetworkProxy,
			"http_proxy="+cfg.NetworkProxy,
			"https_proxy="+cfg.NetworkProxy,
		)
	}
	cmd.Env = cmdEnv

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm install 失败: %w", err)
	}

	// 校验安装结果
	cliBinJs := filepath.Join(runtimeDir, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
	if _, err := os.Stat(cliBinJs); err != nil {
		return fmt.Errorf("npm 安装完成但入口文件缺失: %s", cliBinJs)
	}

	LogInfo("[升级] 核心运行时包安装成功: %s", pkgSpec)
	return nil
}

func update(forceRebuild bool) {
	stopAndWait()

	if forceRebuild {
		// 重新部署：安装 config.json 中记录的当前版本
		targetVer := strings.TrimPrefix(strings.TrimSpace(GetConfig().Version), "v")
		if targetVer == "" || targetVer == "-" {
			LogWarning("[升级] 重新部署失败: 配置文件中未记录有效的当前版本")
			state.SetStatus(StatusStopped, "重新部署失败: 配置文件未记录当前版本")
			return
		}

		state.SetStatus(StatusBuilding, fmt.Sprintf("正在重新部署核心服务 (v%s)...", targetVer))
		state.SetTargetVersion(targetVer)

		_ = safeRemoveAll(filepath.Join(runtimeDir, "node_modules"))

		if err := installDshFromNpm(targetVer); err != nil {
			LogWarning("[升级] 重新部署失败: %s", err)
			state.SetStatus(StatusStopped, "重新部署失败: "+err.Error())
			return
		}

		verAfter := readVersion()
		LogInfo("[升级] 核心服务重新部署成功: v%s", verAfter)
		refreshVersion()
		SetBuildTime(time.Now())
		state.SetStatus(StatusStopped, "")
		restartService()
		return
	}

	// 检查更新与在线升级：检测 NPM 上游最新发布版本
	info, err := fetchRemoteNpmInfo(dshPackageName)
	if err != nil || info == nil {
		errMsg := "获取远程版本信息失败"
		if err != nil {
			errMsg += ": " + err.Error()
		}
		LogWarning("[升级] %s", errMsg)
		state.SetStatus(StatusStopped, errMsg)
		restartService()
		return
	}

	channel := GetConfig().GetUpdateChannel()
	targetVer := resolveTargetVersion(info, channel)
	if targetVer == "" {
		LogWarning("[升级] 解析远程版本失败，无有效语义化版本")
		state.SetStatus(StatusStopped, "解析远程版本失败")
		restartService()
		return
	}

	verBefore := strings.TrimPrefix(strings.TrimSpace(GetConfig().Version), "v")
	if verBefore == "" || verBefore == "-" {
		verBefore = readVersion()
	}

	if verBefore != "" && CompareSemver(targetVer, verBefore) <= 0 {
		LogInfo("[升级] 当前运行版本 (v%s) 已高于或等于远端目标版本 (v%s)，跳过更新", verBefore, targetVer)
		state.SetStatus(StatusStopped, "")
		restartService()
		return
	}

	state.SetStatus(StatusBuilding, fmt.Sprintf("正在更新核心服务 (v%s → v%s)...", verBefore, targetVer))
	state.SetTargetVersion(targetVer)

	if err := installDshFromNpm(targetVer); err != nil {
		LogWarning("[升级] 安装更新失败: %s", err)
		state.SetStatus(StatusStopped, "更新安装失败: "+err.Error())
		return
	}

	verAfter := readVersion()
	LogInfo("[升级] 核心服务更新成功: v%s", verAfter)
	refreshVersion()
	SetBuildTime(time.Now())
	state.SetStatus(StatusStopped, "")
	restartService()
}

func extractTarGz(tarPath, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	cmd := exec.Command("tar", "--no-same-owner", "-xzf", tarPath, "-C", dst)
	cmd.Stdout = NewLogWriterInfo()
	cmd.Stderr = NewLogWriterWarn()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("解压 tar.gz 失败: %w", err)
	}
	return nil
}

func refreshVersion() {
	ver := readVersion()
	if ver == "" {
		ver = "-"
	}
	SetVersion(ver)
}

func readVersion() string {
	data, err := os.ReadFile(filepath.Join(runtimeDir, "node_modules", "@deepseek-ai", "dsh", "package.json"))
	if err != nil {
		// 备用：从运行时顶层 package.json 读取依赖版本
		if pdata, err := os.ReadFile(filepath.Join(runtimeDir, "package.json")); err == nil {
			var pkg struct {
				Dependencies map[string]string `json:"dependencies"`
			}
			if json.Unmarshal(pdata, &pkg) == nil && pkg.Dependencies != nil {
				return strings.TrimLeft(pkg.Dependencies[dshPackageName], "^~v ")
			}
		}
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return ""
	}
	return pkg.Version
}

func readAppDestVersion() string {
	data, err := os.ReadFile(filepath.Join(globalAppDest, ".version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
