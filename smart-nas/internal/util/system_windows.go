// system_windows.go 基于 purego 的 Windows 系统调用封装。
//
// purego 负责类型化函数指针的调用编排（寄存器/栈参数传递），替代旧版
// syscall.NewLazyDLL().NewProc().Call(uintptr...) 的裸指针调用方式；
// DLL 句柄经 golang.org/x/sys/windows.LoadLibrary 获取（purego 在 Windows
// 不提供 Dlopen，Windows 系统 DLL 优先用 x/sys/windows 加载）。
// 函数未找到或调用失败时返回安全零值，调用方按「失败即跳过」处理。
package util

import (
	"errors"
	"unicode/utf16"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/windows"
)

// kernel32 系统 DLL
const kernel32DLL = "kernel32.dll"

// Windows API 常量
const (
	_driveFixed     = 3 // DRIVE_FIXED
	_invalidAttrs   = 0xFFFFFFFF
	_fileAttrHidden = 0x2
	_fileAttrSystem = 0x4
	_maxPathLen     = 261
)

// memoryStatusEx 对应 Windows MEMORYSTATUSEX
type memoryStatusEx struct {
	Length            uint32
	MemoryLoad        uint32
	TotalPhys         uint64
	AvailPhys         uint64
	TotalPageFile     uint64
	AvailPageFile     uint64
	TotalVirtual      uint64
	AvailVirtual      uint64
	AvailExtendedVirt uint64
}

// filetime 对应 Windows FILETIME
type filetime struct {
	LowDateTime  uint32
	HighDateTime uint32
}

// kernel32 类型化函数指针（进程生命周期内加载一次）
var (
	procGlobalMemoryStatusEx func(ms *memoryStatusEx) uint32
	procGetTickCount64       func() uint64
	procGetSystemTimes       func(idle, kernel, user *filetime) uint32
	procGetDriveTypeW        func(rootPath *uint16) uint32
	procGetDiskFreeSpaceExW  func(dir *uint16, freeAvail, totalBytes, totalFree *uint64) uint32
	procGetFileAttributesW   func(name *uint16) uint32
	procGetVolumeInformation func(root *uint16, label *uint16, labelLen uint32, serial, maxLen, flags *uint32) uint32
)

func init() {
	handle, err := windows.LoadLibrary(kernel32DLL)
	if err != nil {
		return // 加载失败：函数指针保持 nil，调用方按不可用处理
	}
	// kernel32 为系统常驻 DLL，函数地址已解析（RegisterLibFunc 内部经
	// GetProcAddress），句柄无需释放也不会被真正卸载
	registerKernel32Func(&procGlobalMemoryStatusEx, handle, "GlobalMemoryStatusEx")
	registerKernel32Func(&procGetTickCount64, handle, "GetTickCount64")
	registerKernel32Func(&procGetSystemTimes, handle, "GetSystemTimes")
	registerKernel32Func(&procGetDriveTypeW, handle, "GetDriveTypeW")
	registerKernel32Func(&procGetDiskFreeSpaceExW, handle, "GetDiskFreeSpaceExW")
	registerKernel32Func(&procGetFileAttributesW, handle, "GetFileAttributesW")
	registerKernel32Func(&procGetVolumeInformation, handle, "GetVolumeInformationW")
}

// registerKernel32Func 注册单个 kernel32 函数。
// RegisterLibFunc 在符号缺失时 panic（kernel32 标准符号不存在概率极低），
// 捕获后保持函数指针 nil，调用方按「该能力不可用」处理。
func registerKernel32Func(fptr any, handle windows.Handle, name string) {
	defer func() {
		if r := recover(); r != nil {
			setNil(fptr)
		}
	}()
	purego.RegisterLibFunc(fptr, uintptr(handle), name)
}

// setNil 将函数指针变量置为 nil（仅支持本文件声明的函数指针类型）
func setNil(fptr any) {
	switch f := fptr.(type) {
	case *func(ms *memoryStatusEx) uint32:
		*f = nil
	case *func() uint64:
		*f = nil
	case *func(idle, kernel, user *filetime) uint32:
		*f = nil
	case *func(rootPath *uint16) uint32: // GetDriveTypeW / GetFileAttributesW 同签名
		*f = nil
	case *func(dir *uint16, freeAvail, totalBytes, totalFree *uint64) uint32:
		*f = nil
	case *func(root *uint16, label *uint16, labelLen uint32, serial, maxLen, flags *uint32) uint32:
		*f = nil
	}
}

// utf16FromString 将字符串编码为以 NUL 结尾的 UTF-16 指针（等价 syscall.UTF16PtrFromString）
func utf16FromString(s string) (*uint16, error) {
	if len(s) > 0 && s[len(s)-1] == 0 {
		return nil, errors.New("string must not contain a NUL byte at the end")
	}
	u := utf16.Encode([]rune(s + "\x00"))
	return &u[0], nil
}

// utf16ToString 将以 NUL 结尾的 UTF-16 缓冲转为字符串（等价 syscall.UTF16ToString）
func utf16ToString(buf []uint16) string {
	for i, c := range buf {
		if c == 0 {
			return string(utf16.Decode(buf[:i]))
		}
	}
	return string(utf16.Decode(buf))
}

// utf16PtrOf 返回字符串的 UTF-16 指针（失败返回 nil，由 API 语义决定行为）
func utf16PtrOf(s string) *uint16 {
	p, err := utf16FromString(s)
	if err != nil {
		return nil
	}
	return p
}

func collectMemory(st *SystemStatus) {
	if procGlobalMemoryStatusEx == nil {
		return
	}
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if procGlobalMemoryStatusEx(&ms) == 0 {
		return
	}
	st.MemoryTotal = ms.TotalPhys
	st.MemoryUsed = ms.TotalPhys - ms.AvailPhys
	if ms.TotalPhys > 0 {
		st.MemoryUsage = float64(st.MemoryUsed) / float64(ms.TotalPhys) * 100
	}
}

func getTickCount64() uint64 {
	if procGetTickCount64 == nil {
		return 0
	}
	return procGetTickCount64()
}

func systemTimes() (idle, kernel, user uint64) {
	if procGetSystemTimes == nil {
		return 0, 0, 0
	}
	var l, k, u filetime
	if procGetSystemTimes(&l, &k, &u) == 0 {
		return 0, 0, 0
	}
	return filetimeToUint64(l), filetimeToUint64(k), filetimeToUint64(u)
}

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

func fileAttributesOf(path string) uint32 {
	if procGetFileAttributesW == nil {
		return _invalidAttrs
	}
	p := utf16PtrOf(path)
	if p == nil {
		return _invalidAttrs
	}
	return procGetFileAttributesW(p)
}

// volumeInformationOf 读取卷标与卷序列号
func volumeInformationOf(root string) (label string, serial uint32, ok bool) {
	if procGetVolumeInformation == nil {
		return "", 0, false
	}
	rp := utf16PtrOf(root)
	if rp == nil {
		return "", 0, false
	}
	var nameBuf [_maxPathLen]uint16
	var serialNum, maxLen, flags uint32
	if procGetVolumeInformation(rp, &nameBuf[0], uint32(len(nameBuf)), &serialNum, &maxLen, &flags) == 0 {
		return "", 0, false
	}
	return utf16ToString(nameBuf[:]), serialNum, true
}
