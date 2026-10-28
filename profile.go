package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

var (
	profileFileMu   sync.Mutex
	npmNameStripRe  = regexp.MustCompile(`^((?:@[a-z0-9-~][\w.-]*/)?[a-z0-9-~][\w.-]*)@.+$`)
	blockedBuildsRe = regexp.MustCompile(`(?i)Ignored build scripts:\s*(.+)`)
	pkgNameRe       = regexp.MustCompile(`^(@?[a-zA-Z0-9][\w.-]*(?:/[@a-zA-Z0-9][\w.-]*)?)@[0-9]`)
)


// normalizePluginKey 提取标准包名，去除版本后缀
func normalizePluginKey(spec string) string {
	if m := npmNameStripRe.FindStringSubmatch(spec); len(m) >= 2 {
		return m[1]
	}
	return spec
}

// 核心基础设施受保护模块正则
var protectedModulePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^cordis:`),
	regexp.MustCompile(`^@deepseek-ai/cordis-`),
	regexp.MustCompile(`^@deepseek-ai/dsh-`),
}

// IsProtectedPlugin 检查是否为受保护的核心基础设施模块
func IsProtectedPlugin(name string) bool {
	if name == "" {
		return false
	}
	for _, p := range protectedModulePatterns {
		if p.MatchString(name) {
			return true
		}
	}
	return false
}

// ProfileManifest Profile 的 package.json 规范结构
type ProfileManifest struct {
	Name         string            `json:"name,omitempty"`
	Private      bool              `json:"private,omitempty"`
	Version      string            `json:"version,omitempty"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
	Dsh          *DshProfileConfig `json:"dsh,omitempty"`
}

type DshProfileConfig struct {
	Profile *DshProfileInner `json:"profile,omitempty"`
}

type DshProfileInner struct {
	Bundles []string `json:"bundles,omitempty"`
}

func profileManifestPath(dir string) string {
	return filepath.Join(dir, "package.json")
}

// readProfileManifestFile 读取 Profile 的 package.json
func readProfileManifestFile(dir string) (*ProfileManifest, error) {
	data, err := os.ReadFile(profileManifestPath(dir))
	if err != nil {
		return nil, err
	}
	var m ProfileManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析 Profile package.json 失败: %w", err)
	}
	if m.Dependencies == nil {
		m.Dependencies = make(map[string]string)
	}
	if m.Dsh == nil {
		m.Dsh = &DshProfileConfig{Profile: &DshProfileInner{Bundles: []string{}}}
	} else if m.Dsh.Profile == nil {
		m.Dsh.Profile = &DshProfileInner{Bundles: []string{}}
	}
	return &m, nil
}

// saveProfileManifestFile 原子写回 Profile 的 package.json
func saveProfileManifestFile(dir string, m *ProfileManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 package.json 失败: %w", err)
	}
	data = append(data, '\n')

	target := profileManifestPath(dir)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// selectBundle 更新 package.json 中的 dsh.profile.bundles
func selectBundle(dir, name string, enabled bool) error {
	profileFileMu.Lock()
	defer profileFileMu.Unlock()

	m, err := readProfileManifestFile(dir)
	if err != nil {
		return err
	}

	previous := m.Dsh.Profile.Bundles
	var bundles []string
	if enabled {
		bundles = append([]string{}, previous...)
		has := false
		for _, b := range bundles {
			if b == name {
				has = true
				break
			}
		}
		if !has {
			bundles = append(bundles, name)
		}
	} else {
		for _, b := range previous {
			if b != name {
				bundles = append(bundles, b)
			}
		}
	}

	// 比较是否有变动
	changed := len(previous) != len(bundles)
	if !changed {
		for i := range previous {
			if previous[i] != bundles[i] {
				changed = true
				break
			}
		}
	}

	if !changed {
		return nil
	}

	m.Dsh.Profile.Bundles = bundles
	return saveProfileManifestFile(dir, m)
}

func profileWorkspaceYamlPath(dir string) string {
	return filepath.Join(dir, "pnpm-workspace.yaml")
}

