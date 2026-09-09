//go:build windows

// restart_windows.go 进程级自重启（v0.23）。
//
// 以相同命令行参数重新拉起自身可执行文件：调用时机为全部资源优雅关闭之后
// （监听端口已释放、日志句柄已关闭、SQLite 已落盘、Ollama 已停止），
// 新进程启动即可接管端口，父进程随后退出，无端口竞争窗口。
package main

import (
	"os"
	"os/exec"
	"syscall"
)

// relaunchSelf 重新拉起自身进程（Windows：隐藏窗口 + 新进程组；
// 不用 DETACHED_PROCESS，交互式终端运行时新进程可继承控制台输出日志）
func relaunchSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "SMARTNAS_RESTART=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	return cmd.Start()
}
