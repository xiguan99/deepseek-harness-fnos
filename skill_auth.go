package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	skillAuthSubsMu sync.Mutex
	skillAuthSubs   = make(map[chan gin.H]struct{})

	skillAuthPayloadMu     sync.Mutex
	cachedSkillAuthPayload gin.H
	skillAuthPayloadLoaded bool
)

// isSkillEnabled 判断当前是否已开启内置技能
func isSkillEnabled() bool {
	cfg := GetConfig()
	return cfg.EnableBuiltinSkill == nil || *cfg.EnableBuiltinSkill
}

// getSkillAuthDirName 返回已部署技能目录名（0.6.2 起为 fnos-cli，旧版为 fnos）
func getSkillAuthDirName() string {
	home := filepath.Join(globalDshHome, "skills")
	for _, name := range []string{"fnos-cli", "fnos"} {
		if _, err := os.Stat(filepath.Join(home, name, "scripts", "fnos-cli")); err == nil {
			return name
		}
		if _, err := os.Stat(filepath.Join(home, name, "scripts", "trim-cli")); err == nil {
			return name
		}
	}
	return "fnos-cli"
}

// getFnosCliPath 获取已部署的飞牛官方命令行工具启动器路径
func getFnosCliPath() string {
	home := filepath.Join(globalDshHome, "skills")
	for _, cand := range []string{
		filepath.Join(home, "fnos-cli", "scripts", "fnos-cli"),
		filepath.Join(home, "fnos", "scripts", "fnos-cli"),
	} {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return filepath.Join(home, getSkillAuthDirName(), "scripts", "fnos-cli")
}

// fnosCliEnv 构造调用命令行工具的环境：HOME 隔离到应用数据目录，
// 会话用文件存储（避免依赖系统密钥环），缓存指向与预置一致的 XDG 缓存目录。
func fnosCliEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+4)
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "HOME="),
			strings.HasPrefix(kv, "XDG_CACHE_HOME="),
			strings.HasPrefix(kv, "XDG_CONFIG_HOME="),
			strings.HasPrefix(kv, "FNOS_CLI_SESSION_STORAGE="):
			// 由本函数统一覆盖
		default:
			out = append(out, kv)
		}
	}
	out = append(out,
		"HOME="+globalHomeDir,
		"XDG_CACHE_HOME="+filepath.Join(globalHomeDir, ".cache"),
		"XDG_CONFIG_HOME="+filepath.Join(globalHomeDir, ".config"),
		"FNOS_CLI_SESSION_STORAGE=file",
	)
	return out
}

// runFnosCli 以隔离环境运行命令行工具，返回 stdout/stderr
func runFnosCli(timeout time.Duration, args ...string) (string, string, error) {
	cli := getFnosCliPath()
	cmd := exec.Command(cli, args...)
	cmd.Env = fnosCliEnv()
	cmd.Dir = globalHomeDir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return stdout.String(), stderr.String(), err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return stdout.String(), stderr.String(), err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return stdout.String(), stderr.String(), fmt.Errorf("命令超时（%v）", timeout)
	}
}

// parseFnosCliJSON 解析命令行工具输出的 JSON（取最后一行非空 JSON）
func parseFnosCliJSON(out string) map[string]any {
	// 新版 CLI 输出是多行美化 JSON：从每个 '{' 起用流式解码器取第一个完整对象
	for i := 0; i < len(out); i++ {
		if out[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(out[i:]))
		var m map[string]any
		if err := dec.Decode(&m); err == nil && m != nil {
			return m
		}
	}
	return nil
}

// SubscribeSkillAuth 订阅技能授权状态变化
func SubscribeSkillAuth(buf int) (<-chan gin.H, func()) {
	ch := make(chan gin.H, buf)
	skillAuthSubsMu.Lock()
	skillAuthSubs[ch] = struct{}{}
	skillAuthSubsMu.Unlock()
	return ch, func() {
		skillAuthSubsMu.Lock()
		delete(skillAuthSubs, ch)
		skillAuthSubsMu.Unlock()
	}
}

// broadcastSkillAuth 广播当前授权状态
func broadcastSkillAuth() {
	payload := getSkillAuthPayload()
	skillAuthSubsMu.Lock()
	subs := make([]chan gin.H, 0, len(skillAuthSubs))
	for ch := range skillAuthSubs {
		subs = append(subs, ch)
	}
	skillAuthSubsMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- payload:
		default:
		}
	}
}

