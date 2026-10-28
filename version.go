package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 只认形如 v0.4.1 / 0.4.1.1 / v0.2.1-rc.2 / v0.2.2-alpha.2.1 的 tag。
var appTagVersionRe = regexp.MustCompile(`^v?\d+(\.\d+){1,3}(-[0-9A-Za-z.]+)?$`)

// 应用包（fnOS 套件）的发布仓库。新包发在新仓，老仓里还有历史版本，
// 两个都查一遍取最高——只查一个会出现"明明发了新版，面板却说没有"。
var appReleaseRepos = []string{
	"xiguan99/deepseek.harness.fnos",
	"xiguan99/deepseek-harness-fnos",
}

// 包内资产的固定前缀：用它认出"这个 Release 是本体包"，
// 免得把面板包（harness-panel_v…）当成应用新版。
const appPackageAssetPrefix = "deepseek.harness_v"

// 老源码架构打包线（0.3.x / 0.4.x）与当前 0.2.x 线不是一个方案：
// 拿 0.4.4 跟 0.2.2-alpha.2.1 按 semver 直接比会得出"可以升到 0.4.4"的反向结论，
// 装上去是另一套源码架构（快照/配置都不兼容）。所以这条线一律不参与升级比较。
// 将来上游真走到 0.3，这条常量要跟着改。
const appLegacySchemeFloor = "0.3.0"

var (
	cachedAppRemoteVer  string
	cachedAppRemotePre  bool
	cachedAppCheckMutex sync.Mutex
)

// githubRelease 只取用得上的一小部分字段
type githubRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
	Assets     []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

// getAppRemoteVersion 获取远端最新版本号
func getAppRemoteVersion() string {
	cachedAppCheckMutex.Lock()
	defer cachedAppCheckMutex.Unlock()
	return cachedAppRemoteVer
}

// getAppRemoteIsPrerelease 远端最新版本是不是预览版（alpha/beta/rc）
func getAppRemoteIsPrerelease() bool {
	cachedAppCheckMutex.Lock()
	defer cachedAppCheckMutex.Unlock()
	return cachedAppRemotePre
}

