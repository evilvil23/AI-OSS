// zipindex.go zip 条目索引（rel -> zip 内名称），供还原链使用。
package backup

import (
	"archive/zip"
	"path/filepath"
	"strings"
)

// newZipIndex 建立 zip 内文件索引：相对路径（slash）→ zip 条目名
func newZipIndex(zipPath string) (map[string]string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := make(map[string]string, len(r.File))
	for _, zf := range r.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		name := strings.TrimSuffix(zf.Name, "/")
		out[filepath.ToSlash(name)] = zf.Name
	}
	return out, nil
}
