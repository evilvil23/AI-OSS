package backup

import (
	"syscall"
	"unsafe"
)

// Windows 系统调用薄封装（集中在此文件，便于单元测试打桩与跨平台编译）

func syscallNewLazyDLL(name string) *syscall.LazyDLL {
	return syscall.NewLazyDLL(name)
}

func utf16PtrFromString(s string) (*uint16, error) {
	return syscall.UTF16PtrFromString(s)
}

func unsafePointerOf(p any) uintptr {
	switch v := p.(type) {
	case *uint16:
		return uintptr(unsafe.Pointer(v))
	case *uint32:
		return uintptr(unsafe.Pointer(v))
	default:
		return 0
	}
}

func utf16ToString(buf []uint16) string {
	for i, c := range buf {
		if c == 0 {
			return syscall.UTF16ToString(buf[:i])
		}
	}
	return syscall.UTF16ToString(buf)
}
