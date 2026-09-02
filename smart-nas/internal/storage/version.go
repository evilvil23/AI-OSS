package storage

import (
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"smart-nas/internal/util"
)

// versionsDir 历史版本存放目录
func (s *Service) versionsDir() string { return filepath.Join(s.root, ".versions") }

// SaveVersion 将旧文件存档为版本，并返回版本号
func (s *Service) SaveVersion(fileID uint, blobPath string, md5 string, size int64) (int, error) {
	if _, err := os.Stat(blobPath); err != nil {
		return 0, nil // 原文件已不存在，跳过
	}
	vs := s.repo.Versions(fileID)
	nextVer := 1
	if len(vs) > 0 {
		nextVer = vs[0].Version + 1
	}
	vd := s.versionsDir()
	archPath := filepath.Join(vd, md5+"."+strconv.Itoa(nextVer))
	_ = os.MkdirAll(vd, 0o755)
	if err := movePath(blobPath, archPath); err != nil {
		return 0, err
	}
	v := &FileVersion{
		FileID:      fileID,
		Version:     nextVer,
		MD5:         md5,
		StoragePath: archPath,
		Size:        size,
	}
	if err := s.repo.AddVersion(v); err != nil {
		return 0, err
	}
	return nextVer, nil
}

// TrimVersions 裁剪多余的历史版本，物理删除被裁剪的 blob
func (s *Service) TrimVersions(fileID uint, keep int) error {
	if keep <= 0 {
		return nil
	}
	removed := s.repo.TrimVersions(fileID, keep)
	for _, v := range removed {
		_ = os.Remove(v.StoragePath)
	}
	return nil
}

// CommitUpload 上传完成后提交元数据：
//  1. 秒传去重（同 MD5 复用）
//  2. 同名文件保留历史版本
//  3. 创建或更新 FileMeta（文件写入真实目录）
//
// returns fileMeta, dedup bool, err
func (s *Service) CommitUpload(userID uint, name string, parentID uint, mimeType, md5, tempPath string, size int64) (*FileMeta, bool, error) {
	// 客户端未提供 MD5 时，由服务端计算
	if md5 == "" && tempPath != "" {
		if h, err := fileMD5(tempPath); err == nil {
			md5 = h
		}
	}
	parentPath, err := s.resolveDirPath(parentID)
	if err != nil {
		return nil, false, err
	}
	// 上传目标不可为受保护的系统路径，文件名不可为系统保留名
	if isProtectedPath(parentPath) {
		return nil, false, ErrProtected
	}
	if util.IsProtectedName(name) {
		return nil, false, ErrProtected
	}
	if !s.canWrite(userID, parentPath) {
		return nil, false, fmt.Errorf("无权上传到「%s」：需要该目录的写权限", filepath.Base(parentPath))
	}
	destPath := filepath.Join(parentPath, name)

	existing := s.repo.ByParentName(parentID, name)
	if existing != nil && !existing.IsDir {
		// 相同 MD5 → 完全去重，仅更新时间
		if existing.MD5 == md5 && md5 != "" {
			existing.Size = size
			existing.MimeType = mimeType
			if err := s.repo.Update(existing); err != nil {
				return nil, true, err
			}
			if tempPath != "" {
				_ = os.Remove(tempPath)
			}
			return existing, true, nil
		}
		// 内容变化：旧文件降级为历史版本（在覆盖前保存）
		_, _ = s.SaveVersion(existing.ID, existing.StoragePath, existing.MD5, existing.Size)
		if err := s.writeContent(destPath, tempPath, md5, size); err != nil {
			return nil, false, err
		}
		existing.MD5 = md5
		existing.StoragePath = destPath
		existing.Size = size
		existing.MimeType = mimeType
		if err := s.repo.Update(existing); err != nil {
			return nil, false, err
		}
		_ = s.TrimVersions(existing.ID, s.cfg.VersionKeep)
		return existing, false, nil
	}

	// 新建
	if err := s.writeContent(destPath, tempPath, md5, size); err != nil {
		return nil, false, err
	}
	f := &FileMeta{
		Name:        name,
		ParentID:    parentID,
		Size:        size,
		MD5:         md5,
		MimeType:    mimeType,
		StoragePath: destPath,
		OwnerID:     userID,
	}
	if err := s.repo.Create(f); err != nil {
		return nil, false, err
	}
	return f, false, nil
}

// writeContent 将内容写入目标路径：优先移动临时文件，否则从同 MD5 文件复制（秒传）
func (s *Service) writeContent(destPath, tempPath, md5 string, size int64) error {
	if tempPath != "" {
		return movePath(tempPath, destPath)
	}
	src := s.findByMD5(md5, size)
	if src == nil {
		return errors.New("秒传源文件不存在")
	}
	return copyPath(src.StoragePath, destPath)
}

// ResolveDownloadPath 组装文件绝对路径
func (s *Service) ResolveDownloadPath(f *FileMeta) (string, error) {
	if f.IsDir {
		return "", errors.New("目录不支持直接下载")
	}
	if f.StoragePath == "" {
		return "", errors.New("文件尚未就绪")
	}
	return f.StoragePath, nil
}

// VersionDownloadPath 获取指定版本的物理路径（版本号从 1 开始）
func (s *Service) VersionDownloadPath(fileID uint, version int) (string, error) {
	for _, v := range s.repo.Versions(fileID) {
		if v.Version == version {
			if v.StoragePath == "" {
				return "", errors.New("版本文件不存在")
			}
			return v.StoragePath, nil
		}
	}
	return "", errors.New("版本不存在")
}

// ListVersions 文件历史版本列表
func (s *Service) ListVersions(fileID uint) []FileVersion {
	var out []FileVersion
	for _, v := range s.repo.Versions(fileID) {
		out = append(out, *v)
	}
	return out
}

// OpenFile 打开物理文件
func (s *Service) OpenFile(path string) (*os.File, error) {
	return os.Open(path)
}

// fileMD5 计算文件 MD5
func fileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
