package main

import (
	"embed"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"
)

//go:embed web/*
var embeddedWebFS embed.FS

var (
	globalPkgVar       string
	globalAppVer       string
	globalAppDest      string
	globalRunUser      string
	globalDshHome      string
	globalHomeDir      string
	globalPnpmDir      string
	globalPnpmHome     string
	globalNpmCache     string
	globalSnapshotsDir string
	runtimeDir         string
)

func main() {
	globalPkgVar = os.Getenv("DATA_LIBRARY_PATH")
	if globalPkgVar == "" {
		LogFatal("[主程序] 环境变量缺失: DATA_LIBRARY_PATH")
	}

	globalAppDest = strings.TrimSpace(os.Getenv("TRIM_APPDEST"))
	if globalAppDest == "" {
		LogFatal("[主程序] 环境变量缺失: TRIM_APPDEST")
	}

	globalAppVer = strings.TrimSpace(os.Getenv("TRIM_APPVER"))
	if globalAppVer == "" {
		LogFatal("[主程序] 环境变量缺失: TRIM_APPVER")
	}

	globalRunUser = os.Getenv("DSH_RUN_USER")
	if globalRunUser == "" {
		globalRunUser = "root"
	}

	globalDshHome = filepath.Join(globalPkgVar, "dsh-data")
	globalHomeDir = filepath.Join(globalPkgVar, "home")
	globalPnpmDir = filepath.Join(globalPkgVar, "pnpm-env")
	globalPnpmHome = filepath.Join(globalPkgVar, "pnpm-home")
	globalNpmCache = filepath.Join(globalPkgVar, "npm-cache")
	globalSnapshotsDir = filepath.Join(globalPkgVar, "snapshots")
	runtimeDir = filepath.Join(globalPkgVar, "dsh-runtime")

	InitLogger()
	LogInfo("[主程序] 服务启动初始化 (DATA_LIBRARY_PATH=%s, TRIM_APPDEST=%s, TRIM_APPVER=%s, DSH_RUN_USER=%s)", globalPkgVar, globalAppDest, globalAppVer, globalRunUser)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		LogInfo("[主程序] 捕获终止信号 %s，执行停机流程", sig)
		stopAndWait()
		os.Exit(0)
	}()

	InitConfig()
	InitAppEnv()
	InitHarness()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	WebFS = embeddedWebFS
	InitControlToken()
	InitRoutes(r)
	StartWorkspaceWatch()
	StartAppUpdateChecker()

	_ = os.MkdirAll(globalAppDest, 0755)
	socketPath := filepath.Join(globalAppDest, "web.sock")
	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		LogFatal("[主程序] Unix Socket 监听失败 [%s]: %s", socketPath, err)
	}
	defer listener.Close()

	// 权限收紧到 0660（属主应用用户 : AppUsers）：本机其它用户不再能直接调用控制面。
	// 注意它仍然是控制通道，/api/* 同样校验控制令牌（见 authz.go）。
	if err := os.Chmod(socketPath, 0660); err != nil {
		LogWarning("[主程序] Unix Socket 权限设置失败: %s", err)
	}

	// 回环控制面：面板应用通过 127.0.0.1:<controlPort> + 控制令牌调用
	controlPort := 2297
	if v := strings.TrimSpace(os.Getenv("DSH_CONTROL_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
			controlPort = n
		}
	}
	if err := StartControlListener(r, controlPort); err != nil {
		LogWarning("[控制面] 回环监听启动失败 [127.0.0.1:%d]: %s", controlPort, err)
	}

	LogInfo("[主程序] HTTP 服务已就绪，监听 Socket: %s", socketPath)
	if err := r.RunListener(listener); err != nil {
		LogFatal("[主程序] HTTP 服务异常退出: %s", err)
	}
}
