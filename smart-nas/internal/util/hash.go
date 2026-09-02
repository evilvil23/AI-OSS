package util

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// MD5Bytes 计算字节切片 MD5
func MD5Bytes(data []byte) string {
	h := md5.Sum(data)
	return hex.EncodeToString(h[:])
}

// MD5File 计算文件 MD5（流式计算，适合大文件）
func MD5File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// RandomToken 生成随机十六进制 token（用于共享链接等）
func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}