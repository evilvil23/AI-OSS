//go:build linux

package hardware

import (
	"os"
	"strconv"
	"strings"
)

func detectOS() string { return "linux" }

// detectTotalMemory 读取 /proc/meminfo 的 MemTotal
func detectTotalMemory() float64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.ParseInt(fields[1], 10, 64)
				if err == nil {
					return float64(kb) / 1024 / 1024
				}
			}
		}
	}
	return 0
}

// detectCPUModel 读取 /proc/cpuinfo 的 model name
func detectCPUModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "model name") {
			if idx := strings.Index(line, ":"); idx >= 0 {
				return strings.TrimSpace(line[idx+1:])
			}
		}
	}
	return ""
}
