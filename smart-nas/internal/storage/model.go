// Package storage 文件存储 / NAS 模块。
// 说明：文档选用 GORM + SQLite 管理元数据；当前离线环境不可用，
// 这里采用 JSONStore 落盘等价实现，文件 blob 仍存储于文件系统。
package storage

import (
	"time"
)

// FileMeta 文件/目录元数据
// 注意：时间字段使用值类型（zero 表示未设置）。go-toml 无法将 *time.Time
// 编码为 TOML 日期时间（会退化为字符串且无法回读），因此避免指针时间。
type FileMeta struct {
	ID           uint      `json:"id"`
	Name         string    `json:"name"`
	ParentID     uint      `json:"parent_id"`
	IsDir        bool      `json:"is_dir"`
	Size         int64     `json:"size"`
	MD5          string    `json:"md5"`
	MimeType     string    `json:"mime_type"`
	StoragePath  string    `json:"storage_path"`            // 真实物理路径（持久化，对外脱敏见 Public）
	TrashOrigin  string    `json:"trash_origin,omitempty"`  // 回收站中记录的原路径，用于恢复
	OwnerID      uint      `json:"owner_id"`
	Shared       bool      `json:"shared"`
	ShareToken   string    `json:"share_token"`
	ShareExpires time.Time `json:"share_expires,omitzero"`
	SharePwdHash string    `json:"share_pwd_hash"`
	DeletedAt    time.Time `json:"deleted_at,omitzero"` // 零值表示未删除
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Deleted 是否在回收站中
func (f *FileMeta) Deleted() bool { return f != nil && !f.DeletedAt.IsZero() }

// IsDisk 是否为磁盘根节点（顶层目录且具有真实路径）
func (f *FileMeta) IsDisk() bool {
	return f != nil && f.IsDir && f.ParentID == 0 && f.StoragePath != ""
}

// Public 返回脱敏视图：
// 文件隐藏物理路径；目录/磁盘保留真实路径（path 用于前端展示与导航）
func (f *FileMeta) Public() *FileMeta {
	if f == nil {
		return nil
	}
	cp := *f
	cp.TrashOrigin = ""
	cp.ShareToken = ""
	cp.SharePwdHash = ""
	if !f.IsDir {
		cp.StoragePath = ""
	}
	return &cp
}

// PublicDetail 详情脱敏视图：与 Public 相同，但保留文件真实路径。
// 仅用于已通过读权限校验的详情接口（GET /api/files/:id），供前端展示文件路径。
func (f *FileMeta) PublicDetail() *FileMeta {
	if f == nil {
		return nil
	}
	cp := *f
	cp.TrashOrigin = ""
	cp.ShareToken = ""
	cp.SharePwdHash = ""
	return &cp
}

// PublicList 批量脱敏
func PublicList(list []*FileMeta) []*FileMeta {
	out := make([]*FileMeta, 0, len(list))
	for _, f := range list {
		out = append(out, f.Public())
	}
	return out
}

// FileVersion 文件历史版本
type FileVersion struct {
	ID          uint      `json:"id"`
	FileID      uint      `json:"file_id"`
	Version     int       `json:"version"`
	MD5         string    `json:"md5"`
	StoragePath string    `json:"storage_path"` // 版本物理路径（持久化，对外脱敏）
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
}

// Public 版本脱敏视图
func (v *FileVersion) Public() *FileVersion {
	if v == nil {
		return nil
	}
	cp := *v
	cp.StoragePath = ""
	return &cp
}

// StorageStats 存储用量统计
type StorageStats struct {
	TotalFiles   int   `json:"total_files"`
	TotalDirs    int   `json:"total_dirs"`
	UsedBytes    int64 `json:"used_bytes"`
	TrashBytes   int64 `json:"trash_bytes"`
	TrashFiles   int   `json:"trash_files"`
	VersionBytes int64 `json:"version_bytes"`
}