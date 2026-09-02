// protected.go 隐藏/系统文件的统一屏蔽规则（v0.09）。
//
// 前端不可访问、不可显示隐藏的系统文件（如 $RECYCLE.BIN、System Volume
// Information、pagefile.sys 等），以免误操作破坏系统。名称级规则以共用方法
// 形式沉淀在 internal/util（IsProtectedName / IsProtectedPath，WebDAV 等模块
// 同样调用）；本包在其上叠加 Windows 隐藏/系统属性检测
// （IsProtectedEntry / isHiddenFromUser / denyProtected），列表、搜索、回收站、
// 详情、下载、重命名、删除、移动、复制、上传、分享等所有文件操作均调用同一套判断。
package storage

import (
	"errors"

	"smart-nas/internal/util"
)

// ErrProtected 受保护系统文件被拒时的统一错误
var ErrProtected = errors.New("受保护的系统文件，禁止访问或操作")

// IsProtectedEntry 共用判断：文件系统条目是否应被屏蔽。
// 规则一：条目名命中系统保留名（util.IsProtectedName）；
// 规则二：实体带有 Windows 隐藏/系统属性（util.IsHiddenSystemFile）。
func IsProtectedEntry(path, name string) bool {
	if util.IsProtectedName(name) {
		return true
	}
	return util.IsHiddenSystemFile(path)
}

// isProtectedPath 路径中任一组件命中保留名即视为受保护（用于操作校验，
// 覆盖直接携带 ID 访问列表之外的条目、以及位于受保护目录内的子条目）
func isProtectedPath(p string) bool {
	return util.IsProtectedPath(p)
}

// isHiddenFromUser 元数据是否应对用户不可见（列表/搜索/回收站/统计共用）
func isHiddenFromUser(f *FileMeta) bool {
	if f == nil {
		return true
	}
	if isProtectedPath(f.StoragePath) {
		return true
	}
	return IsProtectedEntry(f.StoragePath, f.Name)
}

// denyProtected 文件操作前的统一校验：受保护条目返回 ErrProtected
func denyProtected(f *FileMeta) error {
	if isHiddenFromUser(f) {
		return ErrProtected
	}
	return nil
}
