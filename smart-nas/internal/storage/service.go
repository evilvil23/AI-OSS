package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"smart-nas/internal/config"
	"smart-nas/internal/security"
	"smart-nas/internal/util"
	"smart-nas/pkg/logger"
)

// Service 文件存储服务（基于真实文件系统，按磁盘组织）
type Service struct {
	repo        *Repository
	root        string
	cfg         config.StorageConfig
	argon       security.Argon2Params
	permFn      PermFn
	permScopeFn func(userID uint, dir string) bool
	trashPath        string // 全局回收站目录（设置页/配置文件自定义，优先级最高）
	defaultTrashPath string // 默认回收站目录（运行目录下 data/trash）
}

// PermFn 权限校验回调：判断用户对某路径是否具有读/写权限（由 server 层注入）
type PermFn func(userID uint, path string, write bool) bool

// NewService 创建存储服务
func NewService(repo *Repository, root string, cfg config.StorageConfig, argon security.Argon2Params) (*Service, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	s := &Service{repo: repo, root: root, cfg: cfg, argon: argon, defaultTrashPath: filepath.Join(filepath.Dir(root), "trash")}
	// 初始化磁盘根节点元数据（单盘失败不阻断启动，仅记录警告）
	for _, d := range s.disks() {
		if err := s.ensureDisk(d); err != nil {
			logger.Warn("初始化磁盘根节点失败", "disk", d, "error", err)
		}
	}
	return s, nil
}

// SetPermFn 注入权限校验回调
func (s *Service) SetPermFn(fn PermFn) { s.permFn = fn }

// SetPermScopeFn 注入"用户在目录内是否拥有任意权限范围"回调（v0.13）。
// 用于受限用户的可见性：磁盘/目录只要包含用户可访问的子路径即展示，
// 使其能够逐级导航进入被授权的目录（修复配置了子目录权限却看不到磁盘的问题）。
func (s *Service) SetPermScopeFn(fn func(userID uint, dir string) bool) { s.permScopeFn = fn }

// SetTrashPath 设置全局回收站目录（空表示使用默认位置 运行目录/data/trash）
func (s *Service) SetTrashPath(p string) {
	s.trashPath = strings.TrimSpace(p)
}

// SetDefaultTrashPath 设置默认回收站目录（main 注入运行目录 data/trash；自定义 trashPath 优先）
func (s *Service) SetDefaultTrashPath(p string) {
	if p != "" {
		s.defaultTrashPath = filepath.Clean(p)
	}
}

// DefaultTrashPath 返回当前默认回收站目录（供设置页预填显示）
func (s *Service) DefaultTrashPath() string {
	return filepath.Clean(s.defaultTrashPath)
}

func (s *Service) canRead(userID uint, path string) bool {
	if s.permFn == nil {
		return true
	}
	return s.permFn(userID, path, false)
}

func (s *Service) canWrite(userID uint, path string) bool {
	if s.permFn == nil {
		return true
	}
	return s.permFn(userID, path, true)
}

// permScope 判断用户在 dir 内是否拥有任意权限范围（未注入回调时视为无额外可见性）
func (s *Service) permScope(userID uint, dir string) bool {
	if s.permScopeFn == nil {
		return false
	}
	return s.permScopeFn(userID, dir)
}

// autoDiscoverDisks 自动发现本机磁盘（Windows 盘符）。变量形式便于测试注入。
var autoDiscoverDisks = func() []string {
	if runtime.GOOS == "windows" {
		return util.ListFixedDrives()
	}
	return nil
}

// disks 返回允许访问的磁盘根目录：
// 优先取配置；未配置时 Windows 上自动发现固定盘符；仍未发现回退到 root 单磁盘
func (s *Service) disks() []string {
	if len(s.cfg.Disks) > 0 {
		out := make([]string, 0, len(s.cfg.Disks))
		for _, d := range s.cfg.Disks {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			if abs, err := filepath.Abs(d); err == nil {
				d = abs
			}
			out = append(out, filepath.Clean(d))
		}
		if len(out) > 0 {
			return out
		}
	}
	if drives := autoDiscoverDisks(); len(drives) > 0 {
		return drives
	}
	return []string{filepath.Clean(s.root)}
}

