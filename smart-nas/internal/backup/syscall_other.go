//go:build !windows

package backup

import "syscall"

// 非 Windows 平台桩实现：USB 监控不可用（家庭 NAS 目标平台为 Windows）

func syscallNewLazyDLL(name string) *syscall.LazyDLL { return nil }

func utf16PtrFromString(s string) (*uint16, error) { return nil, syscall.EWINDOWS }

func unsafePointerOf(p any) uintptr { return 0 }

func utf16ToString(buf []uint16) string { return "" }
