//go:build !windows

// winapi_other.go 非 Windows 平台桩实现：USB 监控与磁盘空间校验不可用
// （家庭 NAS 目标平台为 Windows）。所有函数返回安全零值。
package backup

func driveTypeOf(root string) int { return 0 }

func diskSpaceOf(root string) (freeAvail, totalBytes, totalFree uint64, ok bool) {
	return 0, 0, 0, false
}

func volumeInfoOf(root string) (label, serial string, ok bool) { return "", "", false }
