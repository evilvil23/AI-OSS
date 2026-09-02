//go:build !windows

package backup

// 非 Windows 平台：无卷空间查询实现（跳过该项预检查）
func freeSpaceOf(path string) (int64, bool) { return 0, false }
