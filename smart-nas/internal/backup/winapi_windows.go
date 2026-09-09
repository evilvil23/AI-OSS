//go:build windows

// winapi_windows.go 基于 purego 的 Windows 系统调用封装（替代旧 syscall.NewLazyDLL）。
// purego 负责类型化函数指针的调用编排，DLL 句柄经 golang.org/x/sys/windows.LoadLibrary
// 获取（purego 在 Windows 不提供 Dlopen，Windows 系统 DLL 优先用 x/sys/windows 加载）；
// 函数缺失时保持 nil，调用方按「失败即跳过」处理。
package backup

import (
	"unicode/utf16"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/windows"
)

// kernel32 函数指针（包初始化时懒加载一次）
var (
	procGetDriveTypeW       func(rootPath *uint16) uint32
	procGetDiskFreeSpaceExW func(dir *uint16, freeAvail, totalBytes, totalFree *uint64) uint32
	procGetVolumeInfo       func(root *uint16, label *uint16, labelLen uint32, serial, maxLen, flags *uint32) uint32
)

func init() {
	handle, err := windows.LoadLibrary("kernel32.dll")
	if err != nil {
		return // 加载失败：函数指针保持 nil，包装函数按不可用处理
	}
	// kernel32 为系统常驻 DLL，函数地址已解析，句柄无需释放
	purego.RegisterLibFunc(&procGetDriveTypeW, uintptr(handle), "GetDriveTypeW")
	purego.RegisterLibFunc(&procGetDiskFreeSpaceExW, uintptr(handle), "GetDiskFreeSpaceExW")
	purego.RegisterLibFunc(&procGetVolumeInfo, uintptr(handle), "GetVolumeInformationW")
}

// driveTypeOf 返回驱动器类型（DRIVE_REMOVABLE=2 / DRIVE_FIXED=3 ...），失败返回 0
func driveTypeOf(root string) int {
	if procGetDriveTypeW == nil {
		return 0
	}
	rp := utf16PtrOf(root)
	if rp == nil {
		return 0
	}
	return int(procGetDriveTypeW(rp))
}

// diskSpaceOf 查询磁盘空间（可用/总/剩余字节），失败返回 ok=false
func diskSpaceOf(root string) (freeAvail, totalBytes, totalFree uint64, ok bool) {
	if procGetDiskFreeSpaceExW == nil {
		return 0, 0, 0, false
	}
	rp := utf16PtrOf(root)
	if rp == nil {
		return 0, 0, 0, false
	}
	if procGetDiskFreeSpaceExW(rp, &freeAvail, &totalBytes, &totalFree) == 0 {
		return 0, 0, 0, false
	}
	return freeAvail, totalBytes, totalFree, true
}

// volumeInfoOf 读取卷标与卷序列号（十六进制大写字符串），失败返回空
func volumeInfoOf(root string) (label, serial string, ok bool) {
	if procGetVolumeInfo == nil {
		return "", "", false
	}
	rp := utf16PtrOf(root)
	if rp == nil {
		return "", "", false
	}
	var nameBuf [261]uint16
	var serialNum, maxLen, flags uint32
	if procGetVolumeInfo(rp, &nameBuf[0], uint32(len(nameBuf)), &serialNum, &maxLen, &flags) == 0 {
		return "", "", false
	}
	return utf16ToString(nameBuf[:]), fmtSerialHex(serialNum), true
}

// utf16PtrOf 将字符串编码为以 NUL 结尾的 UTF-16 指针（失败返回 nil）
func utf16PtrOf(s string) *uint16 {
	u := utf16.Encode([]rune(s + "\x00"))
	return &u[0]
}

// utf16ToString 将以 NUL 结尾的 UTF-16 缓冲转为字符串
func utf16ToString(buf []uint16) string {
	for i, c := range buf {
		if c == 0 {
			return string(utf16.Decode(buf[:i]))
		}
	}
	return string(utf16.Decode(buf))
}

// fmtSerialHex 卷序列号格式化为 8 位大写十六进制（如 "1A2B3C4D"）
func fmtSerialHex(n uint32) string {
	const hexDigits = "0123456789ABCDEF"
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = hexDigits[n&0xF]
		n >>= 4
	}
	return string(out)
}
