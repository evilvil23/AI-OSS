package storage

import (
	"os"
	"path/filepath"
	"testing"

	"smart-nas/internal/config"
	"smart-nas/internal/security"
)

func newTestStorage(t *testing.T) (*Service, string) {
	t.Helper()
	base := t.TempDir()
	diskDir := filepath.Join(base, "disk1")
	repo, err := NewRepository(filepath.Join(base, "db", "metadata.db"), filepath.Join(base, "db"))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	cfg := config.StorageConfig{Disks: []string{diskDir}}
	argon := security.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}
	svc, err := NewService(repo, filepath.Join(base, "root"), cfg, argon)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, diskDir
}

func diskRoot(t *testing.T, s *Service) *FileMeta {
	t.Helper()
	files, err := s.ListFiles(0, 0, "")
	if err != nil || len(files) != 1 {
		t.Fatalf("应返回 1 个磁盘节点, got %d, err=%v", len(files), err)
	}
	return &files[0]
}

// TestListDisksSyncsNewDisk：服务启动后新出现的磁盘在下一次列表时自动出现
func TestListDisksSyncsNewDisk(t *testing.T) {
	base := t.TempDir()
	diskA := filepath.Join(base, "diskA")
	diskB := filepath.Join(base, "diskB")
	if err := os.MkdirAll(diskA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(diskB, 0o755); err != nil {
		t.Fatal(err)
	}
	// 模拟启动时只发现 diskA
	orig := autoDiscoverDisks
	discovered := []string{diskA}
	autoDiscoverDisks = func() []string { return discovered }
	t.Cleanup(func() { autoDiscoverDisks = orig })

	repo, err := NewRepository(filepath.Join(base, "db", "metadata.db"), filepath.Join(base, "db"))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	cfg := config.StorageConfig{} // 未配置 disks → 走自动发现
	argon := security.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}
	svc, err := NewService(repo, filepath.Join(base, "root"), cfg, argon)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	files, err := svc.ListFiles(0, 0, "")
	if err != nil || len(files) != 1 {
		t.Fatalf("启动后应只有 1 个磁盘, got %d err=%v", len(files), err)
	}
	// 运行中插入新磁盘 diskB → 下一次列表自动同步出现
	discovered = []string{diskA, diskB}
	files, err = svc.ListFiles(0, 0, "")
	if err != nil || len(files) != 2 {
		t.Fatalf("新磁盘应自动出现, got %d err=%v", len(files), err)
	}
	names := map[string]bool{}
	for _, f := range files {
		names[f.Name] = true
	}
	if !names["diskA"] || !names["diskB"] {
		t.Fatalf("磁盘名不符: %+v", files)
	}
}

// TestListDisksHidesRemovedDisk：拔出的磁盘从列表隐藏，重新接入后恢复
func TestListDisksHidesRemovedDisk(t *testing.T) {
	base := t.TempDir()
	diskA := filepath.Join(base, "diskA")
	diskB := filepath.Join(base, "diskB")
	for _, d := range []string{diskA, diskB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	orig := autoDiscoverDisks
	discovered := []string{diskA, diskB}
	autoDiscoverDisks = func() []string { return discovered }
	t.Cleanup(func() { autoDiscoverDisks = orig })

	repo, err := NewRepository(filepath.Join(base, "db", "metadata.db"), filepath.Join(base, "db"))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	cfg := config.StorageConfig{}
	argon := security.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}
	svc, err := NewService(repo, filepath.Join(base, "root"), cfg, argon)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	files, _ := svc.ListFiles(0, 0, "")
	if len(files) != 2 {
		t.Fatalf("应有 2 个磁盘, got %d", len(files))
	}
	// 模拟拔出 diskB → 隐藏（元数据保留）
	discovered = []string{diskA}
	files, _ = svc.ListFiles(0, 0, "")
	if len(files) != 1 || files[0].Name != "diskA" {
		t.Fatalf("拔出后应只剩 diskA: %+v", files)
	}
	// 模拟重新接入 → 恢复显示（元数据未丢失，ID 不变）
	var diskBID uint
	for _, f := range svc.repo.All() {
		if f.Name == "diskB" {
			diskBID = f.ID
		}
	}
	if diskBID == 0 {
		t.Fatal("diskB 元数据应保留")
	}
	discovered = []string{diskA, diskB}
	files, _ = svc.ListFiles(0, 0, "")
	if len(files) != 2 {
		t.Fatalf("重新接入后应恢复 2 个磁盘, got %d", len(files))
	}
	for _, f := range files {
		if f.Name == "diskB" && f.ID != diskBID {
			t.Fatalf("diskB ID 应保持不变: %d != %d", f.ID, diskBID)
		}
	}
}

