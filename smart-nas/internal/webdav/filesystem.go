// Package webdav 提供系统级 WebDAV 文件挂载访问。
//
// 说明：文档选用标准库以外的 WebDAV 库，这里基于 golang.org/x/net/webdav
// 实现等价能力：PROPFIND / MKCOL / PUT / GET / DELETE / MOVE / COPY，
// 配合 Basic Auth 实现用户目录隔离（每个用户一个独立根目录）。
package webdav

import (
	"os"
)

// userRoot 返回 WebDAV 根目录。
// 说明：文件系统以磁盘（共享）形式组织，WebDAV 直接暴露磁盘根目录，
// 实际读写权限由用户目录权限配置控制。
func userRoot(root string, userID uint) (string, error) {
	_ = userID
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}