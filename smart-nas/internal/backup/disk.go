// disk.go 备份前磁盘剩余空间校验（Windows 实现，经 purego 调用 kernel32）。
package backup

import (
	"path/filepath"
	"strings"
)

// volumeRoot 返回路径所在卷根（如 "S:\"）
func volumeRoot(path string) string {
	vol := filepath.VolumeName(path)
	if vol == "" {
		return ""
	}
	return vol + string(filepath.Separator)
}

// freeSpaceOf 返回路径所在卷的剩余字节数；第二个返回值=false 表示查询失败（跳过校验）。
func freeSpaceOf(path string) (int64, bool) {
	root := volumeRoot(path)
	if root == "" || !isWindows() {
		return 0, false
	}
	freeAvail, totalBytes, _, ok := diskSpaceOf(root)
	// totalBytes==0 视为查询无效（虚拟盘/沙箱可能返回 0），跳过空间校验
	if !ok || totalBytes == 0 {
		return 0, false
	}
	return int64(freeAvail), true
}

var _ = strings.TrimSpace
