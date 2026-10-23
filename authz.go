package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// 控制面鉴权
//
// 双包形态下，管理界面搬到了独立的 harness-panel 应用里，它和本体不是同一个
// 系统用户。面板通过本机回环端口调用本体的 /api/*，因此控制面必须自带鉴权：
//
//	1) 控制令牌：应用启动时随机生成，写到 ${TRIM_PKGVAR}/control.token
//	   （权限 0640，属组 AppUsers），面板以同组身份读取，其它用户读不到；
//	2) 两个入口都校验：回环 TCP（默认 127.0.0.1:2297）与 Unix socket
//	   ${TRIM_APPDEST}/web.sock（权限 0660）。只靠文件权限不够，锁门不能留窗。
//
// 令牌不落日志、不进接口返回体，仅在启动时刷新。
const controlTokenHeader = "X-DSH-Control-Token"

var (
	controlTokenMu  sync.RWMutex
	controlTokenVal string
)

// ControlTokenPath 返回控制令牌文件路径。
func ControlTokenPath() string {
	return filepath.Join(globalPkgVar, "control.token")
}

// InitControlToken 生成（或复用）本次运行的控制令牌并落盘。
func InitControlToken() {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		LogFatal("[控制面] 生成控制令牌失败: %s", err)
	}
	token := hex.EncodeToString(buf)

	path := ControlTokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		LogFatal("[控制面] 创建令牌目录失败: %s", err)
	}
	// 先写临时文件再 rename，避免面板读到写了一半的内容
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token), 0640); err != nil {
		LogFatal("[控制面] 写入控制令牌失败: %s", err)
	}
	_ = os.Chmod(tmp, 0640)
	if err := os.Rename(tmp, path); err != nil {
		LogFatal("[控制面] 更新控制令牌失败: %s", err)
	}
	_ = os.Chmod(path, 0640)

	controlTokenMu.Lock()
	controlTokenVal = token
	controlTokenMu.Unlock()

	LogInfo("[控制面] 控制令牌已生成，写入 %s（0640）", path)
}

// ControlToken 返回当前控制令牌。
func ControlToken() string {
	controlTokenMu.RLock()
	defer controlTokenMu.RUnlock()
	return controlTokenVal
}

// RequireControlToken 校验控制令牌。用于所有 /api/* 控制接口。
func RequireControlToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		want := ControlToken()
		got := strings.TrimSpace(c.GetHeader(controlTokenHeader))
		if want == "" || got == "" ||
			subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code":    http.StatusUnauthorized,
				"message": "控制接口需要有效令牌（由本体生成，面板自动读取）",
			})
			return
		}
		c.Next()
	}
}

// StartControlListener 在回环地址上暴露控制面，供面板应用调用。
// 只绑 127.0.0.1，不对外网开放。
func StartControlListener(r *gin.Engine, port int) error {
	addr := "127.0.0.1:" + itoa(port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: r}
	go func() {
		if err := srv.Serve(ln); err != nil && !isExpectedCloseErr(err) {
			LogWarning("[控制面] 回环监听异常退出: %s", err)
		}
	}()
	LogInfo("[控制面] 已监听 %s（仅本机，需控制令牌）", addr)
	return nil
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [12]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
