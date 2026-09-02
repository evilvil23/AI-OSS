// Package security 提供底层安全原语：Argon2id 密码哈希。
// 独立于 auth（认证服务）与 user（用户服务），避免循环依赖。
package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"

	"golang.org/x/crypto/argon2"
)

// Argon2Params Argon2id 参数（与 config.toml [auth] 对应）
type Argon2Params struct {
	Time    uint32
	Memory  uint32 // KB
	Threads uint8
	KeyLen  uint32
	SaltLen uint32
}

// DefaultArgon2Params OWASP 推荐参数
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{Time: 3, Memory: 65536, Threads: 4, KeyLen: 32, SaltLen: 16}
}

// HashPassword 使用 Argon2id 生成 (salt, hash) 的 base64 编码
func HashPassword(password string, p Argon2Params) (saltB64, hashB64 string, err error) {
	if p.SaltLen == 0 {
		p = DefaultArgon2Params()
	}
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", "", err
	}
	hash := argon2Hash(password, salt, p)
	return base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(hash), nil
}

// HashPasswordWithSalt 使用给定盐生成哈希
func HashPasswordWithSalt(password, saltB64 string, p Argon2Params) (string, error) {
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(argon2Hash(password, salt, p)), nil
}

// VerifyPassword 校验密码
func VerifyPassword(password, saltB64, hashB64 string, p Argon2Params) bool {
	expected, err := base64.StdEncoding.DecodeString(hashB64)
	if err != nil {
		return false
	}
	actual, err := HashPasswordWithSalt(password, saltB64, p)
	if err != nil {
		return false
	}
	actualBytes, err := base64.StdEncoding.DecodeString(actual)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(actualBytes, expected) == 1
}

// CheckPasswordHashFormat 校验存储的哈希编码格式
func CheckPasswordHashFormat(hashB64 string) error {
	if hashB64 == "" {
		return errors.New("password hash is empty")
	}
	if _, err := base64.StdEncoding.DecodeString(hashB64); err != nil {
		return errors.New("invalid password hash encoding")
	}
	return nil
}

// argon2Hash 计算 Argon2id 哈希（保证默认参数生效）
func argon2Hash(password string, salt []byte, p Argon2Params) []byte {
	if p.Time == 0 || p.Memory == 0 || p.Threads == 0 || p.KeyLen == 0 {
		p = DefaultArgon2Params()
	}
	return argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
}