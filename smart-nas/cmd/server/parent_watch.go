// parent_watch.go 父进程看门狗（v0.26）。
//
// 背景：`go run` 不会向子进程转发 Ctrl+C / 终止信号（golang/go#40467，各 Go 版本
// 长期如此），go run 自身退出后其编译产物成为孤儿进程继续占用端口，
// 表现为「终端 Ctrl+C 无法终止服务进程」。
//
// 处理：检测到本进程由 go run 启动（可执行文件位于临时构建目录 go-build*）时，
// 监控父进程；父进程退出即向退出信号通道注入中断，复用既有优雅关闭流程。
// 直接运行编译产物或由服务管理器启动时不启用，避免误退出。
package main

import (
	"os"
	"path/filepath"
	"strings"

	"smart-nas/pkg/logger"
)

// watchParentExit 由 go run 启动时监控父进程退出并回调 onExit；其他启动方式不做任何事
func watchParentExit(onExit func()) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	// go run 的编译产物位于临时构建目录（Windows / Linux 均为 go-build*）
	if !strings.Contains(filepath.ToSlash(exe), "go-build") {
		return
	}
	pid := os.Getppid()
	if pid <= 0 {
		return
	}
	logger.Info("检测到由 go run 启动，启用父进程看门狗（父进程退出时同步关闭服务）", "ppid", pid)
	go func() {
		if waitProcessExit(pid) {
			logger.Info("父进程已退出（go run 结束），触发优雅关闭")
			onExit()
		}
	}()
}