// diskName 生成磁盘显示名：路径即盘符根（如 D:/）时用盘符，否则用末级目录名
func diskName(path string) string {
	p := filepath.Clean(path)
	vol := filepath.VolumeName(p)
	if base := filepath.Base(p); base != "." && base != string(filepath.Separator) {
		if strings.EqualFold(base, vol) {
			return vol
		}
		return base
	}
	if vol != "" {
		return vol
	}
	return p
}

// ensureDisk 确保磁盘根节点元数据存在
func (s *Service) ensureDisk(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	for _, f := range s.repo.All() {
		if f.IsDir && f.ParentID == 0 && samePath(f.StoragePath, path) {
			return nil
		}
	}
	f := &FileMeta{
		Name:        diskName(path),
		ParentID:    0,
		IsDir:       true,
		OwnerID:     0,
		StoragePath: path,
	}
	return s.repo.Create(f)
}

// ListFiles 列出目录文件；parentID=0 时返回磁盘列表
func (s *Service) ListFiles(userID uint, parentID uint, keyword string) ([]FileMeta, error) {
	if keyword != "" {
		return s.search(userID, keyword)
	}
	if parentID == 0 {
		return s.listDisks(userID)
	}
	return s.listDir(userID, parentID)
}

// syncDisks 运行时同步磁盘根节点元数据：
//   - 新接入的磁盘（服务启动后才出现/插入的盘符）自动创建根节点；
//   - 已移除的磁盘保留元数据但从列表隐藏（重新接入后自动恢复）。
//
// 修复：此前磁盘仅在服务启动时发现一次，导致新增盘符不显示、
// 拔出的盘符仍残留在文件管理中。
func (s *Service) syncDisks() {
	current := s.disks()
	for _, d := range current {
		if err := s.ensureDisk(d); err != nil {
			logger.Warn("同步磁盘根节点失败", "disk", d, "error", err)
		}
	}
	// 清理历史遗留的受保护条目元数据（如早期浏览时同步过的 $RECYCLE.BIN），
	// 使其彻底从列表/搜索/统计中消失
	for _, f := range s.repo.All() {
		if !f.Deleted() && isProtectedPath(f.StoragePath) {
			_ = s.repo.Purge(f.ID)
		}
	}
}