// TestListDisksParentZero：parent_id=0 应返回磁盘根节点（IsDisk 判定）
func TestListDisksParentZero(t *testing.T) {
	s, diskDir := newTestStorage(t)
	d := diskRoot(t, s)
	if !d.IsDir || d.ParentID != 0 {
		t.Fatalf("磁盘节点应为根目录: %+v", d)
	}
	if filepath.Clean(d.StoragePath) != filepath.Clean(diskDir) {
		t.Fatalf("磁盘节点 StoragePath 不符: %s != %s", d.StoragePath, diskDir)
	}
	if !d.IsDisk() {
		t.Fatal("磁盘节点 IsDisk() 应为 true")
	}
}

// TestDiskRootProtected：磁盘根不可删除 / 重命名 / 移动
func TestDiskRootProtected(t *testing.T) {
	s, _ := newTestStorage(t)
	d := diskRoot(t, s)

	if err := s.DeleteFile(0, []uint{d.ID}); err == nil {
		t.Fatal("磁盘根目录应不可删除")
	}
	if err := s.RenameFile(0, d.ID, "newname"); err == nil {
		t.Fatal("磁盘根目录应不可重命名")
	}
	sub, err := s.CreateDir(0, "照片", d.ID)
	if err != nil {
		t.Fatalf("CreateDir: %v", err)
	}
	if err := s.MoveFile(0, []uint{d.ID}, sub.ID); err == nil {
		t.Fatal("磁盘根目录应不可移动")
	}
	if err := s.CopyFile(0, []uint{d.ID}, sub.ID); err == nil {
		t.Fatal("磁盘根目录应不可复制")
	}
}

// TestCreateDirAndListDir：新建文件夹同步到真实文件系统
func TestCreateDirAndListDir(t *testing.T) {
	s, diskDir := newTestStorage(t)
	d := diskRoot(t, s)
	sub, err := s.CreateDir(0, "照片", d.ID)
	if err != nil {
		t.Fatalf("CreateDir: %v", err)
	}
	if _, err := os.Stat(sub.StoragePath); err != nil {
		t.Fatalf("真实目录未创建: %v", err)
	}
	files, err := s.ListFiles(0, d.ID, "")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 || files[0].Name != "照片" {
		t.Fatalf("目录列表不符: %+v", files)
	}
	_ = diskDir
}

// TestDeleteToTrashAndGlobalTrash：删除移入回收站；自定义回收站路径优先
func TestDeleteToTrashAndGlobalTrash(t *testing.T) {
	s, diskDir := newTestStorage(t)
	d := diskRoot(t, s)
	sub, err := s.CreateDir(0, "照片", d.ID)
	if err != nil {
		t.Fatalf("CreateDir: %v", err)
	}
	// 未配置回收站位置 → 默认集中存放于运行目录 data/trash（base/trash）
	if err := s.DeleteFile(0, []uint{sub.ID}); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	trash := s.ListTrash(0)
	if len(trash) != 1 || trash[0].Name != "照片" {
		t.Fatalf("回收站列表不符: %+v", trash)
	}
	if dir := s.trashDirFor(filepath.Join(diskDir, "x")); filepath.Clean(dir) != filepath.Join(filepath.Dir(diskDir), "trash") {
		t.Fatalf("默认回收站应为 data/trash, got %s", dir)
	}
	// 配置自定义回收站 → 自定义路径优先
	s.SetTrashPath(filepath.Join(diskDir, "..", "global-trash"))
	if dir := s.trashDirFor(filepath.Join(diskDir, "x")); filepath.Base(dir) != "global-trash" {
		t.Fatalf("自定义回收站应优先, got %s", dir)
	}
}

