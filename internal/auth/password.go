package auth

import (
	"golang.org/x/crypto/bcrypt"
)

// HashPassword 使用 bcrypt 生成口令哈希。
func HashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword 校验明文口令与 bcrypt 哈希。
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
