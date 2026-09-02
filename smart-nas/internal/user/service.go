package user

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"smart-nas/internal/security"
	"smart-nas/internal/util"
	"smart-nas/pkg/logger"
)

// Service 用户业务逻辑
type Service struct {
	repo    *Repository
	argon2p security.Argon2Params
}

// NewService 创建用户服务
func NewService(repo *Repository, p security.Argon2Params) *Service {
	return &Service{repo: repo, argon2p: p}
}

// CreateUser 创建用户（role 仅允许 master/admin/user；主人角色仅由配置文件创建）
func (s *Service) CreateUser(username, password, role string, permissions []Permission) (*User, error) {
	if username == "" || password == "" {
		return nil, errors.New("用户名和密码不能为空")
	}
	if s.repo.Exists(username) {
		return nil, errors.New("用户名已存在")
	}
	if role == "" {
		role = RoleUser
	}
	if !isValidRole(role) {
		return nil, errors.New("无效的角色")
	}
	if role == RoleMaster {
		return nil, errors.New("主人账号不可新增，请通过配置文件管理")
	}
	perms, err := validatePermissionPaths(permissions)
	if err != nil {
		return nil, err
	}
	salt, hash, err := security.HashPassword(password, s.argon2p)
	if err != nil {
		return nil, err
	}
	u := &User{
		Username:     username,
		PasswordHash: hash,
		PasswordSalt: salt,
		Role:         role,
		Permissions:  normalizePermissions(perms),
	}
	if err := s.repo.Create(u); err != nil {
		return nil, err
	}
	return u, nil
}

func isValidRole(role string) bool {
	return role == RoleMaster || role == RoleAdmin || role == RoleUser
}

// GetByUsername 根据用户名获取用户
func (s *Service) GetByUsername(username string) (*User, error) {
	return s.repo.GetByUsername(username)
}

// GetByID 根据 ID 获取用户
func (s *Service) GetByID(id uint) (*User, error) {
	return s.repo.GetByID(id)
}

// List 用户列表
func (s *Service) List() ([]*User, error) { return s.repo.List() }

// UpdateUser 更新用户资料（role / permissions / password）。
// 主人账号：角色与密码不可通过接口修改（仅配置文件）。
func (s *Service) UpdateUser(id uint, role *string, permissions []Permission, newPassword *string) (*User, error) {
	u, err := s.repo.GetByID(id)
	if err != nil {
		return nil, err
	}
	if u.IsMaster() {
		if (role != nil && *role != "") || (newPassword != nil && *newPassword != "") {
			return nil, errors.New("主人账号的角色与密码仅可通过配置文件修改")
		}
		if permissions != nil {
			perms, err := validatePermissionPaths(permissions)
			if err != nil {
				return nil, err
			}
			u.Permissions = normalizePermissions(perms)
		}
		if err := s.repo.Update(u); err != nil {
			return nil, err
		}
		return u, nil
	}
	if role != nil && *role != "" {
		if !isValidRole(*role) {
			return nil, errors.New("无效的角色")
		}
		if *role == RoleMaster {
			return nil, errors.New("主人账号不可被提升/创建")
		}
		u.Role = *role
	}
	if permissions != nil {
		perms, err := validatePermissionPaths(permissions)
		if err != nil {
			return nil, err
		}
		u.Permissions = normalizePermissions(perms)
	}
	if newPassword != nil && *newPassword != "" {
		salt, hash, err := security.HashPassword(*newPassword, s.argon2p)
		if err != nil {
			return nil, err
		}
		u.PasswordSalt = salt
		u.PasswordHash = hash
	}
	if err := s.repo.Update(u); err != nil {
		return nil, err
	}
	return u, nil
}

// SetPermissions 设置用户目录权限（路径经校验与归一化）
func (s *Service) SetPermissions(id uint, permissions []Permission) (*User, error) {
	u, err := s.repo.GetByID(id)
	if err != nil {
		return nil, err
	}
	perms, err := validatePermissionPaths(permissions)
	if err != nil {
		return nil, err
	}
	u.Permissions = normalizePermissions(perms)
	if err := s.repo.Update(u); err != nil {
		return nil, err
	}
	return u, nil
}

// DeleteUser 删除用户（主人不可删除）
func (s *Service) DeleteUser(id uint) error {
	u, err := s.repo.GetByID(id)
	if err != nil {
		return err
	}
	if u.IsMaster() {
		return errors.New("主人账号不可删除")
	}
	if u.Role == RoleAdmin && s.repo.CountAdmins() <= 1 {
		return errors.New("至少需要保留一个管理员账号")
	}
	return s.repo.Delete(id)
}

// ChangePassword 用户修改自己的密码（主人仅可通过配置文件修改）
func (s *Service) ChangePassword(id uint, oldPassword, newPassword string) error {
	u, err := s.repo.GetByID(id)
	if err != nil {
		return err
	}
	if u.IsMaster() {
		return errors.New("主人密码仅可通过配置文件修改")
	}
	if !security.VerifyPassword(oldPassword, u.PasswordSalt, u.PasswordHash, s.argon2p) {
		return errors.New("原密码错误")
	}
	salt, hash, err := security.HashPassword(newPassword, s.argon2p)
	if err != nil {
		return err
	}
	u.PasswordSalt = salt
	u.PasswordHash = hash
	return s.repo.Update(u)
}

// CanAccess 判断用户对某路径是否具有读/写权限。
// 规则（v0.13）：主人（master）全量放行；未配置任何权限的用户（含管理员）
// 默认全量放行；配置了权限后（含管理员）仅匹配的路径可访问，写权限隐含读权限。
// 此前管理员无视权限配置全量放行，与「管理员只能授予自身权限范围内目录」的要求不符，已取消该特权。
func (s *Service) CanAccess(userID uint, path string, write bool) bool {
	u, err := s.repo.GetByID(userID)
	if err != nil {
		return false
	}
	if u.Role == RoleMaster {
		return true
	}
	if len(u.Permissions) == 0 {
		return true
	}
	for _, p := range u.Permissions {
		if pathMatches(path, p.Path) {
			if write {
				return p.Write
			}
			return p.Read || p.Write
		}
	}
	return false
}

