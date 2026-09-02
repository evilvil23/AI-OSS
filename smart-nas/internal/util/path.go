// path.go Windows 绝对路径校验（v0.13）：手动输入的路径（回收站位置、日志位置、
// 用户目录权限等）统一校验格式并归一化为反斜杠分隔，与前端 normalizeWinPath /
// isValidWinPath 保持一致语义。
package util

import "strings"

// NormalizeWinPath 去除首尾空白，正/反斜杠统一归一为反斜杠
func NormalizeWinPath(p string) string {
	p = strings.TrimSpace(p)
	for strings.Contains(p, "/") {
		p = strings.ReplaceAll(p, "/", "\\")
	}
	for strings.Contains(p, "\\\\") {
		p = strings.ReplaceAll(p, "\\\\", "\\")
	}
	return p
}

// IsValidAbsPath 校验是否为合法的 Windows 绝对路径：
// 盘符开头（如 Y:\ 或 Y:/），且不含 < > " | ? * 等非法字符。
func IsValidAbsPath(p string) bool {
	if len(p) < 3 {
		return false
	}
	c := p[0]
	if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) || p[1] != ':' {
		return false
	}
	if p[2] != '\\' && p[2] != '/' {
		return false
	}
	for i := 3; i < len(p); i++ {
		switch p[i] {
		case '<', '>', '"', '|', '?', '*':
			return false
		}
	}
	return true
}
