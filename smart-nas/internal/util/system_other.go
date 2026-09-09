//go:build !windows

// system_other.go 非 Windows 平台的系统调用桩实现。
// 所有函数返回安全零值，调用方按「失败即跳过」处理（家庭 NAS 目标平台为 Windows）。
package util

// memoryStatusEx 占位结构（仅 Windows 使用）
type memoryStatusEx struct{ Length uint32 }

// filetime 占位结构（仅 Windows 使用）
type filetime struct{ LowDateTime, HighDateTime uint32 }

func collectMemory(st *SystemStatus) {}
func getTickCount64() uint64         { return 0 }
func systemTimes() (idle, kernel, user uint64) {
	return 0, 0, 0
}

func driveTypeOf(root string) int { return 0 }

func diskSpaceOf(root string) (freeAvail, totalBytes, totalFree uint64, ok bool) {
	return 0, 0, 0, false
}

func fileAttributesOf(path string) uint32 { return _invalidAttrs }

func volumeInformationOf(root string) (label string, serial uint32, ok bool) {
	return "", 0, false
}
