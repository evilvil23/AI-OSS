// copy.go 热拷贝引擎：完整备份 / 增量备份 / 还原 的底层文件操作。
//
// 热备份原则：不强制锁文件；被占用/正在写入的文件跳过并标记（不中断整个备份）。
package backup

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"smart-nas/pkg/logger"
)

// skippedFiles 备份过程中被跳过的文件（被占用 / 无读权限）
type skippedFiles struct {
	Items []string // "路径: 原因"
}

func (s *skippedFiles) add(path, reason string) {
	s.Items = append(s.Items, fmt.Sprintf("%s: %s", path, reason))
}

func (s *skippedFiles) isEmpty() bool { return len(s.Items) == 0 }

// badPatternLogged 已告警过的非法通配符模式（逐文件匹配会反复触发，
// 每个非法模式只记录一次告警，避免刷日志）。
var badPatternLogged sync.Map

// warnBadPattern 记录非法排除规则告警（backup.log + 主日志），便于排查配置问题。
func warnBadPattern(p string, err error) {
	if _, ok := badPatternLogged.LoadOrStore(p, struct{}{}); ok {
		return
	}
	logger.Warn("[backup] 排除规则通配符语法非法，该规则不会生效", "pattern", p, "error", err)
}

// excluded 判断相对路径是否命中排除规则。
// 规则支持：精确名称（foo.txt）、目录名（node_modules）、通配符（*.tmp）、
// 目录前缀（带 / 后缀仅匹配源根目录顶层，如 "logs/"）、"# " 开头的注释行（忽略）。
// Windows 下按路径大小写不敏感匹配，与 NTFS 语义一致。
// 非法通配符（如 "[a-"）不参与匹配（该规则自身不生效，不影响其余规则），
// 但会记录一次告警日志。
func excluded(rel string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	caseFold := runtime.GOOS == "windows"
	rel = filepath.ToSlash(rel)
	if caseFold {
		rel = strings.ToLower(rel)
	}
	name := filepath.Base(rel)
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		// 注释行（exclude-list.txt 风格）：跳过，不作为规则参与匹配
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		if caseFold {
			p = strings.ToLower(p)
		}
		if ok, err := filepath.Match(p, name); err != nil {
			warnBadPattern(p, err)
		} else if ok {
			return true
		}
		if ok, err := filepath.Match(strings.TrimPrefix(filepath.ToSlash(p), "/"), rel); err != nil {
			warnBadPattern(p, err)
		} else if ok {
			return true
		}
		// 目录前缀规则（如 "logs/" 或 "logs"）
		prefix := strings.TrimSuffix(filepath.ToSlash(p), "/")
		if prefix != "" && (rel == prefix || strings.HasPrefix(rel, prefix+"/")) {
			return true
		}
	}
	return false
}

// copyFileHot 单文件热拷贝：打开失败（被独占占用）返回错误但不 panic，
// 调用方决定跳过并标记。写临时文件 + rename 保证半成品不污染目标。
func copyFileHot(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err // 被占用 / 无权限：交给调用方标记跳过
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	tmp := dst + ".bakpart-" + fmt.Sprint(os.Getpid())
	defer os.Remove(tmp)
	out, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	// 源文件在拷贝期间被修改：大小对不上则视为不可靠，跳过
	srcInfo, serr := os.Stat(src)
	if serr == nil && srcInfo.Size() != n {
		return 0, fmt.Errorf("文件在备份期间被修改")
	}
	if err := os.Rename(tmp, dst); err != nil {
		return 0, err
	}
	// 保留源文件修改时间：增量比对（mtime+size 指纹）与还原结果的正确性都依赖它
	if serr == nil {
		_ = os.Chtimes(dst, srcInfo.ModTime(), srcInfo.ModTime())
	}
	return n, nil
}

