package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAlignProfilePlugins 验证：核心版本变化后，profile 里钉死的核心插件版本会被
// 对齐到随包运行时版本，并把旧副本换成指向随包目录的软链；缺失的插件保持原样。
func TestAlignProfilePlugins(t *testing.T) {
	tmp := t.TempDir()

	oldHome, oldRuntime := globalDshHome, runtimeDir
	globalDshHome = filepath.Join(tmp, "dsh-data")
	runtimeDir = filepath.Join(tmp, "dsh-runtime")
	defer func() { globalDshHome, runtimeDir = oldHome, oldRuntime }()

	runtimeVer := "0.2.2-alpha.2"
	writePkg := func(path string, doc map[string]any) {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		raw, _ := json.Marshal(doc)
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	// 随包运行时：核心 + 两个插件；故意不提供 experimental bundle
	writePkg(filepath.Join(runtimeDir, "node_modules", "@deepseek-ai", "dsh", "package.json"),
		map[string]any{"name": "@deepseek-ai/dsh", "version": runtimeVer})
	for _, name := range []string{"dsh-schedule", "dsh-time-context"} {
		writePkg(filepath.Join(runtimeDir, "node_modules", "@deepseek-ai", name, "package.json"),
			map[string]any{"name": "@deepseek-ai/" + name, "version": runtimeVer})
	}

	keptLocal := filepath.Join(tmp, "kept-local-plugin")
	if err := os.MkdirAll(keptLocal, 0755); err != nil {
		t.Fatalf("mkdir kept-local: %v", err)
	}

	// 用户 profile：核心插件钉在旧版
	profileDir := filepath.Join(globalDshHome, "profiles", "web")
	writePkg(filepath.Join(profileDir, "package.json"), map[string]any{
		"name": "web",
		"dependencies": map[string]any{
			"@deepseek-ai/dsh-schedule":                     "0.2.1-rc.3",
			"@deepseek-ai/dsh-time-context":                 "~0.2.1-rc.3",
			"@deepseek-ai/dsh-experimental-schedule-bundle": "0.2.0-rc.2",
			"@local/dsh-mnemon-throttle":                    "file:" + keptLocal,
			"@local/dsh-gone":                               "link:/tmp/definitely-missing-dir",
		},
	})
	// 旧副本（目录形态）
	writePkg(filepath.Join(profileDir, "node_modules", "@deepseek-ai", "dsh-schedule", "package.json"),
		map[string]any{"name": "@deepseek-ai/dsh-schedule", "version": "0.2.1-rc.3"})

	alignProfilePlugins()

	raw, err := os.ReadFile(filepath.Join(profileDir, "package.json"))
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	var doc struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got := doc.Dependencies["@deepseek-ai/dsh-schedule"]; got != runtimeVer {
		t.Errorf("dsh-schedule = %q, want %q", got, runtimeVer)
	}
	if got := doc.Dependencies["@deepseek-ai/dsh-time-context"]; got != runtimeVer {
		t.Errorf("dsh-time-context = %q, want %q", got, runtimeVer)
	}
	// 随包没有的插件不许动
	if got := doc.Dependencies["@deepseek-ai/dsh-experimental-schedule-bundle"]; got != "0.2.0-rc.2" {
		t.Errorf("bundle 被误改: %q", got)
	}
	// 目标仍存在的本地包不许动；目标已删除的应被移除
	if got := doc.Dependencies["@local/dsh-mnemon-throttle"]; got != "file:"+keptLocal {
		t.Errorf("@local 有效行被误改: %q", got)
	}
	if _, ok := doc.Dependencies["@local/dsh-gone"]; ok {
		t.Errorf("失效的 @local 行未被移除")
	}

	// 旧副本应变成指向随包的软链
	link := filepath.Join(profileDir, "node_modules", "@deepseek-ai", "dsh-schedule")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat %s: %v", link, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s 不是软链", link)
	}
	dest, _ := os.Readlink(link)
	if want := filepath.Join(runtimeDir, "node_modules", "@deepseek-ai", "dsh-schedule"); dest != want {
		t.Errorf("软链指向 %q, want %q", dest, want)
	}

	// 幂等：再跑一次不应报错、不应改动
	alignProfilePlugins()
	raw2, _ := os.ReadFile(filepath.Join(profileDir, "package.json"))
	if string(raw) != string(raw2) {
		t.Errorf("第二次运行改动了 package.json")
	}
}

// TestDropDanglingLocalBundles 验证失效的 @local 链接包行会被移除，有效的保持不动。
func TestDropDanglingLocalBundles(t *testing.T) {
	tmp := t.TempDir()
	oldHome, oldRuntime := globalDshHome, runtimeDir
	globalDshHome = filepath.Join(tmp, "dsh-data")
	runtimeDir = filepath.Join(tmp, "dsh-runtime")
	defer func() { globalDshHome, runtimeDir = oldHome, oldRuntime }()

	validDir := filepath.Join(tmp, "still-here")
	if err := os.MkdirAll(validDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profileDir := filepath.Join(globalDshHome, "profiles", "web")
	doc := map[string]any{
		"name": "web",
		"dependencies": map[string]any{
			"@local/gone": "link:" + filepath.Join(tmp, "deleted-dir"),
			"@local/kept": "file:" + validDir,
			"some-plugin": "^1.2.3",
		},
	}
	raw, _ := json.Marshal(doc)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(profileDir, "package.json")), 0755); err != nil {
		t.Fatalf("mkdir profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "package.json"), raw, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	deps := doc["dependencies"].(map[string]any)
	dropped := dropDanglingLocalBundles(profileDir, deps)

	if len(dropped) != 1 || !strings.Contains(dropped[0], "@local/gone") {
		t.Fatalf("dropped = %v, want 只删 @local/gone", dropped)
	}
	if _, ok := deps["@local/kept"]; !ok {
		t.Errorf("@local/kept 被误删")
	}
	if _, ok := deps["some-plugin"]; !ok {
		t.Errorf("some-plugin 被误删")
	}
}
