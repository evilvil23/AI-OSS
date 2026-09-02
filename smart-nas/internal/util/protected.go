// protected.go 系统保留名规则（v0.09）：供 storage / webdav 等模块共用的
// 隐藏/系统文件屏蔽判断，保证 REST、WebSocket 与 WebDAV 入口行为一致。
package util

import "path/filepath"

// protectedNames 系统保留名与 NAS 内部目录（大小写不敏感，任意层级均屏蔽）。
// 注意：中央回收站目录名 trash 不在此列（回收站条目本身需要可见），
// WebDAV 挂载层对根下的内部目录另行屏蔽。
var protectedNames = map[string]struct{}{
	"$recycle.bin":              {},
	"system volume information": {},
	"pagefile.sys":              {},
	"swapfile.sys":              {},
	"hiberfil.sys":              {},
	"dumpstack.log":             {},
	"dumpstack.log.tmp":         {},
	"config.msi":                {},
	"recovery":                  {},
	"found.000":                 {},
	"deliveryoptimization":      {}, // Windows 更新分发缓存目录（普通属性但属系统管理）
	".trash":                    {},
	".versions":                 {},
}

// IsProtectedName 条目名是否为系统保留名 / NAS 内部目录名
func IsProtectedName(name string) bool {
	_, ok := protectedNames[lower(name)]
	return ok
}

// IsProtectedPath 路径中任一组件命中保留名即视为受保护
func IsProtectedPath(p string) bool {
	vol := filepath.VolumeName(p)
	rest := p[len(vol):]
	start := 0
	for i := 0; i <= len(rest); i++ {
		if i == len(rest) || rest[i] == '\\' || rest[i] == '/' {
			if i > start {
				if _, ok := protectedNames[lower(rest[start:i])]; ok {
					return true
				}
			}
			start = i + 1
		}
	}
	return false
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