// approveBuilds 在 pnpm-workspace.yaml 中放行依赖构建脚本
func approveBuilds(dir string, pkgs []string) error {
	if len(pkgs) == 0 {
		return nil
	}
	profileFileMu.Lock()
	defer profileFileMu.Unlock()

	yamlPath := profileWorkspaceYamlPath(dir)
	var root yaml.Node
	data, err := os.ReadFile(yamlPath)
	if err == nil {
		_ = yaml.Unmarshal(data, &root)
	}

	if root.Kind == 0 || len(root.Content) == 0 {
		root.Kind = yaml.DocumentNode
		root.Content = []*yaml.Node{
			{Kind: yaml.MappingNode},
		}
	}

	docMap := root.Content[0]
	var allowBuildsNode *yaml.Node

	for i := 0; i < len(docMap.Content); i += 2 {
		if docMap.Content[i].Value == "allowBuilds" {
			allowBuildsNode = docMap.Content[i+1]
			break
		}
	}

	if allowBuildsNode == nil {
		allowBuildsNode = &yaml.Node{Kind: yaml.MappingNode}
		docMap.Content = append(docMap.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "allowBuilds"},
			allowBuildsNode,
		)
	}

	for _, p := range pkgs {
		found := false
		for i := 0; i < len(allowBuildsNode.Content); i += 2 {
			if allowBuildsNode.Content[i].Value == p {
				allowBuildsNode.Content[i+1].Value = "true"
				allowBuildsNode.Content[i+1].Tag = "!!bool"
				found = true
				break
			}
		}
		if !found {
			allowBuildsNode.Content = append(allowBuildsNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: p},
				&yaml.Node{Kind: yaml.ScalarNode, Value: "true", Tag: "!!bool"},
			)
		}
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		return err
	}
	return os.WriteFile(yamlPath, out, 0644)
}

// removeAllowBuilds 清理 pnpm-workspace.yaml 中的构建放行项
func removeAllowBuilds(dir string, pkgs []string) error {
	if len(pkgs) == 0 {
		return nil
	}
	profileFileMu.Lock()
	defer profileFileMu.Unlock()

	yamlPath := profileWorkspaceYamlPath(dir)
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil || len(root.Content) == 0 {
		return nil
	}

	docMap := root.Content[0]
	var allowBuildsNode *yaml.Node
	for i := 0; i < len(docMap.Content); i += 2 {
		if docMap.Content[i].Value == "allowBuilds" {
			allowBuildsNode = docMap.Content[i+1]
			break
		}
	}

	if allowBuildsNode == nil || allowBuildsNode.Kind != yaml.MappingNode {
		return nil
	}

	dropSet := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		dropSet[p] = true
	}

	var newContent []*yaml.Node
	for i := 0; i < len(allowBuildsNode.Content); i += 2 {
		k := allowBuildsNode.Content[i].Value
		if !dropSet[k] {
			newContent = append(newContent, allowBuildsNode.Content[i], allowBuildsNode.Content[i+1])
		}
	}
	allowBuildsNode.Content = newContent

	out, err := yaml.Marshal(&root)
	if err != nil {
		return err
	}
	return os.WriteFile(yamlPath, out, 0644)
}

// parseBlockedPackages 从 pnpm 错误输出中提取被拦截构建脚本的包名
func parseBlockedPackages(tail string) []string {
	m := blockedBuildsRe.FindStringSubmatch(tail)
	if len(m) < 2 {
		return nil
	}
	var pkgs []string
	for _, part := range strings.Split(m[1], ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if name := pkgNameRe.FindStringSubmatch(part); len(name) >= 2 {
			pkgs = append(pkgs, name[1])
		} else {
			pkgs = append(pkgs, part)
		}
	}
	return pkgs
}

// ResetAllProfilePatches 清空 Profile 插件目录
func ResetAllProfilePatches() {
	profileFileMu.Lock()
	defer profileFileMu.Unlock()

	_ = safeRemoveAll(filepath.Join(globalDshHome, "profiles"))
}

// PnpmFailureCode pnpm 故障分类类型
type PnpmFailureCode string

