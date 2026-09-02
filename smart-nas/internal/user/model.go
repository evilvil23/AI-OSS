package user

import "time"

// Permission 目录访问权限（读/写）
type Permission struct {
	Path  string `json:"path"`  // 目录路径（磁盘根或子目录）
	Read  bool   `json:"read"`  // 读权限
	Write bool   `json:"write"` // 写权限
}

// 角色常量
const (
	RoleMaster = "master" // 主人：默认用户，仅可通过配置文件管理，可管理所有用户
	RoleAdmin  = "admin"  // 管理员：可管理普通用户
	RoleUser   = "user"   // 普通用户
)

// User 用户模型
type User struct {
	ID           uint         `json:"id"`
	Username     string       `json:"username"`
	PasswordHash string       `json:"password_hash"` // Argon2id 哈希（可安全持久化）
	PasswordSalt string       `json:"password_salt"`
	Role         string       `json:"role"` // master / admin / user
	Permissions  []Permission `json:"permissions"`
	Avatar       string       `json:"avatar"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// IsMaster 是否主人
func (u *User) IsMaster() bool { return u != nil && u.Role == RoleMaster }

// IsAdmin 是否管理员（含主人）
func (u *User) IsAdmin() bool { return u != nil && (u.Role == RoleMaster || u.Role == RoleAdmin) }

// CanManageUser 判断 operator 是否有权管理 target：
// 主人可管理所有用户；管理员只能管理普通用户；普通用户无管理权限。
func (u *User) CanManageUser(target *User) bool {
	if u == nil || target == nil {
		return false
	}
	if u.IsMaster() {
		return true
	}
	if u.Role == RoleAdmin {
		return target.Role == RoleUser
	}
	return false
}

// Public 对外暴露的脱敏视图
func (u *User) Public() map[string]interface{} {
	return map[string]interface{}{
		"id":          u.ID,
		"username":    u.Username,
		"role":        u.Role,
		"permissions": u.Permissions,
		"avatar":      u.Avatar,
		"created_at":  u.CreatedAt,
		"updated_at":  u.UpdatedAt,
	}
}