// HasScopeUnder 判断用户在 dir 目录内是否拥有任意权限范围（含完全等于 dir）。
// 用于受限用户的可见性：磁盘/目录只要包含用户可访问的子路径，即展示出来供导航进入。
func (s *Service) HasScopeUnder(userID uint, dir string) bool {
	u, err := s.repo.GetByID(userID)
	if err != nil {
		return false
	}
	if u.Role == RoleMaster || len(u.Permissions) == 0 {
		return true
	}
	for _, p := range u.Permissions {
		if pathMatches(p.Path, dir) {
			return true
		}
	}
	return false
}

// CanGrant 校验操作者能否授予指定权限集合（v0.13）：
// 主人不限；其余角色（含管理员）只能授予自身具备权限的目录，
// 且授予的读/写不能超过自身在该目录的权限（只读管理员只能授只读）。
func (s *Service) CanGrant(operator *User, perms []Permission) error {
	if operator == nil {
		return errors.New("无效的操作者")
	}
	if operator.Role == RoleMaster {
		return nil
	}
	for _, p := range perms {
		if strings.TrimSpace(p.Path) == "" {
			continue
		}
		if !s.CanAccess(operator.ID, p.Path, p.Write) {
			return fmt.Errorf("无权授予超出自身权限范围的目录：%s", p.Path)
		}
	}
	return nil
}

// pathMatches 判断 target 是否等于 prefix 或位于 prefix 之下（Windows 大小写不敏感）
func pathMatches(target, prefix string) bool {
	t := filepath.Clean(target)
	p := filepath.Clean(prefix)
	if strings.EqualFold(t, p) {
		return true
	}
	rel, err := filepath.Rel(p, t)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// normalizePermissions 清理并去重权限列表：
// - 去空路径；去重复项
// - 若已具备某路径的完整权限（读或写到位），则其下任何子路径条目均为冗余，直接剔除
func normalizePermissions(perms []Permission) []Permission {
	cleaned := make([]Permission, 0, len(perms))
	for _, p := range perms {
		p.Path = strings.TrimSpace(p.Path)
		if p.Path == "" {
			continue
		}
		cleaned = append(cleaned, p)
	}
	var out []Permission
	for i, a := range cleaned {
		covered := false
		for j, b := range cleaned {
			if i == j {
				continue
			}
			if permCovers(b, a) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, a)
		}
	}
	return out
}

// validatePermissionPaths 校验并归一化权限路径（v0.13）：
// 正/反斜杠统一为反斜杠；必须为盘符开头的合法 Windows 绝对路径。
func validatePermissionPaths(perms []Permission) ([]Permission, error) {
	out := make([]Permission, 0, len(perms))
	for _, p := range perms {
		p.Path = util.NormalizeWinPath(p.Path)
		if p.Path == "" {
			continue
		}
		if !util.IsValidAbsPath(p.Path) {
			return nil, fmt.Errorf("目录路径不合法（应形如 Y:\\catalog）：%s", p.Path)
		}
		out = append(out, p)
	}
	return out, nil
}
func permCovers(rule, target Permission) bool {
	if !pathMatches(target.Path, rule.Path) {
		return false
	}
	// 同路径：权限更高的保留，权限相同时去重
	if strings.EqualFold(filepath.Clean(rule.Path), filepath.Clean(target.Path)) {
		if rule.Read == target.Read && rule.Write == target.Write {
			return true
		}
	}
	return (target.Read == false || rule.Read) && (target.Write == false || rule.Write)
}

// ensureMasterImpl 确保配置中的默认用户存在且为主人角色，密码以配置文件为准
// （每次启动按配置重置主人密码，实现"仅通过配置文件修改"）。
func (s *Service) ensureMasterImpl(username, password string) error {
	if username == "" {
		username = "admin"
	}
	if password == "" {
		password = "admin123"
	}
	u, err := s.repo.GetByUsername(username)
	if err != nil {
		// 不存在：若系统无任何用户则直接创建；否则仍创建为默认主人账号
		nu, cerr := s.createMaster(username, password)
		if cerr != nil {
			return cerr
		}
		u = nu
	} else {
		// 存在：重置为 master 角色并按配置更新密码
		salt, hash, herr := security.HashPassword(password, s.argon2p)
		if herr != nil {
			return herr
		}
		u.Role = RoleMaster
		u.PasswordSalt = salt
		u.PasswordHash = hash
		if err := s.repo.Update(u); err != nil {
			return err
		}
	}
	logger.Warn("默认主人账号已就绪",
		"username", username, "role", RoleMaster, "id", u.ID,
		"提示", "主人密码仅可通过 config.toml 的 [auth] 段修改")
	return nil
}

// createMaster 直接以主人角色创建用户（绕过 CreateUser 的角色限制）
func (s *Service) createMaster(username, password string) (*User, error) {
	salt, hash, err := security.HashPassword(password, s.argon2p)
	if err != nil {
		return nil, err
	}
	u := &User{
		Username:     username,
		PasswordHash: hash,
		PasswordSalt: salt,
		Role:         RoleMaster,
	}
	if err := s.repo.Create(u); err != nil {
		return nil, err
	}
	return u, nil
}

// EnsureMaster 系统启动时确保配置中的默认用户为主人角色
func (s *Service) EnsureMaster(username, password string) error {
	return s.ensureMasterImpl(username, password)
}
