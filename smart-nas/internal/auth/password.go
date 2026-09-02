// Package auth 提供用户认证：JWT 签发/校验 + 认证服务。
// 密码哈希（Argon2id）收拢在 internal/security 以避免循环依赖，
// 此处为文档约定的 API 位置，转发到 security。
package auth

import "smart-nas/internal/security"

// Argon2Params Argon2id 参数
type Argon2Params = security.Argon2Params

// DefaultArgon2Params OWASP 推荐参数
func DefaultArgon2Params() Argon2Params { return security.DefaultArgon2Params() }

// HashPassword 使用 Argon2id 生成 (salt, hash) 的 base64 编码
func HashPassword(password string, p Argon2Params) (string, string, error) {
	return security.HashPassword(password, p)
}

// HashPasswordWithSalt 使用给定盐生成哈希
func HashPasswordWithSalt(password, saltB64 string, p Argon2Params) (string, error) {
	return security.HashPasswordWithSalt(password, saltB64, p)
}

// VerifyPassword 校验密码
func VerifyPassword(password, saltB64, hashB64 string, p Argon2Params) bool {
	return security.VerifyPassword(password, saltB64, hashB64, p)
}

// CheckPasswordHashFormat 校验哈希格式
func CheckPasswordHashFormat(hashB64 string) error {
	return security.CheckPasswordHashFormat(hashB64)
}