// dirSize 统计目录大小
func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, ierr := d.Info(); ierr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// hashDir 计算备份产物的 SHA-256 校验哈希（文件内容流式累加）
func hashDir(path string, isZip bool) (string, error) {
	h := sha256.New()
	if isZip {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
	} else {
		err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // 读不到的条目跳过（热备份容忍）
			}
			if d.IsDir() {
				return nil
			}
			f, err := os.Open(p)
			if err != nil {
				return nil
			}
			defer f.Close()
			_, _ = io.Copy(h, f)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyHash 重新计算哈希并与历史记录比对（还原前校验）
func verifyHash(path string, isZip bool, expected string) error {
	if expected == "" {
		return nil // 旧记录无哈希：跳过校验（不阻断）
	}
	actual, err := hashDir(path, isZip)
	if err != nil {
		return fmt.Errorf("校验失败: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("备份产物已损坏（校验和不匹配）")
	}
	return nil
}

// fileProg 单文件处理完成回调（file=相对/绝对路径，n=字节数；nil 表示无需进度上报）
type fileProg func(file string, n int64)

// fullCopy 完整备份：src 目录 → dst 目录（跳过排除项与被占用文件）。
// onProg 每处理完一个文件回调一次（可为 nil）。返回拷贝字节数与跳过列表。
func fullCopy(src, dst string, excludes []string, skip *skippedFiles, onProg fileProg) (int64, error) {
	info, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		// 单文件源
		if excluded(filepath.Base(src), excludes) {
			return 0, nil
		}
		n, err := copyFileHot(src, dst)
		if err != nil {
			skip.add(src, err.Error())
			return 0, nil
		}
		reportProg(onProg, src, n)
		return n, nil
	}
	var total int64
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			skip.add(p, err.Error())
			return nil
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		if excluded(rel, excludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		n, cerr := copyFileHot(p, target)
		if cerr != nil {
			skip.add(p, cerr.Error())
			return nil // 热备份：不中断
		}
		reportProg(onProg, p, n)
		total += n
		return nil
	})
	return total, err
}

// incrementalCopy 增量备份：仅拷贝「修改时间/大小不同于父备份快照」的文件。
// parentSnapshot 为父备份内的相对路径 → (大小, 修改时间Unix纳秒) 快照。
// onProg 每处理完一个文件回调一次（可为 nil）。返回拷贝字节数与跳过列表。
func incrementalCopy(src, dst string, excludes []string, parentSnapshot map[string]fileStamp, skip *skippedFiles, onProg fileProg) (int64, error) {
	info, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("增量备份源必须为目录")
	}
	var total int64
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			skip.add(p, err.Error())
			return nil
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		if excluded(rel, excludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		fi, ierr := d.Info()
		if ierr != nil {
			skip.add(p, ierr.Error())
			return nil
		}
		stamp := fileStamp{Size: fi.Size(), ModNano: fi.ModTime().UnixNano()}
		// 快照键统一为「/」分隔（含 zip 条目与 sidecar），查找前需转换分隔符，
		// 否则子目录文件（如 sub/b.txt vs sub\b.txt）永远比对失配
		if parent, ok := parentSnapshot[filepath.ToSlash(rel)]; ok && sameStamp(parent, stamp) {
			return nil // 未变化：跳过
		}
		n, cerr := copyFileHot(p, target)
		if cerr != nil {
			skip.add(p, cerr.Error())
			return nil
		}
		reportProg(onProg, p, n)
		total += n
		return nil
	})
	return total, err
}

// reportProg nil 安全的进度回调调用
func reportProg(onProg fileProg, file string, n int64) {
	if onProg != nil {
		onProg(file, n)
	}
}

// fileStamp 增量比对用的文件指纹
type fileStamp struct {
	Size    int64 `json:"size"`
	ModNano int64 `json:"mod_nano"` // 修改时间（UnixNano；0 表示快照未记录时间戳）
}

// sameStamp 未变化判定：大小一致且修改时间一致。
// 快照侧 ModNano=0（旧版 zip 产物未记录时间戳）时退化为仅比大小。
func sameStamp(a, b fileStamp) bool {
	if a.Size != b.Size {
		return false
	}
	return a.ModNano == b.ModNano || a.ModNano == 0 || b.ModNano == 0
}

// snapshotDir 生成目录快照（相对路径 → 指纹）
func snapshotDir(root string, excludes []string) map[string]fileStamp {
	out := make(map[string]fileStamp)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || excluded(rel, excludes) {
			return nil
		}
		if fi, ierr := d.Info(); ierr == nil {
			out[rel] = fileStamp{Size: fi.Size(), ModNano: fi.ModTime().UnixNano()}
		}
		return nil
	})
	return out
}

// zipDir 将目录压缩为 zip 包（level 1-9）。onProg 每压缩完一个文件回调一次（可为 nil，
// n 为该文件压缩前的原始字节数）。返回产物大小。
func zipDir(src, dstZip string, level int, excludes []string, skip *skippedFiles, onProg fileProg) (int64, error) {
	if level <= 0 {
		level = 6
	}
	if level > 9 {
		level = 9
	}
	if err := os.MkdirAll(filepath.Dir(dstZip), 0o755); err != nil {
		return 0, err
	}
	tmp := dstZip + ".bakpart"
	defer os.Remove(tmp)
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	w := zip.NewWriter(f)
	defer w.Close()
	var total int64
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			skip.add(p, err.Error())
			return nil
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil || rel == "." {
			return nil
		}
		if excluded(rel, excludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			_, cerr := w.Create(rel + "/")
			return cerr
		}
		hdr := &zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Deflate}
		// 写入源文件修改时间：增量快照比对与 zip 还原保留 mtime 都依赖它
		if fi, ierr := d.Info(); ierr == nil {
			hdr.Modified = fi.ModTime()
		}
		entry, cerr := w.CreateHeader(hdr)
		if cerr != nil {
			return cerr
		}
		file, oerr := os.Open(p)
		if oerr != nil {
			skip.add(p, oerr.Error())
			return nil
		}
		n, cerr := io.Copy(entry, file)
		file.Close()
		if cerr != nil {
			skip.add(p, cerr.Error())
			return nil
		}
		reportProg(onProg, p, n)
		total += n
		return nil
	})
	if err != nil {
		f.Close()
		return 0, err
	}
	if err := w.Close(); err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if fi, serr := os.Stat(tmp); serr == nil {
		total = fi.Size() // 压缩包大小以产物为准
	}
	return total, os.Rename(tmp, dstZip)
}

