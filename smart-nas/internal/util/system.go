// Package util 提供公共工具函数：系统状态、网络、哈希、ID 生成。
package util

import (
	"os"
	"runtime"
	"time"
)

// SystemStatus 系统状态信息
type SystemStatus struct {
	Hostname    string     `json:"hostname"`
	OS          string     `json:"os"`
	Arch        string     `json:"arch"`
	CPUCores    int        `json:"cpu_cores"`
	CPUUsage    float64    `json:"cpu_usage"`
	MemoryTotal uint64     `json:"memory_total"`
	MemoryUsed  uint64     `json:"memory_used"`
	MemoryUsage float64    `json:"memory_usage"`
	Disks       []DiskInfo `json:"disks"`
	Uptime      string     `json:"uptime"`
	LANIP       string     `json:"lan_ip"`
}

// DiskInfo 磁盘信息
type DiskInfo struct {
	MountPoint string  `json:"mount_point"`
	Total      uint64  `json:"total"`
	Used       uint64  `json:"used"`
	Usage      float64 `json:"usage"`
}

// GetSystemStatus 采集系统状态。
// Windows API 经 purego 动态加载（见 system_windows.go），非 Windows 平台为桩实现。
func GetSystemStatus() (*SystemStatus, error) {
	hostname, _ := os.Hostname()
	st := &SystemStatus{
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CPUCores: runtime.NumCPU(),
		LANIP:    GetLANIP(),
		Uptime:   (time.Duration(getTickCount64()) * time.Millisecond).String(),
	}
	collectMemory(st)
	collectCPU(st)
	collectDisks(st)
	return st, nil
}

// filetimeToUint64 将 64 位 FILETIME 组合为整数
func filetimeToUint64(ft filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

func collectCPU(st *SystemStatus) {
	idle0, kernel0, user0 := systemTimes()
	time.Sleep(200 * time.Millisecond)
	idle1, kernel1, user1 := systemTimes()
	idle := idle1 - idle0
	kernel := kernel1 - kernel0
	user := user1 - user0
	total := kernel + user
	if total > 0 {
		busy := total - idle
		st.CPUUsage = float64(busy) / float64(total) * 100
	}
}

func collectDisks(st *SystemStatus) {
	for _, root := range fixedDriveRoots() {
		if driveTypeOf(root) != _driveFixed {
			continue
		}
		_, totalBytes, totalFree, ok := diskSpaceOf(root)
		if !ok || totalBytes == 0 {
			continue
		}
		used := totalBytes - totalFree
		st.Disks = append(st.Disks, DiskInfo{
			MountPoint: root,
			Total:      totalBytes,
			Used:       used,
			Usage:      float64(used) / float64(totalBytes) * 100,
		})
	}
}

// ListFixedDrives 枚举本机固定磁盘盘符（Windows），返回如 ["C:/", "D:/"]。
// 非 Windows 平台返回空切片。
func ListFixedDrives() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	var out []string
	for _, root := range fixedDriveRoots() {
		if driveTypeOf(root) == _driveFixed {
			out = append(out, root)
		}
	}
	return out
}

// IsHiddenSystemFile 判断路径是否带有 Windows 隐藏/系统属性（如 $RECYCLE.BIN、
// pagefile.sys、desktop.ini 等）。非 Windows 平台恒返回 false；路径不存在也返回
// false（交由调用方的其它规则判断）。
func IsHiddenSystemFile(path string) bool {
	if runtime.GOOS != "windows" || path == "" {
		return false
	}
	attrs := fileAttributesOf(path)
	if attrs == _invalidAttrs {
		return false
	}
	return attrs&(_fileAttrHidden|_fileAttrSystem) != 0
}

func fixedDriveRoots() []string {
	var roots []string
	// 从 A 盘开始枚举，避免遗漏 B: 等前部盘符（部分机器存在 B: 固定磁盘）
	for letter := 'A'; letter <= 'Z'; letter++ {
		roots = append(roots, string(letter)+":\\")
	}
	return roots
}
