package util

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"uuid" // Go 1.27 标准库（RFC 9562），替代 github.com/google/uuid
)

// NewUUID 生成标准 UUID 字符串（v4 随机，含短横线；无短横线版本用 NewUUIDCompact）
func NewUUID() string {
	return uuid.NewV4().String()
}

// NewUUIDCompact 生成无短横线的 32 字符 UUID（可作为文件名后缀等）
func NewUUIDCompact() string {
	return strings.ReplaceAll(uuid.NewV4().String(), "-", "")
}

// RandomHex 生成随机十六进制字符串（长度为 2*n 字符）
func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// 兼容 ID 生成：随机数字 ID（演示用），实际项目中可用雪花算法
func RandomNumericID() uint64 {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return 100000 + uint64(b[0])<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3])
}