// TestListDirFiltersProtectedEntries：隐藏/系统文件不出现在列表中
func TestListDirFiltersProtectedEntries(t *testing.T) {
	s, diskDir := newTestStorage(t)
	d := diskRoot(t, s)
	for _, name := range []string{"$RECYCLE.BIN", "System Volume Information", "pagefile.sys", "正常目录"} {
		if err := os.MkdirAll(filepath.Join(diskDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files, err := s.ListFiles(0, d.ID, "")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 || files[0].Name != "正常目录" {
		t.Fatalf("受保护条目应被过滤: %+v", files)
	}
	// 搜索同样过滤（受保护条目即使元数据存在也不可见）
	for _, name := range []string{"$RECYCLE.BIN", "pagefile.sys"} {
		if err := s.repo.Create(&FileMeta{Name: name, ParentID: d.ID, IsDir: true, StoragePath: filepath.Join(diskDir, name)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListFiles(0, 0, "RECYCLE")
	if err != nil || len(got) != 0 {
		t.Fatalf("搜索应过滤受保护条目: %+v err=%v", got, err)
	}
}

// TestOperationsDenyProtected：所有文件操作对受保护条目统一拒绝
func TestOperationsDenyProtected(t *testing.T) {
	s, diskDir := newTestStorage(t)
	d := diskRoot(t, s)
	// 直接注入受保护条目元数据（模拟历史遗留/直接携带 ID 访问）
	prot := &FileMeta{Name: "$RECYCLE.BIN", ParentID: d.ID, IsDir: true, StoragePath: filepath.Join(diskDir, "$RECYCLE.BIN")}
	if err := s.repo.Create(prot); err != nil {
		t.Fatal(err)
	}
	normal, err := s.CreateDir(0, "目标", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFileMeta(prot.ID); err == nil {
		t.Fatal("GetFileMeta 应拒绝受保护条目")
	}
	if s.CheckAccess(0, prot.ID, false) || s.CheckAccess(0, prot.ID, true) {
		t.Fatal("CheckAccess 应拒绝受保护条目")
	}
	if err := s.DeleteFile(0, []uint{prot.ID}); err == nil {
		t.Fatal("DeleteFile 应拒绝受保护条目")
	}
	if err := s.RenameFile(0, prot.ID, "x"); err == nil {
		t.Fatal("RenameFile 应拒绝受保护条目")
	}
	if err := s.MoveFile(0, []uint{prot.ID}, normal.ID); err == nil {
		t.Fatal("MoveFile 应拒绝受保护条目")
	}
	if err := s.CopyFile(0, []uint{prot.ID}, normal.ID); err == nil {
		t.Fatal("CopyFile 应拒绝受保护条目")
	}
	if _, _, err := s.CreateShareLink(0, prot.ID, 0, ""); err == nil {
		t.Fatal("CreateShareLink 应拒绝受保护条目")
	}
	// 目录名不可创建为系统保留名
	if _, err := s.CreateDir(0, "pagefile.sys", d.ID); err == nil {
		t.Fatal("CreateDir 应拒绝系统保留名")
	}
	// 正常条目不受影响
	if _, err := s.GetFileMeta(normal.ID); err != nil {
		t.Fatalf("正常条目应可访问: %v", err)
	}
}

// TestTrashPurgeClearRestoreAll：回收站物理删除 / 一键清空 / 一键还原
func TestTrashPurgeClearRestoreAll(t *testing.T) {
	s, diskDir := newTestStorage(t)
	d := diskRoot(t, s)
	var ids []uint
	for _, name := range []string{"a", "b", "c"} {
		p := filepath.Join(diskDir, name)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		files, err := s.ListFiles(0, d.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if f.Name == name && !f.IsDir {
				ids = append(ids, f.ID)
			}
		}
	}
	if len(ids) != 3 {
		t.Fatalf("应同步到 3 个文件, got %d", len(ids))
	}
	// 删除 a、b → 物理删除 a
	if err := s.DeleteFile(0, ids[:2]); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	n, err := s.PurgeFiles(0, []uint{ids[0]})
	if err != nil || n != 1 {
		t.Fatalf("PurgeFiles: n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(diskDir, "a")); !os.IsNotExist(err) {
		t.Fatal("物理删除后原文件不应存在（已入回收站并被清理）")
	}
	if len(s.ListTrash(0)) != 1 {
		t.Fatalf("回收站应剩 1 项, got %d", len(s.ListTrash(0)))
	}
	// 一键还原 → b 回到原位置
	n, err = s.RestoreAllTrash(0)
	if err != nil || n != 1 {
		t.Fatalf("RestoreAllTrash: n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(diskDir, "b")); err != nil {
		t.Fatalf("还原后文件应存在: %v", err)
	}
	// 删除 b、c → 一键清空
	if err := s.DeleteFile(0, ids[1:]); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	n, err = s.ClearTrash(0)
	if err != nil || n != 2 {
		t.Fatalf("ClearTrash: n=%d err=%v", n, err)
	}
	if len(s.ListTrash(0)) != 0 {
		t.Fatal("清空后回收站应为空")
	}
}

// PublicDetail 详情视图保留文件真实路径（列表 Public 脱敏，详情 PublicDetail 保留）
func TestPublicDetail(t *testing.T) {
	file := &FileMeta{Name: "a.mp4", IsDir: false, StoragePath: `S:\video\a.mp4`, ShareToken: "tok", TrashOrigin: `S:\old`}
	pub := file.Public()
	if pub.StoragePath != "" {
		t.Fatalf("Public 应脱敏文件路径: %q", pub.StoragePath)
	}
	if pub.ShareToken != "" || pub.TrashOrigin != "" {
		t.Fatal("Public 应脱敏分享/回收站字段")
	}
	det := file.PublicDetail()
	if det.StoragePath != `S:\video\a.mp4` {
		t.Fatalf("PublicDetail 应保留文件路径: %q", det.StoragePath)
	}
	if det.ShareToken != "" || det.TrashOrigin != "" {
		t.Fatal("PublicDetail 仍应脱敏分享/回收站字段")
	}
	// 目录两者均保留路径
	dir := &FileMeta{Name: "d", IsDir: true, StoragePath: `S:\video\d`}
	if dir.Public().StoragePath != `S:\video\d` {
		t.Fatal("目录 Public 应保留路径")
	}
}