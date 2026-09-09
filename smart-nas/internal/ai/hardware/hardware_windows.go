//go:build windows

package hardware

import (
	"os/exec"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/windows"
)

// memoryStatusEx 对应 Windows MEMORYSTATUSEX
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// procGlobalMemoryStatusEx 经 purego 动态获取（替代 windows.NewLazySystemDLL）
var procGlobalMemoryStatusEx func(ms *memoryStatusEx) uint32

func init() {
	handle, err := windows.LoadLibrary("kernel32.dll")
	if err != nil {
		return // 加载失败：detectTotalMemory 返回 0，走无 GPU 分支
	}
	// kernel32 为系统常驻 DLL，函数地址已解析，句柄无需释放
	purego.RegisterLibFunc(&procGlobalMemoryStatusEx, uintptr(handle), "GlobalMemoryStatusEx")
}

func detectOS() string { return "windows" }

// detectTotalMemory 通过 GlobalMemoryStatusEx 读取总物理内存
func detectTotalMemory() float64 {
	if procGlobalMemoryStatusEx == nil {
		return 0
	}
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if procGlobalMemoryStatusEx(&ms) == 0 {
		return 0
	}
	return float64(ms.TotalPhys) / 1024 / 1024 / 1024
}

// detectCPUModel 从注册表读取 CPU 型号
func detectCPUModel() string {
	out, err := exec.Command("reg", "query",
		`HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0`,
		"/v", "ProcessorNameString").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if idx := strings.Index(line, "ProcessorNameString"); idx >= 0 {
			parts := strings.SplitN(line, "REG_SZ", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}
