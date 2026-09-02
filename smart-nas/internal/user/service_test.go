package user

import (
	"testing"

	"smart-nas/internal/security"
)

func testArgon() security.Argon2Params {
	return security.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}
}

func newTestUserSvc(t *testing.T) *Service {
	t.Helper()
	repo, err := NewRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	return NewService(repo, testArgon())
}

func TestCanAccessNoPermissions(t *testing.T) {
	s := newTestUserSvc(t)
	u, err := s.CreateUser("tom", "tom123", RoleUser, nil)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// 未配置权限默认全量访问
	if !s.CanAccess(u.ID, "C:/anything/path", false) {
		t.Fatal("无权限配置应默认全量可读")
	}
	if !s.CanAccess(u.ID, "C:/anything/path", true) {
		t.Fatal("无权限配置应默认全量可写")
	}
}

func TestCanAccessPerms(t *testing.T) {
	s := newTestUserSvc(t)
	u, err := s.CreateUser("tom", "tom123", RoleUser, []Permission{
		{Path: "D:/photo", Read: true, Write: true},
		{Path: "E:/", Read: true, Write: false},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// 命中可读写目录
	if !s.CanAccess(u.ID, "D:/photo/a.jpg", true) {
		t.Fatal("D:/photo 子路径应可写")
	}
	// 只读目录：子路径可读不可写
	if !s.CanAccess(u.ID, "E:/video.mp4", false) {
		t.Fatal("E:/ 子路径应可读")
	}
	if s.CanAccess(u.ID, "E:/video.mp4", true) {
		t.Fatal("E:/ 只读目录不应可写")
	}
	// 写权限隐含读权限
	if !s.CanAccess(u.ID, "D:/photo", false) {
		t.Fatal("可写目录应隐含可读")
	}
	// 未匹配目录拒绝
	if s.CanAccess(u.ID, "F:/secret", false) {
		t.Fatal("未匹配权限的目录应拒绝")
	}
}

func TestCanAccessAdminFull(t *testing.T) {
	s := newTestUserSvc(t)
	// 未配置权限的管理员默认全量访问
	u, err := s.CreateUser("boss", "boss123", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if !s.CanAccess(u.ID, "Z:/anywhere", true) {
		t.Fatal("未配置权限的管理员应默认全量可写")
	}
	// 配置了权限的管理员同样受权限约束（v0.13 取消 admin 全量放行）
	rw, err := s.CreateUser("boss2", "boss123", RoleAdmin, []Permission{{Path: "D:/", Read: true, Write: false}})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if s.CanAccess(rw.ID, "Z:/anywhere", false) {
		t.Fatal("受限管理员不应访问未授权目录")
	}
	if s.CanAccess(rw.ID, "D:/x", true) {
		t.Fatal("只读管理员不应可写")
	}
}

func TestNormalizePermissionsDedup(t *testing.T) {
	got := normalizePermissions([]Permission{
		{Path: "S:/", Read: true, Write: true},
		{Path: "S:/photo", Read: true, Write: true},   // 已被 S:/ 完整覆盖 → 剔除
		{Path: "s:/photo/2024", Read: true, Write: true}, // 大小写不同同样剔除
		{Path: "D:/docs", Read: true, Write: false},
	})
	if len(got) != 2 {
		t.Fatalf("去重后应有 2 条, got %d: %+v", len(got), got)
	}
	for _, p := range got {
		if p.Path == "S:/photo" || p.Path == "s:/photo/2024" {
			t.Fatalf("冗余子目录条目未去重: %+v", got)
		}
	}
}

func TestCreateUserCannotBeMaster(t *testing.T) {
	s := newTestUserSvc(t)
	if _, err := s.CreateUser("fake", "pw", RoleMaster, nil); err == nil {
		t.Fatal("不允许通过接口创建主人账号")
	}
}

func TestCanManageUser(t *testing.T) {
	user1 := &User{Role: RoleUser}
	admin := &User{Role: RoleAdmin}
	master := &User{Role: RoleMaster}
	if !master.CanManageUser(admin) || !master.CanManageUser(user1) {
		t.Fatal("主人应可管理所有用户")
	}
	if !admin.CanManageUser(user1) {
		t.Fatal("管理员应可管理普通用户")
	}
	if admin.CanManageUser(admin) {
		t.Fatal("管理员不可管理其他管理员")
	}
	if user1.CanManageUser(user1) {
		t.Fatal("普通用户无管理权限")
	}
}