// unzipTo 解压 zip 包到目标目录（防 zip-slip：拒绝逃逸路径）
func unzipTo(zipPath, dst string, skip *skippedFiles) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, zf := range r.File {
		name := filepath.FromSlash(zf.Name)
		if strings.Contains(name, "..") || filepath.IsAbs(name) {
			skip.add(zf.Name, "不安全的压缩包路径，已跳过")
			continue
		}
		target := filepath.Join(dst, name)
		if zf.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			skip.add(zf.Name, err.Error())
			continue
		}
		rc, oerr := zf.Open()
		if oerr != nil {
			skip.add(zf.Name, oerr.Error())
			continue
		}
		out, cerr := os.Create(target)
		if cerr != nil {
			rc.Close()
			skip.add(zf.Name, cerr.Error())
			continue
		}
		_, cerr = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if cerr != nil {
			skip.add(zf.Name, cerr.Error())
		}
	}
	return nil
}

// copyHistoryFiles 将历史备份产物内容拷贝到目标目录（还原用）。
// 带 conflict 处理：overwrite / skip / rename。返回冲突文件列表（ask 模式用）。
func copyHistoryFiles(storePath string, isZip bool, dst string, conflict string, excludeRel []string) (conflicts []string, copied int, err error) {
	// 收集历史产物中的文件（相对路径 → 绝对路径）
	files := map[string]string{} // rel -> src abs
	if isZip {
		r, oerr := zip.OpenReader(storePath)
		if oerr != nil {
			return nil, 0, oerr
		}
		defer r.Close()
		for _, zf := range r.File {
			if zf.FileInfo().IsDir() {
				continue
			}
			files[filepath.FromSlash(zf.Name)] = zf.Name // zip 内路径在 extract 时使用
		}
	} else {
		werr := filepath.WalkDir(storePath, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(storePath, p)
			if rerr != nil {
				return nil
			}
			files[rel] = p
			return nil
		})
		if werr != nil {
			return nil, 0, werr
		}
	}

	askMode := conflict == ConflictAsk || conflict == ""
	for rel, srcRef := range files {
		if excluded(rel, excludeRel) {
			continue
		}
		target := filepath.Join(dst, rel)
		if _, serr := os.Lstat(target); serr == nil {
			// 目标已存在：冲突
			switch conflict {
			case ConflictSkip:
				continue
			case ConflictRename:
				if err := renameOld(target); err != nil {
					return conflicts, copied, err
				}
			default: // overwrite 或 ask
				if askMode {
					conflicts = append(conflicts, rel)
					continue
				}
				// overwrite：继续覆盖
			}
		}
		if askMode {
			// ask 模式下不存在的文件也要先汇报，这里直接复制
		}
		if isZip {
			if err := extractZipEntry(storePath, srcRef, target); err != nil {
				return conflicts, copied, err
			}
		} else {
			if _, err := copyFileHot(srcRef, target); err != nil {
				return conflicts, copied, err
			}
		}
		copied++
	}
	return conflicts, copied, nil
}

// renameOld 冲突重命名：a.txt → a.txt.old-20260102-150405
func renameOld(path string) error {
	ts := time.Now().Format("20060102-150405")
	return os.Rename(path, path+".old-"+ts)
}

// extractZipEntry 从 zip 中提取单个条目到 target
func extractZipEntry(zipPath, entryName, target string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, zf := range r.File {
		if zf.Name != filepath.ToSlash(entryName) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, oerr := zf.Open()
		if oerr != nil {
			return oerr
		}
		defer rc.Close()
		out, cerr := os.Create(target)
		if cerr != nil {
			return cerr
		}
		defer out.Close()
		_, cerr = io.Copy(out, rc)
		return cerr
	}
	return fmt.Errorf("压缩包中不存在条目: %s", entryName)
}
