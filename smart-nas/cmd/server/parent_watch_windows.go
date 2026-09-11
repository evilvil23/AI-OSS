//go:build windows

package main

import "golang.org/x/sys/windows"

// waitProcessExit 等待指定进程退出（Windows：同步等待进程句柄，不占用 CPU）；
// 句柄打开失败（进程已不存在）视为立即退出
func waitProcessExit(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	_, err = windows.WaitForSingleObject(h, windows.INFINITE)
	return err == nil
}
