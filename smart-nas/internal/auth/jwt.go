package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Claims JWT 载荷
type Claims struct {
	UserID   uint   `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Exp      int64  `json:"exp"`
	Iat      int64  `json:"iat"`
	JTI      string `json:"jti,omitempty"`
}

// JWT 签发/校验器（HS256 手动实现）
type JWT struct {
	secret []byte
	expire time.Duration
}

// NewJWT 创建 JWT 管理器
func NewJWT(secret string, expireSeconds int) *JWT {
	if expireSeconds <= 0 {
		expireSeconds = 86400
	}
	return &JWT{secret: []byte(secret), expire: time.Duration(expireSeconds) * time.Second}
}

func b64e(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func b64d(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	}
	return b, nil
}

// Sign 生成 token
func (j *JWT) Sign(claims Claims) (string, error) {
	now := time.Now()
	claims.Iat = now.Unix()
	claims.Exp = now.Add(j.expire).Unix()
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	header := []byte(`{"alg":"HS256","typ":"JWT"}`)
	signingInput := b64e(header) + "." + b64e(payload)
	mac := hmac.New(sha256.New, j.secret)
	mac.Write([]byte(signingInput))
	sig := b64e(mac.Sum(nil))
	return signingInput + "." + sig, nil
}

// Parse 校验 token 并返回 Claims
func (j *JWT) Parse(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("无效的 token 格式")
	}
	// 校验签名
	mac := hmac.New(sha256.New, j.secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expected := mac.Sum(nil)
	sig, err := b64d(parts[2])
	if err != nil {
		return nil, errors.New("无效的签名编码")
	}
	if !hmac.Equal(expected, sig) {
		return nil, errors.New("签名不匹配")
	}
	payload, err := b64d(parts[1])
	if err != nil {
		return nil, err
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
		return nil, errors.New("token 已过期")
	}
	return &claims, nil
}