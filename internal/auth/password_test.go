package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("s3cret!")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	if hash == "s3cret!" || len(hash) < 50 {
		t.Errorf("哈希结果异常: %q", hash)
	}
	if !VerifyPassword(hash, "s3cret!") {
		t.Error("正确口令应通过校验")
	}
	if VerifyPassword(hash, "wrong") {
		t.Error("错误口令不应通过校验")
	}
}
