package webdav

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/net/webdav"

	"smart-nas/internal/util"
)

// nasInternalNames WebDAV 根（data/files）下的 NAS 内部目录：
// 中央回收站 trash 与历史版本库 .versions 不通过挂载暴露
var nasInternalNames = map[string]bool{
	"trash": true, ".trash": true, ".versions": true,
	"metadata.db": true, "metadata.db-wal": true, "metadata.db-shm": true, "metadata.db-journal": true,
}

// permDir 包装 webdav.Dir，在每次文件系统操作前校验用户目录权限，
// 并复用共用的隐藏/系统文件屏蔽规则（util.IsProtectedName/IsProtectedPath），
// 防止 WebDAV 绕过权限控制或触达系统文件 / NAS 内部目录。
type permDir struct {
	webdav.Dir
	userID uint
	can    func(uid uint, path string, write bool) bool
}

func (p permDir) abs(name string) string {
	return filepath.Join(string(p.Dir), filepath.FromSlash(name))
}

func (p permDir) canWrite(name string) bool {
	return p.can(p.userID, p.abs(name), true)
}

func (p permDir) canRead(name string) bool {
	return p.can(p.userID, p.abs(name), false)
}

// denied 共用屏蔽判断：路径/名称命中系统保留名，或位于 WebDAV 根下的
// NAS 内部目录（trash / .versions）之内
func (p permDir) denied(name string) bool {
	clean := path.Clean("/" + strings.TrimPrefix(name, "/"))
	if util.IsProtectedPath(p.abs(clean)) || util.IsProtectedName(path.Base(clean)) {
		return true
	}
	rel := strings.TrimPrefix(clean, "/")
	first := rel
	if i := strings.Index(rel, "/"); i >= 0 {
		first = rel[:i]
	}
	return nasInternalNames[strings.ToLower(first)]
}

// Mkdir 创建目录（需目标路径写权限，且不可为屏蔽条目）
func (p permDir) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	if p.denied(name) || !p.canWrite(name) {
		return os.ErrPermission
	}
	return p.Dir.Mkdir(ctx, name, perm)
}

// OpenFile 打开文件：写标志时校验写权限，否则校验读权限；
// 读打开时包装结果以在目录列表（PROPFIND）中过滤屏蔽条目
func (p permDir) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if p.denied(name) {
		return nil, os.ErrPermission
	}
	writeFlag := flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0
	if writeFlag {
		if !p.canWrite(name) {
			return nil, os.ErrPermission
		}
	} else if !p.canRead(name) {
		return nil, os.ErrPermission
	}
	f, err := p.Dir.OpenFile(ctx, name, flag, perm)
	if err != nil {
		return nil, err
	}
	if !writeFlag {
		return filterFile{File: f}, nil
	}
	return f, nil
}

// filterFile 包装读打开的文件：目录列表（Readdir）中隐藏屏蔽条目
type filterFile struct {
	webdav.File
}

func (f filterFile) Readdir(n int) ([]os.FileInfo, error) {
	infos, err := f.File.Readdir(n)
	out := make([]os.FileInfo, 0, len(infos))
	for _, fi := range infos {
		if util.IsProtectedName(fi.Name()) || nasInternalNames[strings.ToLower(fi.Name())] {
			continue
		}
		out = append(out, fi)
	}
	return out, err
}

// RemoveAll 删除（需目标路径写权限）
func (p permDir) RemoveAll(ctx context.Context, name string) error {
	if p.denied(name) || !p.canWrite(name) {
		return os.ErrPermission
	}
	return p.Dir.RemoveAll(ctx, name)
}

// Rename 移动/重命名（源与目标均需写权限且不可为屏蔽条目）
func (p permDir) Rename(ctx context.Context, oldName, newName string) error {
	if p.denied(oldName) || p.denied(newName) || !p.canWrite(oldName) || !p.canWrite(newName) {
		return os.ErrPermission
	}
	return p.Dir.Rename(ctx, oldName, newName)
}

// Stat 查看状态（需读权限）
func (p permDir) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	if p.denied(name) || !p.canRead(name) {
		return nil, os.ErrPermission
	}
	return p.Dir.Stat(ctx, name)
}
