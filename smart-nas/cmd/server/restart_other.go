//go:build !windows

// restart_other.go 进程级自重启（v0.23，非 Windows 平台）。
//
// 以相同命令行参数重新拉起自身可执行文件：调用时机为全部资源优雅关闭之后
// （监听端口已释放、日志句柄已关闭、SQLite 已落盘、Ollama 已停止），
// 新进程启动即可接管端口，父进程随后退出，无端口竞争窗口。
package main

import (
	"os"
	"os/exec"
)

// relaunchSelf 重新拉起自身进程
func relaunchSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "SMARTNAS_RESTART=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}