const (
	PnpmFailureHoistPatternDiff PnpmFailureCode = "hoist-pattern-diff"
	PnpmFailureReleaseAge       PnpmFailureCode = "release-age-violation"
	PnpmFailureFetchTimeout     PnpmFailureCode = "fetch-timeout"
	PnpmFailureTransientNetwork PnpmFailureCode = "transient-network"
	PnpmFailureIgnoredBuilds    PnpmFailureCode = "ignored-builds"
	PnpmFailureGitDepPrepare    PnpmFailureCode = "git-prepare-not-allowed"
	PnpmFailureFetch404         PnpmFailureCode = "fetch-404"
	PnpmFailureUnexpectedStore  PnpmFailureCode = "unexpected-store"
	PnpmFailureUnknown          PnpmFailureCode = "unknown"
)

type PnpmFailureInfo struct {
	Code        PnpmFailureCode
	Recoverable bool
	Message     string
	DetailPkg   string
}

var (
	re404Pkg       = regexp.MustCompile(`(?:GET|fetch)\s+\S*\/([^/\s:]+)(?::|\s)`)
	reTransientNet = regexp.MustCompile(`(?i)(?:ERR_PNPM_FETCH_5\d\d|ERR_PNPM_META_FETCH_FAIL|FetchError|ECONNRESET|ETIMEDOUT|EAI_AGAIN|ENETUNREACH|socket hang up|network timeout)`)
	reFetchTimeout = regexp.MustCompile(`(?i)(?:operation was aborted due to timeout|TimeoutError|error \(23\))`)
	// 允许四段版本：a.b.c(.d)(-pre)。第四段是 fnOS 打包补丁号（如 0.4.1.1 / 0.2.1.1），
	// 用来区分"同一个上游版本打了多次包"；比较时排在 patch 之后、pre 之前。
	semverPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:\.(\d+))?(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)
)

// ClassifyPnpmFailure 分类 pnpm 失败原因
func ClassifyPnpmFailure(output string) PnpmFailureInfo {
	if strings.Contains(output, "ERR_PNPM_PUBLIC_HOIST_PATTERN_DIFF") {
		return PnpmFailureInfo{
			Code:        PnpmFailureHoistPatternDiff,
			Recoverable: true,
			Message:     "node_modules 存在依赖结构差异，已自动重建后重试",
		}
	}
	if strings.Contains(output, "ERR_PNPM_UNEXPECTED_STORE") {
		return PnpmFailureInfo{
			Code:        PnpmFailureUnexpectedStore,
			Recoverable: true,
			Message:     "依赖存储位置变更，已自动清理缓存并重试",
		}
	}
	if strings.Contains(output, "ERR_PNPM_MINIMUM_RELEASE_AGE_VIOLATION") ||
		strings.Contains(output, "ERR_PNPM_NO_MATURE_MATCHING_VERSION") {
		return PnpmFailureInfo{
			Code:        PnpmFailureReleaseAge,
			Recoverable: true,
			Message:     "新版本受 pnpm 安全发布期限制，已自动放行并重试",
		}
	}
	if reFetchTimeout.MatchString(output) {
		return PnpmFailureInfo{
			Code:        PnpmFailureFetchTimeout,
			Recoverable: true,
			Message:     "下载耗时超出默认限制，已自动延长超时时间并重试",
		}
	}
	if strings.Contains(output, "ERR_PNPM_IGNORED_BUILDS") {
		return PnpmFailureInfo{
			Code:        PnpmFailureIgnoredBuilds,
			Recoverable: true,
			Message:     "依赖包含构建脚本，已自动放行并重试",
		}
	}
	if strings.Contains(output, "ERR_PNPM_GIT_DEP_PREPARE_NOT_ALLOWED") {
		return PnpmFailureInfo{
			Code:        PnpmFailureGitDepPrepare,
			Recoverable: true,
			Message:     "Git 插件包含构建脚本，已自动放行并重试",
		}
	}
	if strings.Contains(output, "ERR_PNPM_FETCH_404") {
		detailPkg := ""
		if m := re404Pkg.FindStringSubmatch(output); len(m) > 1 {
			detailPkg = strings.ReplaceAll(m[1], "%2F", "/")
			detailPkg = strings.ReplaceAll(detailPkg, "%2f", "/")
		}
		msg := "指定的插件包在 npm 镜像源上不存在 (404)"
		if detailPkg != "" {
			msg = fmt.Sprintf("依赖包「%s」在镜像源上不存在 (404)", detailPkg)
		}
		return PnpmFailureInfo{
			Code:        PnpmFailureFetch404,
			Recoverable: false,
			Message:     msg,
			DetailPkg:   detailPkg,
		}
	}
	if reTransientNet.MatchString(output) {
		return PnpmFailureInfo{
			Code:        PnpmFailureTransientNetwork,
			Recoverable: true,
			Message:     "网络连接瞬态抖动，已自动重试",
		}
	}
	return PnpmFailureInfo{
		Code:        PnpmFailureUnknown,
		Recoverable: false,
		Message:     "插件指令执行失败",
	}
}