// getSkillAuthPayload 获取当前授权状态（带缓存）
func getSkillAuthPayload() gin.H {
	skillAuthPayloadMu.Lock()
	if skillAuthPayloadLoaded {
		payload := cachedSkillAuthPayload
		skillAuthPayloadMu.Unlock()
		return payload
	}
	skillAuthPayloadMu.Unlock()
	payload := refreshSkillAuthPayload()
	skillAuthPayloadMu.Lock()
	cachedSkillAuthPayload = payload
	skillAuthPayloadLoaded = true
	skillAuthPayloadMu.Unlock()
	return payload
}

// refreshSkillAuthPayload 刷新授权状态缓存
func refreshSkillAuthPayload() gin.H {
	if !isSkillEnabled() {
		return gin.H{"authorized": false}
	}
	if _, err := os.Stat(getFnosCliPath()); err != nil {
		return gin.H{"authorized": false}
	}
	// 0.6.2 无 user info 命令，用 system info 的返回码判断授权
	out, _, err := runFnosCli(15*time.Second, "system", "info")
	if err != nil {
		return gin.H{"authorized": false}
	}
	payload := gin.H{"authorized": true}
	if m := parseFnosCliJSON(out); m != nil {
		if res, ok := m["result"].(map[string]any); ok {
			if name, ok := res["device_name"].(string); ok && name != "" {
				payload["username"] = name
			}
		}
	}
	return payload
}

// handleSkillAuthStart 发起授权：一次性运行 login --authorize-only 获取授权链接
func handleSkillAuthStart(c *gin.Context) {
	if !isSkillEnabled() {
		Fail(c, 400, "当前未启用内置技能")
		return
	}
	if _, err := os.Stat(getFnosCliPath()); err != nil {
		Fail(c, 400, "命令行工具尚未部署，请稍后重试")
		return
	}
	var req struct {
		Port *string `json:"port"` // 兼容旧版前端参数，0.6.2 已无 --port，收下忽略
	}
	_ = c.ShouldBindJSON(&req)

	// 清理旧版会话遗留，避免新旧机制混淆
	cleanLegacyTrimSession()

	stdout, stderr, err := runFnosCli(20*time.Second, "login", "--authorize-only", "--agent-name", "dsh")
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(stdout)
		}
		if msg == "" {
			msg = err.Error()
		}
		Fail(c, 500, "获取授权链接失败: "+msg)
		return
	}
	m := parseFnosCliJSON(stdout)
	urlVal, _ := m["authorization_url"].(string)
	if urlVal == "" {
		Fail(c, 500, "获取授权链接失败: 输出缺少 authorization_url")
		return
	}
	OK(c, gin.H{"url": urlVal})
}

// handleSkillAuthConfirm 完成授权：运行 login --code <code>
func handleSkillAuthConfirm(c *gin.Context) {
	if !isSkillEnabled() {
		Fail(c, 400, "当前未启用内置技能")
		return
	}
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, 400, "请提供访问码")
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		Fail(c, 400, "请提供访问码")
		return
	}

	stdout, stderr, err := runFnosCli(30*time.Second, "login", "--code", code)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(stdout)
		}
		if msg == "" {
			msg = err.Error()
		}
		Fail(c, 400, "授权失败: "+msg)
		return
	}
	payload := refreshSkillAuthPayload()
	OKMsg(c, "授权成功", gin.H{"authorized": payload["authorized"]})
	broadcastSkillAuth()
}

// handleSkillAuthLogout 退出登录并清除会话
func handleSkillAuthLogout(c *gin.Context) {
	if !isSkillEnabled() {
		Fail(c, 400, "当前未启用内置技能")
		return
	}
	if _, err := os.Stat(getFnosCliPath()); err == nil {
		_, _, _ = runFnosCli(15*time.Second, "logout")
	}
	// 清除会话文件存储（profiles 目录）
	_ = os.RemoveAll(filepath.Join(globalHomeDir, ".config", "fnos-cli", "profiles"))
	_ = os.RemoveAll(filepath.Join(globalHomeDir, ".config", "trim-cli"))
	payload := refreshSkillAuthPayload()
	OKMsg(c, "已退出登录并清除会话", gin.H{"authorized": payload["authorized"]})
	broadcastSkillAuth()
}
