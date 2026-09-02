package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"smart-nas/internal/config"
	"smart-nas/internal/security"
	"smart-nas/internal/user"
)

// newScopedStorage 构造带用户权限回调的存储服务（复现 user2 场景）：
// 磁盘 disk1 下有 catalog（用户仅有其读权限）与 private（无权限）两个目录。
func newScopedStorage(t *testing.T) (*Service, *user.Service, string, string) {
	t.Helper()
	base := t.TempDir()
	diskDir := filepath.Join(base, "disk1")
	catalogDir := filepath.Join(diskDir, "catalog")
	privateDir := filepath.Join(diskDir, "private")
	for _, d := range []string{catalogDir, privateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "可见.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(privateDir, "机密.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo, err := NewRepository(filepath.Join(base, "db", "metadata.db"), filepath.Join(base, "db"))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	argon := security.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}
	svc, err := NewService(repo, filepath.Join(base, "root"), config.StorageConfig{Disks: []string{diskDir}}, argon)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	userRepo, err := user.NewRepository(filepath.Join(base, "udb"))
	if err != nil {
		t.Fatal(err)
	}
	userSvc := user.NewService(userRepo, argon)
	if err := userSvc.EnsureMaster("admin", "admin123"); err != nil {
		t.Fatal(err)
	}
	svc.SetPermFn(func(uid uint, path string, write bool) bool {
		return userSvc.CanAccess(uid, path, write)
	})
	svc.SetPermScopeFn(func(uid uint, dir string) bool {
		return userSvc.HasScopeUnder(uid, dir)
	})
	return svc, userSvc, diskDir, catalogDir
}

// TestRestrictedUserNavigation：配置了子目录权限的用户能看到磁盘、
// 经磁盘导航进入被授权目录，且看不到未授权的兄弟目录（v0.13 修复）
func TestRestrictedUserNavigation(t *testing.T) {
	svc, userSvc, diskDir, catalogDir := newScopedStorage(t)
	u, err := userSvc.CreateUser("user2", "pass123", "user", []user.Permission{
		{Path: catalogDir, Read: true, Write: false},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// 磁盘列表：应能看到磁盘（因其包含被授权子目录）
	disks, err := svc.ListFiles(u.ID, 0, "")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(disks) != 1 {
		t.Fatalf("受限用户应看到 1 个磁盘, got %d", len(disks))
	}
	disk := disks[0]

	// 进入磁盘：只应看到 catalog（可导航），private 不可见
	entries, err := svc.ListFiles(u.ID, disk.ID, "")
	if err != nil {
		t.Fatalf("进入磁盘失败: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "catalog" {
		t.Fatalf("磁盘下应只看到 catalog: %+v", entries)
	}

	// 进入 catalog：可见其中文件；只读不可写
	inside, err := svc.ListFiles(u.ID, entries[0].ID, "")
	if err != nil {
		t.Fatalf("进入被授权目录失败: %v", err)
	}
	if len(inside) != 1 || inside[0].Name != "可见.txt" {
		t.Fatalf("被授权目录内容不符: %+v", inside)
	}
	if _, err := svc.CreateDir(u.ID, "新建", entries[0].ID); err == nil {
		t.Fatal("只读用户不应能创建目录")
	}

	// 未授权目录直接以 ID 访问被拒（private 元数据由管理员浏览产生）
	master, err := userSvc.GetByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	adminEntries, _ := svc.ListFiles(master.ID, disk.ID, "")
	for _, e := range adminEntries {
		if e.Name == "private" {
			if _, err := svc.ListFiles(u.ID, e.ID, ""); err == nil {
				t.Fatal("未授权目录应不可访问")
			}
		}
	}
	_ = diskDir
}


// TestDenyReason：权限不足时返回具体原因（缺读/缺写），被屏蔽或不存在返回空串
func TestDenyReason(t *testing.T) {
	svc, userSvc, diskDir, catalogDir := newScopedStorage(t)
	u, err := userSvc.CreateUser("ro", "pass123", "user", []user.Permission{
		{Path: catalogDir, Read: true, Write: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 管理员浏览磁盘使 private 目录存在元数据
	master, _ := userSvc.GetByUsername("admin")
	disks, _ := svc.ListFiles(master.ID, 0, "")
	var privateID, catalogID uint
	entries, _ := svc.ListFiles(master.ID, disks[0].ID, "")
	for _, e := range entries {
		if e.Name == "private" {
			privateID = e.ID
		}
		if e.Name == "catalog" {
			catalogID = e.ID
		}
	}
	// 只读用户对 catalog：可读、DenyReason(写) 应给出写权限原因
	if svc.DenyReason(u.ID, catalogID, false) != "" {
		t.Fatal("只读用户应可读 catalog")
	}
	reason := svc.DenyReason(u.ID, catalogID, true)
	if reason == "" || !strings.Contains(reason, "写权限") {
		t.Fatalf("写拒绝原因不符: %q", reason)
	}
	// 未授权目录：读拒绝原因
	if privateID == 0 {
		t.Fatal("未找到 private 元数据")
	}
	reason = svc.DenyReason(u.ID, privateID, false)
	if reason == "" || !strings.Contains(reason, "读权限") {
		t.Fatalf("读拒绝原因不符: %q", reason)
	}
	// 不存在的 ID：空串（按 404 处理）
	if svc.DenyReason(u.ID, 99999, false) != "" {
		t.Fatal("不存在的 ID 应返回空串")
	}
	_ = diskDir
}