// isPrereleaseVersion 版本号里带 -alpha/-beta/-rc 就是预览版
func isPrereleaseVersion(v string) bool {
	p, ok := parseSemver(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v")))
	if !ok {
		return false
	}
	return len(p.Pre) > 0
}

// isLegacyAppVersion 老源码架构打包线（0.3.x 及以上、尚未到 1.0）的版本号
func isLegacyAppVersion(v string) bool {
	p, ok := parseSemver(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v")))
	if !ok {
		return false
	}
	return p.Major == 0 && p.Minor >= 3
}

// checkAppHasUpdate 检查是否存在新版本
func checkAppHasUpdate(localVer string) bool {
	cleanLocal := strings.TrimPrefix(strings.TrimSpace(localVer), "v")
	if cleanLocal == "" {
		return false
	}

	cachedAppCheckMutex.Lock()
	defer cachedAppCheckMutex.Unlock()
	if cachedAppRemoteVer == "" {
		return false
	}
	return CompareSemver(cachedAppRemoteVer, cleanLocal) > 0
}

// buildGithubClient 带代理与超时的 HTTP 客户端（GitHub API 用）
func buildGithubClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if cfg := GetConfig(); cfg.NetworkProxy != "" {
		if pURL, err := url.Parse(cfg.NetworkProxy); err == nil {
			transport.Proxy = http.ProxyURL(pURL)
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// fetchRepoReleases 拉一个仓库最近的 Release 列表。
// 为什么不用 /releases/latest：GitHub 的 latest 会跳过 prerelease，
// 而我们发 alpha/rc 包时按官方习惯标成 prerelease，走 latest 就永远看不到新包。
func fetchRepoReleases(repo string) ([]githubRelease, error) {
	api := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=30", repo)
	req, err := http.NewRequest("GET", api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "DeepSeek-Harness-FNOS")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := buildGithubClient(10 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var rels []githubRelease
	if err := json.Unmarshal(body, &rels); err != nil {
		return nil, err
	}
	return rels, nil
}

// hasAppPackageAsset Release 里挂着本体包才算本应用的发布
func hasAppPackageAsset(rel githubRelease) bool {
	for _, a := range rel.Assets {
		if strings.HasPrefix(a.Name, appPackageAssetPrefix) {
			return true
		}
	}
	return false
}

// fetchAppRemoteUpdate 拉取远端最新版本（跨仓库取最高，跳过 alpha/老方案以外的一切噪音）
func fetchAppRemoteUpdate() {
	localVer := globalAppVer
	if localVer == "" {
		return
	}

	bestVer := ""
	bestPre := false
	for _, repo := range appReleaseRepos {
		rels, err := fetchRepoReleases(repo)
		if err != nil {
			LogInfo("[版本] 查询 %s 发布列表失败: %v", repo, err)
			continue
		}
		for _, rel := range rels {
			if rel.Draft || !hasAppPackageAsset(rel) {
				continue
			}
			tag := strings.TrimSpace(rel.TagName)
			if !appTagVersionRe.MatchString(tag) {
				continue
			}
			ver := strings.TrimPrefix(tag, "v")
			if isLegacyAppVersion(ver) {
				// 老方案（0.3.x/0.4.x）不参与比较，只记一行便于排查
				continue
			}
			if bestVer == "" || CompareSemver(ver, bestVer) > 0 {
				bestVer = ver
				bestPre = rel.Prerelease || isPrereleaseVersion(ver)
			}
		}
	}

	if bestVer == "" {
		return
	}

	cachedAppCheckMutex.Lock()
	oldRemoteVer := cachedAppRemoteVer
	cachedAppRemoteVer = bestVer
	cachedAppRemotePre = bestPre
	cachedAppCheckMutex.Unlock()

	cleanLocal := strings.TrimPrefix(strings.TrimSpace(localVer), "v")
	if CompareSemver(bestVer, cleanLocal) > 0 && bestVer != oldRemoteVer {
		LogInfo("[版本] 发现新版本 v%s (当前版本 v%s)", bestVer, cleanLocal)
	}
}

// ── 官方上游（DSH 运行时）────────────────────────────────
// 与 appReleaseRepos 是两条线，容易混，这里写清楚：
//   appReleaseRepos → 我们自己的包发版仓，回答"有没有新包（应用中心要不要升级）"
//   upstreamDshRepo → DeepSeek 官方仓，回答"官方运行时走到哪了（我们要不要跟版）"
// 卡片上「上游最新」指后者。官方 tag 统一是 dsh-v0.2.2-alpha.2 这种形态。
const upstreamDshRepo = "deepseek-ai/deepseek-harness"

var upstreamTagRe = regexp.MustCompile(`^dsh-v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?)$`)

var (
	cachedUpstreamVer string
	cachedUpstreamPre bool
)

// getUpstreamVersion 官方运行时最新版本（不含 dsh- 前缀）
func getUpstreamVersion() string {
	cachedAppCheckMutex.Lock()
	defer cachedAppCheckMutex.Unlock()
	return cachedUpstreamVer
}

// getUpstreamIsPrerelease 官方最新版是不是预览版
func getUpstreamIsPrerelease() bool {
	cachedAppCheckMutex.Lock()
	defer cachedAppCheckMutex.Unlock()
	return cachedUpstreamPre
}

// fetchUpstreamDshVersion 查官方最新运行时版本
func fetchUpstreamDshVersion() {
	rels, err := fetchRepoReleases(upstreamDshRepo)
	if err != nil {
		LogInfo("[版本] 查询官方上游发布列表失败: %v", err)
		return
	}
	bestVer := ""
	bestPre := false
	for _, rel := range rels {
		if rel.Draft {
			continue
		}
		m := upstreamTagRe.FindStringSubmatch(strings.TrimSpace(rel.TagName))
		if m == nil {
			continue
		}
		ver := m[1]
		if bestVer == "" || CompareSemver(ver, bestVer) > 0 {
			bestVer = ver
			bestPre = rel.Prerelease || isPrereleaseVersion(ver)
		}
	}
	if bestVer == "" {
		return
	}
	cachedAppCheckMutex.Lock()
	changed := cachedUpstreamVer != bestVer
	cachedUpstreamVer = bestVer
	cachedUpstreamPre = bestPre
	cachedAppCheckMutex.Unlock()
	if changed {
		LogInfo("[版本] 官方上游最新 v%s（预览=%v），本包内置 v%s", bestVer, bestPre, readVersion())
	}
}

// StartAppUpdateChecker 启动后台版本检查轮询
func StartAppUpdateChecker() {
	go func() {
		time.Sleep(3 * time.Second)
		fetchAppRemoteUpdate()
		fetchUpstreamDshVersion()

		ticker := time.NewTicker(3 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			fetchAppRemoteUpdate()
			fetchUpstreamDshVersion()
		}
	}()
}