// FormatPnpmFailureMessage 格式化输出友好的中文错误
func FormatPnpmFailureMessage(output string) string {
	info := ClassifyPnpmFailure(output)
	if info.Code == PnpmFailureFetch404 {
		return info.Message
	}

	lines := strings.Split(output, "\n")
	var meaningfulLines []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "ERR_PNPM_") ||
			strings.HasPrefix(trimmed, "npm ERR!") ||
			strings.HasPrefix(trimmed, "error:") ||
			strings.Contains(trimmed, "Error:") {
			meaningfulLines = append(meaningfulLines, trimmed)
		}
	}

	if len(meaningfulLines) > 0 {
		return fmt.Sprintf("%s（%s）", info.Message, meaningfulLines[0])
	}

	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t != "" && !strings.HasPrefix(t, "at ") {
			if len(t) > 120 {
				t = t[:120] + "…"
			}
			return fmt.Sprintf("%s: %s", info.Message, t)
		}
	}
	return info.Message
}

type parsedSemver struct {
	Major int
	Minor int
	Patch int
	// Revision 是四段版本的第四段（打包补丁号）。两段/三段写法时为 0。
	Revision int
	Pre      []string
}

func parseSemver(v string) (parsedSemver, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "^")
	v = strings.TrimPrefix(v, "~")

	m := semverPattern.FindStringSubmatch(v)
	if len(m) == 0 {
		return parsedSemver{}, false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	rev := 0
	if m[4] != "" {
		rev, _ = strconv.Atoi(m[4])
	}

	var pre []string
	if m[5] != "" {
		pre = strings.Split(m[5], ".")
	}
	return parsedSemver{Major: maj, Minor: min, Patch: pat, Revision: rev, Pre: pre}, true
}

// CompareSemver 比较语义化版本: v1 > v2 返回 1; v1 < v2 返回 -1; 相等返回 0
func CompareSemver(v1, v2 string) int {
	p1, ok1 := parseSemver(v1)
	p2, ok2 := parseSemver(v2)
	if !ok1 || !ok2 {
		if v1 == v2 {
			return 0
		}
		return strings.Compare(v1, v2)
	}

	if p1.Major != p2.Major {
		if p1.Major > p2.Major {
			return 1
		}
		return -1
	}
	if p1.Minor != p2.Minor {
		if p1.Minor > p2.Minor {
			return 1
		}
		return -1
	}
	if p1.Patch != p2.Patch {
		if p1.Patch > p2.Patch {
			return 1
		}
		return -1
	}
	if p1.Revision != p2.Revision {
		if p1.Revision > p2.Revision {
			return 1
		}
		return -1
	}

	if len(p1.Pre) == 0 && len(p2.Pre) > 0 {
		return 1
	}
	if len(p1.Pre) > 0 && len(p2.Pre) == 0 {
		return -1
	}
	if len(p1.Pre) == 0 && len(p2.Pre) == 0 {
		return 0
	}

	maxLen := len(p1.Pre)
	if len(p2.Pre) > maxLen {
		maxLen = len(p2.Pre)
	}
	for i := 0; i < maxLen; i++ {
		if i >= len(p1.Pre) {
			return -1
		}
		if i >= len(p2.Pre) {
			return 1
		}
		s1 := p1.Pre[i]
		s2 := p2.Pre[i]
		if s1 == s2 {
			continue
		}
		n1, err1 := strconv.Atoi(s1)
		n2, err2 := strconv.Atoi(s2)
		if err1 == nil && err2 == nil {
			if n1 > n2 {
				return 1
			}
			return -1
		}
		if err1 == nil && err2 != nil {
			return -1
		}
		if err1 != nil && err2 == nil {
			return 1
		}
		return strings.Compare(s1, s2)
	}
	return 0
}
