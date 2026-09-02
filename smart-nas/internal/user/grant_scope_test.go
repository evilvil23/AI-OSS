package user

import (
	"strings"
	"testing"
)

// TestAdminSubjectToPermissions：配置了权限的管理员同样受权限约束（v0.13 取消 admin 全量放行）
func TestAdminSubjectToPermissions(t *testing.T) {
	s := newTestUserSvc(t)
	admin, err := s.CreateUser("boss", "boss123", RoleAdmin, []Permission{
		{Path: `Y:\catalog`, Read: true, Write: false},
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if s.CanAccess(admin.ID, `Y:\catalog`, true) {
		t.Fatal("只读管理员不应有写权限")
	}
	if !s.CanAccess(admin.ID, `Y:\catalog`, false) {
		t.Fatal("管理员应有被授权目录的读权限")
	}
	if s.CanAccess(admin.ID, `Z:\other`, false) {
		t.Fatal("管理员不应访问未授权目录")
	}
	// 未配置权限的管理员仍默认全量（向后兼容）
	full, _ := s.CreateUser("boss2", "boss123", RoleAdmin, nil)
	if !s.CanAccess(full.ID, `Z:\anywhere`, true) {
		t.Fatal("未配置权限的管理员应默认全量")
	}
}

// TestCanGrant：非主人只能授予自身权限范围内的目录，且不超自身读写级别
func TestCanGrant(t *testing.T) {
	s := newTestUserSvc(t)
	master := &User{ID: 0, Role: RoleMaster}
	readOnlyAdmin, err := s.CreateUser("roa", "pass123", RoleAdmin, []Permission{
		{Path: `Y:\catalog`, Read: true, Write: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 主人不限
	if err := s.CanGrant(master, []Permission{{Path: `Z:\any`, Read: true, Write: true}}); err != nil {
		t.Fatalf("主人应可授予任意权限: %v", err)
	}
	// 只读管理员：可授自身目录的读
	if err := s.CanGrant(readOnlyAdmin, []Permission{{Path: `Y:\catalog`, Read: true}}); err != nil {
		t.Fatalf("只读管理员应可授只读: %v", err)
	}
	// 不可授写（超出自身级别）
	if err := s.CanGrant(readOnlyAdmin, []Permission{{Path: `Y:\catalog`, Read: true, Write: true}}); err == nil {
		t.Fatal("只读管理员不可授予写权限")
	}
	// 不可授自身范围外目录
	if err := s.CanGrant(readOnlyAdmin, []Permission{{Path: `Z:\other`, Read: true}}); err == nil {
		t.Fatal("不可授予范围外目录")
	}
	// 自身目录的子目录可授（读）
	if err := s.CanGrant(readOnlyAdmin, []Permission{{Path: `Y:\catalog\sub`, Read: true}}); err != nil {
		t.Fatalf("子目录只读应可授予: %v", err)
	}
	// 未配置权限的管理员（默认全量）可授任意
	fullAdmin, _ := s.CreateUser("fa", "pass123", RoleAdmin, nil)
	if err := s.CanGrant(fullAdmin, []Permission{{Path: `Z:\any`, Write: true}}); err != nil {
		t.Fatalf("全量管理员应可授予: %v", err)
	}
}

// TestHasScopeUnder：受限用户在其被授权路径的祖先目录上具有“范围”
func TestHasScopeUnder(t *testing.T) {
	s := newTestUserSvc(t)
	u, err := s.CreateUser("tom", "tom123", RoleUser, []Permission{
		{Path: `Y:\catalog`, Read: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasScopeUnder(u.ID, `Y:\`) {
		t.Fatal("Y: 盘应因包含被授权子目录而可见")
	}
	if !s.HasScopeUnder(u.ID, `Y:\catalog`) {
		t.Fatal("被授权目录本身应有范围")
	}
	if s.HasScopeUnder(u.ID, `Z:\`) {
		t.Fatal("无授权的磁盘不应有范围")
	}
	// 未配置权限 / 主人：任意目录均有范围
	full, _ := s.CreateUser("full", "pass123", RoleUser, nil)
	if !s.HasScopeUnder(full.ID, `Z:\`) {
		t.Fatal("全量用户任意目录应有范围")
	}
}

// TestPermissionPathValidation：权限路径校验与归一化（正斜杠→反斜杠、非法路径拒绝）
func TestPermissionPathValidation(t *testing.T) {
	s := newTestUserSvc(t)
	u, err := s.CreateUser("tom", "tom123", RoleUser, []Permission{
		{Path: "Y:/catalog/nested", Read: true},
	})
	if err != nil {
		t.Fatalf("正斜杠路径应归一化接受: %v", err)
	}
	if len(u.Permissions) != 1 || u.Permissions[0].Path != `Y:\catalog\nested` {
		t.Fatalf("路径应归一化为反斜杠: %+v", u.Permissions)
	}
	if _, err := s.CreateUser("bad", "pass123", RoleUser, []Permission{
		{Path: "relative/path", Read: true},
	}); err == nil || !strings.Contains(err.Error(), "不合法") {
		t.Fatalf("相对路径应被拒绝: %v", err)
	}
	if _, err := s.CreateUser("bad2", "pass123", RoleUser, []Permission{
		{Path: `Y:\bad<name`, Read: true},
	}); err == nil {
		t.Fatal("含非法字符的路径应被拒绝")
	}
}