// listDisks 列出用户可读的磁盘（先同步当前可用磁盘，隐藏已移除的盘符）
func (s *Service) listDisks(userID uint) ([]FileMeta, error) {
	s.syncDisks()
	current := s.disks()
	var out []FileMeta
	for _, f := range s.repo.All() {
		if !f.IsDisk() || f.Deleted() {
			continue
		}
		// 磁盘已移除（如拔出的移动盘）则隐藏，直到重新接入
		mounted := false
		for _, d := range current {
			if samePath(f.StoragePath, d) {
				mounted = true
				break
			}
		}
		if !mounted {
			continue
		}
		// 磁盘可读，或用户在该磁盘内拥有任意权限范围（可导航进入被授权的子目录）时可见
		if !s.canRead(userID, f.StoragePath) && !s.permScope(userID, f.StoragePath) {
			continue
		}
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// listDir 列出真实目录内容并同步元数据
func (s *Service) listDir(userID uint, parentID uint) ([]FileMeta, error) {
	parent, err := s.repo.Get(parentID)
	if err != nil || !parent.IsDir || parent.Deleted() {
		return nil, errors.New("目录不存在")
	}
	// 目录可读，或用户在其中拥有任意权限范围（需要经此目录导航进入被授权的子目录）时可列出
	if !s.canRead(userID, parent.StoragePath) && !s.permScope(userID, parent.StoragePath) {
		return nil, fmt.Errorf("无权访问「%s」：未授予该目录的读权限", parent.Name)
	}
	entries, err := os.ReadDir(parent.StoragePath)
	if err != nil {
		return nil, err
	}
	var out []FileMeta
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(parent.StoragePath, name)
		// 共用屏蔽规则：隐藏/系统文件（$RECYCLE.BIN、pagefile.sys、
		// NAS 内部 .trash/.versions 等）不出现在列表中；元数据库文件自身亦不列出
		if IsProtectedEntry(full, name) || s.isDBArtifact(full) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if f := s.syncEntry(userID, parentID, parent.StoragePath, name, info); f != nil {
			// 受限用户只看到自己可读的条目，以及"包含其可访问子路径"的目录（可继续导航）；
			// 完整权限用户不受影响（canRead 恒真，全部可见）
			if s.canRead(userID, f.StoragePath) || (f.IsDir && s.permScope(userID, f.StoragePath)) {
				out = append(out, *f)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// syncEntry 将真实目录项同步为元数据（存在则更新，不存在则创建）
func (s *Service) syncEntry(userID, parentID uint, parentPath, name string, info os.FileInfo) *FileMeta {
	full := filepath.Join(parentPath, name)
	if f := s.repo.ByParentName(parentID, name); f != nil {
		if !f.IsDir && f.Size != info.Size() {
			f.Size = info.Size()
			_ = s.repo.Update(f)
		}
		return f
	}
	f := &FileMeta{
		Name:        name,
		ParentID:    parentID,
		IsDir:       info.IsDir(),
		Size:        fileSize(info),
		StoragePath: full,
		OwnerID:     userID,
	}
	if err := s.repo.Create(f); err != nil {
		return nil
	}
	return f
}

// search 按关键词搜索（过滤无读权限与受屏蔽的系统文件）
func (s *Service) search(userID uint, keyword string) ([]FileMeta, error) {
	var out []FileMeta
	for _, f := range s.repo.Search(keyword) {
		if isHiddenFromUser(f) {
			continue
		}
		if !s.canRead(userID, f.StoragePath) {
			continue
		}
		out = append(out, *f)
	}
	return out, nil
}

// GetFileMeta 文件详情（受屏蔽的系统文件不可见）
func (s *Service) GetFileMeta(fileID uint) (*FileMeta, error) {
	f, err := s.repo.Get(fileID)
	if err != nil {
		return nil, err
	}
	if err := denyProtected(f); err != nil {
		return nil, err
	}
	return f, nil
}

// CheckAccess 校验用户对文件/目录的访问权限（受屏蔽的系统文件一律拒绝）
func (s *Service) CheckAccess(userID uint, fileID uint, write bool) bool {
	f, err := s.repo.Get(fileID)
	if err != nil {
		return false
	}
	if isHiddenFromUser(f) {
		return false
	}
	if write {
		return s.canWrite(userID, f.StoragePath)
	}
	return s.canRead(userID, f.StoragePath)
}

// DenyReason 返回拒绝访问的具体原因（v0.14：权限不足时向用户说明缺读还是缺写）。
// 空串表示允许访问、或条目不存在/被屏蔽（这两种按 404 处理，不泄露信息）。
func (s *Service) DenyReason(userID uint, fileID uint, write bool) string {
	f, err := s.repo.Get(fileID)
	if err != nil || isHiddenFromUser(f) {
		return ""
	}
	if write && !s.canWrite(userID, f.StoragePath) {
		return fmt.Sprintf("无权操作「%s」：需要该路径的写权限", f.Name)
	}
	if !write && !s.canRead(userID, f.StoragePath) {
		return fmt.Sprintf("无权访问「%s」：需要该路径的读权限", f.Name)
	}
	return ""
}

// CreateDir 创建目录（同步到真实文件系统）
func (s *Service) CreateDir(userID uint, name string, parentID uint) (*FileMeta, error) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return nil, errors.New("目录名非法")
	}
	if util.IsProtectedName(name) {
		return nil, ErrProtected
	}
	parentPath, err := s.resolveDirPath(parentID)
	if err != nil {
		return nil, err
	}
	if isProtectedPath(parentPath) {
		return nil, ErrProtected
	}
	if !s.canWrite(userID, parentPath) {
		return nil, fmt.Errorf("无权在「%s」创建文件夹：需要该目录的写权限", filepath.Base(parentPath))
	}
	if s.repo.ByParentName(parentID, name) != nil {
		return nil, errors.New("同名目录已存在")
	}
	full := filepath.Join(parentPath, name)
	if err := os.MkdirAll(full, 0o755); err != nil {
		return nil, err
	}
	f := &FileMeta{
		Name:        name,
		ParentID:    parentID,
		IsDir:       true,
		OwnerID:     userID,
		StoragePath: full,
	}
	if err := s.repo.Create(f); err != nil {
		return nil, err
	}
	return f, nil
}

// DeleteFile 删除（移入回收站，物理移动文件）
func (s *Service) DeleteFile(userID uint, fileIDs []uint) error {
	for _, id := range fileIDs {
		f, err := s.repo.Get(id)
		if err != nil {
			return err
		}
		if f.IsDisk() {
			return errors.New("磁盘根目录不可删除")
		}
		if err := denyProtected(f); err != nil {
			return err
		}
		if !s.canWrite(userID, f.StoragePath) {
			return fmt.Errorf("无权删除「%s」：需要该路径的写权限", f.Name)
		}
		trashDir := s.trashDirFor(f.StoragePath)
		if err := os.MkdirAll(trashDir, 0o755); err != nil {
			return err
		}
		trashPath := filepath.Join(trashDir, fmt.Sprintf("%d_%s", f.ID, filepath.Base(f.StoragePath)))
		if err := movePath(f.StoragePath, trashPath); err != nil {
			return err
		}
		f.TrashOrigin = f.StoragePath
		f.StoragePath = trashPath
		f.DeletedAt = time.Now()
		if err := s.repo.Update(f); err != nil {
			return err
		}
	}
	return nil
}

// RestoreFromTrash 从回收站恢复（物理移回原路径）
func (s *Service) RestoreFromTrash(userID uint, fileIDs []uint) error {
	for _, id := range fileIDs {
		f, err := s.repo.Get(id)
		if err != nil {
			return err
		}
		if !f.Deleted() {
			return errors.New("文件不在回收站")
		}
		origin := f.TrashOrigin
		if origin == "" {
			origin = f.StoragePath
		}
		// 受保护路径（系统目录等）不可作为恢复目标。
		// 仅做名称级判断：盘符根目录（如 S:\）自带隐藏/系统属性，
		// 不能据此拦截其下正常文件的还原。
		if isProtectedPath(origin) || util.IsProtectedName(filepath.Base(origin)) {
			return ErrProtected
		}
		if !s.canWrite(userID, origin) {
			return fmt.Errorf("无权还原「%s」：需要原目录的写权限", f.Name)
		}
		if err := movePath(f.StoragePath, origin); err != nil {
			return err
		}
		f.StoragePath = origin
		f.TrashOrigin = ""
		f.DeletedAt = time.Time{}
		if err := s.repo.Update(f); err != nil {
			return err
		}
	}
	return nil
}

// ListTrash 回收站文件列表
func (s *Service) ListTrash(userID uint) []*FileMeta {
	list := s.repo.InTrash()
	out := make([]*FileMeta, 0, len(list))
	for _, f := range list {
		if isHiddenFromUser(f) {
			continue
		}
		if !s.canRead(userID, f.TrashOrigin) && !s.canRead(userID, f.StoragePath) {
			continue
		}
		cp := *f
		out = append(out, &cp)
	}
	return out
}

// PurgeFiles 从回收站物理删除指定条目（不可恢复），返回成功删除的数量
func (s *Service) PurgeFiles(userID uint, fileIDs []uint) (int, error) {
	n := 0
	for _, id := range fileIDs {
		f, err := s.repo.Get(id)
		if err != nil {
			continue
		}
		if !f.Deleted() {
			return n, errors.New("文件不在回收站")
		}
		if !s.canWrite(userID, f.TrashOrigin) && !s.canWrite(userID, f.StoragePath) {
			return n, fmt.Errorf("无权删除「%s」：需要原路径的写权限", f.Name)
		}
		if err := os.RemoveAll(f.StoragePath); err != nil {
			return n, err
		}
		if err := s.repo.Purge(f.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ClearTrash 一键清空回收站（无权限条目导致中断前已清理的仍然生效），返回清理数量
func (s *Service) ClearTrash(userID uint) (int, error) {
	var ids []uint
	for _, f := range s.repo.InTrash() {
		ids = append(ids, f.ID)
	}
	return s.PurgeFiles(userID, ids)
}

// RestoreAllTrash 一键还原回收站全部条目（逐条处理，失败项跳过并记录），返回成功数量
func (s *Service) RestoreAllTrash(userID uint) (int, error) {
	var ids []uint
	for _, f := range s.repo.InTrash() {
		ids = append(ids, f.ID)
	}
	n := 0
	for _, id := range ids {
		if err := s.RestoreFromTrash(userID, []uint{id}); err == nil {
			n++
		} else {
			logger.Warn("回收站条目还原失败", "id", id, "error", err)
		}
	}
	return n, nil
}

// PurgeTrash 物理清理回收站（定时任务调用），返回清理字节数
func (s *Service) PurgeTrash(userID uint, before time.Time) (int64, error) {
	var freed int64
	for _, f := range s.repo.InTrash() {
		if f.Deleted() && f.DeletedAt.Before(before) {
			if info, err := os.Stat(f.StoragePath); err == nil {
				freed += info.Size()
			}
			_ = os.RemoveAll(f.StoragePath)
			_ = s.repo.Purge(f.ID)
		}
	}
	return freed, nil
}

// RenameFile 重命名（同步真实文件系统）
func (s *Service) RenameFile(userID uint, fileID uint, newName string) error {
	if newName == "" || strings.ContainsAny(newName, "/\\") {
		return errors.New("文件名非法")
	}
	f, err := s.repo.Get(fileID)
	if err != nil {
		return err
	}
	if f.IsDisk() {
		return errors.New("磁盘根目录不可重命名")
	}
	if err := denyProtected(f); err != nil {
		return err
	}
	if util.IsProtectedName(newName) {
		return ErrProtected
	}
	if !s.canWrite(userID, f.StoragePath) {
		return fmt.Errorf("无权重命名「%s」：需要该路径的写权限", f.Name)
	}
	newPath := filepath.Join(filepath.Dir(f.StoragePath), newName)
	if err := os.Rename(f.StoragePath, newPath); err != nil {
		return err
	}
	f.Name = newName
	f.StoragePath = newPath
	return s.repo.Update(f)
}

// MoveFile 移动文件（同步真实文件系统）
func (s *Service) MoveFile(userID uint, fileIDs []uint, targetDirID uint) error {
	targetPath, err := s.resolveDirPath(targetDirID)
	if err != nil {
		return err
	}
	if isProtectedPath(targetPath) {
		return ErrProtected
	}
	if !s.canWrite(userID, targetPath) {
		return fmt.Errorf("无权移动到「%s」：需要目标目录的写权限", filepath.Base(targetPath))
	}
	for _, id := range fileIDs {
		f, err := s.repo.Get(id)
		if err != nil {
			return err
		}
		if f.ID == targetDirID {
			return errors.New("不能移动到自身")
		}
		if f.IsDisk() {
			return errors.New("磁盘根目录不可移动")
		}
		if err := denyProtected(f); err != nil {
			return err
		}
		if !s.canWrite(userID, f.StoragePath) {
			return fmt.Errorf("无权移动「%s」：需要该路径的写权限", f.Name)
		}
		newPath := filepath.Join(targetPath, f.Name)
		if err := movePath(f.StoragePath, newPath); err != nil {
			return err
		}
		f.ParentID = targetDirID
		f.StoragePath = newPath
		if err := s.repo.Update(f); err != nil {
			return err
		}
	}
	return nil
}

// CopyFile 复制文件/目录到目标目录（目录递归复制）。目标需可写，来源需可读。
func (s *Service) CopyFile(userID uint, ids []uint, targetDirID uint) error {
	targetPath, err := s.resolveDirPath(targetDirID)
	if err != nil {
		return err
	}
	if !s.canWrite(userID, targetPath) {
		return fmt.Errorf("无权复制到「%s」：需要目标目录的写权限", filepath.Base(targetPath))
	}
	targetProtected := isProtectedPath(targetPath)
	for _, id := range ids {
		f, err := s.repo.Get(id)
		if err != nil {
			return err
		}
		if f.Deleted() || !s.canRead(userID, f.StoragePath) {
			return fmt.Errorf("无权复制「%s」：需要该路径的读权限", f.Name)
		}
		if f.ID == targetDirID {
			return errors.New("不能复制到自身")
		}
		if f.IsDisk() {
			return errors.New("磁盘根目录不可复制")
		}
		if err := denyProtected(f); err != nil {
			return err
		}
		if targetProtected {
			return ErrProtected
		}
		dst := filepath.Join(targetPath, f.Name)
		if f.IsDir {
			if err := s.copyDirRecursive(userID, f.StoragePath, dst); err != nil {
				return err
			}
		} else {
			if err := copyPath(f.StoragePath, dst); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyDirRecursive 递归复制目录结构
func (s *Service) copyDirRecursive(userID uint, src, dst string) error {
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		return errors.New("复制源目录无效")
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if IsProtectedEntry(filepath.Join(src, name), name) || s.isDBArtifact(filepath.Join(src, name)) {
			continue
		}
		childSrc := filepath.Join(src, name)
		childDst := filepath.Join(dst, name)
		if e.IsDir() {
			if err := s.copyDirRecursive(userID, childSrc, childDst); err != nil {
				return err
			}
		} else {
			if err := copyPath(childSrc, childDst); err != nil {
				return err
			}
		}
	}
	return nil
}

// CreateShareLink 创建共享链接
func (s *Service) CreateShareLink(userID, fileID uint, expireHours int, password string) (*FileMeta, string, error) {
	f, err := s.repo.Get(fileID)
	if err != nil {
		return nil, "", err
	}
	if err := denyProtected(f); err != nil {
		return nil, "", err
	}
	if f.Deleted() || !s.canRead(userID, f.StoragePath) {
		return nil, "", errors.New("文件不存在")
	}
	token := util.RandomHex(16)
	f.Shared = true
	f.ShareToken = token
	if expireHours > 0 {
		f.ShareExpires = time.Now().Add(time.Duration(expireHours) * time.Hour)
	} else {
		f.ShareExpires = time.Time{}
	}
	if password != "" {
		salt, hash, err := security.HashPassword(password, s.argon)
		if err != nil {
			return nil, "", err
		}
		f.SharePwdHash = salt + ":" + hash
	} else {
		f.SharePwdHash = ""
	}
	if err := s.repo.Update(f); err != nil {
		return nil, "", err
	}
	return f, token, nil
}

// ResolveShare 通过 token 校验共享并获取文件 (password 可为空)
func (s *Service) ResolveShare(token, password string) (*FileMeta, error) {
	for _, f := range s.repo.All() {
		if isHiddenFromUser(f) {
			continue
		}
		if f.Shared && f.ShareToken == token && !f.Deleted() {
			if !f.ShareExpires.IsZero() && time.Now().After(f.ShareExpires) {
				return nil, errors.New("共享链接已过期")
			}
			if f.SharePwdHash != "" {
				parts := strings.SplitN(f.SharePwdHash, ":", 2)
				if len(parts) != 2 || !security.VerifyPassword(password, parts[0], parts[1], s.argon) {
					return nil, errors.New("共享密码错误")
				}
			}
			return f, nil
		}
	}
	return nil, errors.New("共享链接不存在")
}

// OpenShare 打开共享文件的只读流（仅文件）
func (s *Service) OpenShare(f *FileMeta) (io.ReadCloser, int64, error) {
	if f.IsDir {
		return nil, 0, errors.New("目录不可直接下载，请打包")
	}
	h, err := os.Open(f.StoragePath)
	if err != nil {
		return nil, 0, err
	}
	return h, f.Size, nil
}

// CheckDedup 秒传去重检查：同一 MD5 且未删除的文件已存在则返回其 ID
func (s *Service) CheckDedup(md5 string, size int64) (fileID uint, exists bool) {
	if f := s.findByMD5(md5, size); f != nil {
		return f.ID, true
	}
	return 0, false
}

// findByMD5 查找同 MD5 且同大小的未删除文件（走 md5 索引）
func (s *Service) findByMD5(md5 string, size int64) *FileMeta {
	return s.repo.ByMD5(md5, size)
}

// GetStorageStats 存储统计
func (s *Service) GetStorageStats(userID uint) (*StorageStats, error) {
	st := &StorageStats{}
	for _, f := range s.repo.All() {
		if isHiddenFromUser(f) {
			continue
		}
		if f.IsDir {
			if !f.Deleted() {
				st.TotalDirs++
			}
			continue
		}
		if f.Deleted() {
			st.TrashFiles++
			st.TrashBytes += f.Size
			continue
		}
		st.TotalFiles++
		st.UsedBytes += f.Size
	}
	st.VersionBytes = s.repo.VersionBytes()
	return st, nil
}

// resolveDirPath 解析目录 ID 对应的真实路径
func (s *Service) resolveDirPath(parentID uint) (string, error) {
	if parentID == 0 {
		return "", errors.New("请先进入目标文件夹")
	}
	p, err := s.repo.Get(parentID)
	if err != nil || !p.IsDir || p.Deleted() {
		return "", errors.New("目标目录不存在")
	}
	if p.StoragePath == "" {
		return "", errors.New("目标目录路径无效")
	}
	return p.StoragePath, nil
}

// trashDirFor 返回回收站目录：设置页自定义位置优先；
// 默认集中存放于运行目录 data/trash（即 ./data/trash），不再在各盘符下建 .trash
func (s *Service) trashDirFor(path string) string {
	if s.trashPath != "" {
		return filepath.Clean(s.trashPath)
	}
	return filepath.Clean(s.defaultTrashPath)
}

// isDBArtifact 判断路径是否为元数据库自身或其 WAL 伴随文件
func (s *Service) isDBArtifact(p string) bool {
	db := s.repo.DBPath()
	return samePath(p, db) || samePath(p, db+"-wal") || samePath(p, db+"-shm") || samePath(p, db+"-journal")
}

// ---- 路径工具 ----

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}


func fileSize(info os.FileInfo) int64 {
	if info.IsDir() {
		return 0
	}
	return info.Size()
}

// movePath 移动文件/目录，跨设备时回退为复制后删除
func movePath(src, dst string) error {
	if src == "" {
		return errors.New("源路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyPath(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

// copyPath 复制文件（保留内容）
func copyPath(src, dst string) error {
	if src == "" {
		return errors.New("源路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
