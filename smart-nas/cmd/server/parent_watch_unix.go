//go:build !windows

package main

import (
	"syscall"
	"time"
)

// waitProcessExit 等待指定进程退出（非 Windows：signal 0 轮询探测，1 秒间隔）
func waitProcessExit(pid int) bool {
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return true
		}
		time.Sleep(time.Second)
	}
}
