package auth

import (
	"errors"

	"smart-nas/internal/config"
	"smart-nas/internal/security"
	"smart-nas/internal/user"
)

// Service 认证服务：登录、登出、Token 签发校验
type Service struct {
	users *user.Service
	jwt   *JWT
	argon security.Argon2Params
}

// NewService 创建认证服务
func NewService(users *user.Service, cfg config.AuthConfig) *Service {
	return &Service{
		users: users,
		jwt:   NewJWT(cfg.JWTSecret, cfg.JWTExpire),
		argon: security.Argon2Params{
			Time:    cfg.Argon2Time,
			Memory:  cfg.Argon2Memory,
			Threads: cfg.Argon2Threads,
			KeyLen:  cfg.Argon2KeyLen,
			SaltLen: cfg.Argon2SaltLen,
		},
	}
}

// LoginResult 登录结果
type LoginResult struct {
	Token    string                 `json:"token"`
	ExpiresIn int64                 `json:"expires_in"`
	User     map[string]interface{} `json:"user"`
}

// Login 用户名密码登录
func (s *Service) Login(username, password string) (*LoginResult, error) {
	u, err := s.users.GetByUsername(username)
	if err != nil {
		return nil, errors.New("用户名或密码错误")
	}
	if !VerifyPassword(password, u.PasswordSalt, u.PasswordHash, s.argon) {
		return nil, errors.New("用户名或密码错误")
	}
	token, err := s.jwt.Sign(Claims{
		UserID:   u.ID,
		Username: u.Username,
		Role:     u.Role,
	})
	if err != nil {
		return nil, err
	}
	return &LoginResult{Token: token, ExpiresIn: int64(s.jwt.expire.Seconds()), User: u.Public()}, nil
}

// ValidateToken 校验 token 并返回用户
func (s *Service) ValidateToken(token string) (*user.User, *Claims, error) {
	claims, err := s.jwt.Parse(token)
	if err != nil {
		return nil, nil, err
	}
	u, err := s.users.GetByID(claims.UserID)
	if err != nil {
		return nil, nil, errors.New("用户不存在")
	}
	return u, claims, nil
}

// ChangePassword 修改密码
func (s *Service) ChangePassword(userID uint, oldPwd, newPwd string) error {
	return s.users.ChangePassword(userID, oldPwd, newPwd)
}

// CheckPassword 校验用户名密码（供 WebDAV Basic Auth 使用），返回用户 ID
func (s *Service) CheckPassword(username, password string) (uint, error) {
	u, err := s.users.GetByUsername(username)
	if err != nil {
		return 0, errors.New("用户名或密码错误")
	}
	if !VerifyPassword(password, u.PasswordSalt, u.PasswordHash, s.argon) {
		return 0, errors.New("用户名或密码错误")
	}
	return u.ID, nil
}

// CanAccessPath 校验用户对某路径的读/写权限（供 WebDAV 等直接文件访问使用）
func (s *Service) CanAccessPath(userID uint, path string, write bool) bool {
	return s.users.CanAccess(userID, path, write)
}