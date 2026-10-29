package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// alignProfilePlugins 把 profile 里钉死的 @deepseek-ai/dsh-* 核心插件版本对齐到随包运行时版本。
//
// 背景（0.2.2 升级故障，见 0.2.2升级故障报告-20261029.md 的 R2）：
// 核心升级到新版后，dsh-data/profiles/web/package.json 里仍写着旧版插件（例如 0.2.1-rc.3）。
// 这些插件在自己的 package.json 里把 @deepseek-ai/dsh-agent、dsh-llm、dsh-session 等
// 全部钉死在旧版精确版本上，运行时会判定 incompatible 并整套禁用，日志表现为：
//
//	disabling profile plugin row "schedule": Plugin @deepseek-ai/dsh-schedule@0.2.1-rc.3 is incompatible ...
//
// 用户看到的就是「能进页面，但插件全灭」。随包运行时里其实已经带了同版本的这些插件
// （dsh-runtime/node_modules/@deepseek-ai/<name>），所以这里把 profile 的版本号改成运行时
// 版本，并把 profile 里那份旧副本换成指向随包目录的软链——离线、不动用户其它插件、
// 不碰 dsh-data 下的会话与记忆。
func alignProfilePlugins() {
	runtimeVer := readVersion()
	if runtimeVer == "" {
		LogWarning("[插件对齐] 读不到随包运行时版本，跳过插件版本对齐")
		return
	}

	profilesRoot := filepath.Join(globalDshHome, "profiles")
	entries, err := os.ReadDir(profilesRoot)
	if err != nil {
		return // 还没有 profile，正常
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "node_modules" {
			continue
		}
		alignOneProfile(filepath.Join(profilesRoot, entry.Name()), runtimeVer)
	}
}

func alignOneProfile(profileDir, runtimeVer string) {
	pkgFile := filepath.Join(profileDir, "package.json")
	raw, err := os.ReadFile(pkgFile)
	if err != nil {
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		LogWarning("[插件对齐] %s 解析失败: %v", pkgFile, err)
		return
	}
	deps, ok := doc["dependencies"].(map[string]any)
	if !ok || len(deps) == 0 {
		return
	}

	names := make([]string, 0, len(deps))
	for name := range deps {
		names = append(names, name)
	}
	sort.Strings(names)

	aligned := make([]string, 0, len(names))
	missing := make([]string, 0, len(names))
	for _, name := range names {
		// 只处理 DSH 核心插件线；第三方插件（memory-eternal / dsh-browser-cdp 等）不擅自升级
		if !strings.HasPrefix(name, "@deepseek-ai/dsh-") {
			continue
		}
		spec, _ := deps[name].(string)
		current := strings.TrimLeft(spec, "^~v= ")
		if current == runtimeVer {
			continue
		}

		bundled := filepath.Join(runtimeDir, "node_modules", filepath.FromSlash(name))
		if _, err := os.Stat(filepath.Join(bundled, "package.json")); err != nil {
			missing = append(missing, fmt.Sprintf("%s@%s", name, current))
			continue
		}
		if err := linkProfilePackage(profileDir, name, bundled); err != nil {
			LogWarning("[插件对齐] %s 链接到随包版本失败: %v", name, err)
			continue
		}
		deps[name] = runtimeVer
		aligned = append(aligned, fmt.Sprintf("%s %s→%s", name, current, runtimeVer))
	}

	// 清理失效的本地链接包：@local/* 大多以 file:/link: 指到工作目录里的源码，
	// 源码目录被删掉后运行时会报 cannot resolve profile bundle 并跳过（历史遗留污染）。
	dropped := dropDanglingLocalBundles(profileDir, deps)

	if len(aligned) == 0 && len(missing) == 0 && len(dropped) == 0 {
		return
	}

	if len(aligned) > 0 || len(dropped) > 0 {
		if err := writeJSONAtomic(pkgFile, doc); err != nil {
			LogWarning("[插件对齐] 写回 %s 失败: %v", pkgFile, err)
		} else if len(aligned) > 0 {
			LogInfo("[插件对齐] %s：已对齐 %d 个核心插件到 %s（%s）",
				filepath.Base(profileDir), len(aligned), runtimeVer, strings.Join(aligned, "; "))
		}
	}
	if len(dropped) > 0 {
		LogWarning("[插件对齐] %s：已移除指向已删除目录的本地插件行（避免 cannot resolve）: %s",
			filepath.Base(profileDir), strings.Join(dropped, ", "))
	}
	if len(missing) > 0 {
		LogWarning("[插件对齐] %s：以下核心插件随包未提供，保持原样（可能被运行时禁用）: %s",
			filepath.Base(profileDir), strings.Join(missing, ", "))
	}
}

// dropDanglingLocalBundles 删除依赖里 file:/link: 指向不存在路径的插件行，
// 并顺手清掉 node_modules 里对应的残链。返回被移除的 "名字@spec" 列表。
func dropDanglingLocalBundles(profileDir string, deps map[string]any) []string {
	dropped := make([]string, 0)
	for name, rawSpec := range deps {
		spec, _ := rawSpec.(string)
		var target string
		switch {
		case strings.HasPrefix(spec, "file:"):
			target = strings.TrimPrefix(spec, "file:")
		case strings.HasPrefix(spec, "link:"):
			target = strings.TrimPrefix(spec, "link:")
		default:
			continue
		}
		target = strings.TrimSpace(target)
		if target == "" || !filepath.IsAbs(target) {
			continue
		}
		if _, err := os.Stat(target); err == nil {
			continue
		}
		delete(deps, name)
		_ = os.RemoveAll(filepath.Join(profileDir, "node_modules", filepath.FromSlash(name)))
		dropped = append(dropped, fmt.Sprintf("%s@%s", name, spec))
	}
	sort.Strings(dropped)
	return dropped
}

// linkProfilePackage 把 profile 里的插件目录替换为指向随包运行时同名目录的软链。
func linkProfilePackage(profileDir, name, bundled string) error {
	target := filepath.Join(profileDir, "node_modules", filepath.FromSlash(name))
	if fi, err := os.Lstat(target); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if dest, err := os.Readlink(target); err == nil && dest == bundled {
				return nil
			}
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	return os.Symlink(bundled, target)
}

// writeJSONAtomic 原子写回 JSON 文件（保留 2 空格缩进，权限 0644）。
func writeJSONAtomic(path string, doc map[string]any) error {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	tmp := path + ".tmp-align"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
