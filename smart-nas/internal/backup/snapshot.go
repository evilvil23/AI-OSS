// snapshot.go 备份产物快照（增量比对用）+ 备份内容浏览。
package backup

import (
	"archive/zip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// mergeDirSnapshot 将目录内容并入快照（相对路径 → 指纹）
func mergeDirSnapshot(root string, snap map[string]fileStamp) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		if fi, ierr := d.Info(); ierr == nil {
			snap[filepath.ToSlash(rel)] = fileStamp{Size: fi.Size(), ModNano: fi.ModTime().UnixNano()}
		}
		return nil
	})
}

// mergeZipSnapshot 将 zip 内容并入快照
func mergeZipSnapshot(zipPath string, snap map[string]fileStamp) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return
	}
	defer r.Close()
	for _, zf := range r.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		snap[filepath.ToSlash(zf.Name)] = fileStamp{Size: int64(zf.UncompressedSize64)}
	}
}

// BackupEntry 备份内容条目（浏览备份内文件列表，需求 §2.5.3）
type BackupEntry struct {
	RelPath string `json:"rel_path"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time,omitempty"`
	IsDir   bool   `json:"is_dir"`
}

// ListBackupContents 浏览备份产物内的文件列表（目录或 zip 均支持）
func ListBackupContents(storePath string) ([]BackupEntry, error) {
	if strings.HasSuffix(strings.ToLower(storePath), ".zip") {
		return listZipContents(storePath)
	}
	var out []BackupEntry
	err := filepath.WalkDir(storePath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(storePath, p)
		if rerr != nil || rel == "." {
			return nil
		}
		e := BackupEntry{RelPath: filepath.ToSlash(rel), IsDir: d.IsDir()}
		if fi, ierr := d.Info(); ierr == nil {
			e.Size = fi.Size()
			e.ModTime = fi.ModTime().Format("2006-01-02 15:04:05")
		}
		out = append(out, e)
		return nil
	})
	return out, err
}

func listZipContents(zipPath string) ([]BackupEntry, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []BackupEntry
	for _, zf := range r.File {
		e := BackupEntry{
			RelPath: filepath.ToSlash(zf.Name),
			Size:    int64(zf.UncompressedSize64),
			IsDir:   zf.FileInfo().IsDir(),
			ModTime: zf.Modified.Format("2006-01-02 15:04:05"),
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out, nil
}

// OpenBackupFile 打开备份产物中的单个文件（还原部分文件用，返回只读流）
func OpenBackupFile(storePath, relPath string) (io.ReadCloser, error) {
	relPath = filepath.ToSlash(strings.TrimPrefix(relPath, "/"))
	if strings.HasSuffix(strings.ToLower(storePath), ".zip") {
		r, err := zip.OpenReader(storePath)
		if err != nil {
			return nil, err
		}
		for _, zf := range r.File {
			if zf.Name == relPath {
				rc, oerr := zf.Open()
				if oerr != nil {
					r.Close()
					return nil, oerr
				}
				return &zipEntryCloser{ReadCloser: rc, zr: r}, nil
			}
		}
		r.Close()
		return nil, fs.ErrNotExist
	}
	return os.Open(filepath.Join(storePath, filepath.FromSlash(relPath)))
}

// zipEntryCloser 包装 zip 条目流：关闭时同时关闭 zip 归档
type zipEntryCloser struct {
	io.ReadCloser
	zr *zip.ReadCloser
}

func (z *zipEntryCloser) Close() error {
	err := z.ReadCloser.Close()
	_ = z.zr.Close()
	return err
}
