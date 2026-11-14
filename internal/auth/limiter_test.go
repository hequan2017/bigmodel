package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestLoginLimiterUnit(t *testing.T) {
	l := newLoginLimiter()
	l.lockDuration = 50 * time.Millisecond

	if locked, _ := l.lockedRemaining("u"); locked {
		t.Fatal("初始不应锁定")
	}
	for i := 0; i < 4; i++ {
		l.fail("u")
	}
	if locked, _ := l.lockedRemaining("u"); locked {
		t.Fatal("未达阈值不应锁定")
	}
	l.fail("u") // 第 5 次 → 锁定
	if locked, remain := l.lockedRemaining("u"); !locked || remain <= 0 {
		t.Fatal("达到阈值应锁定")
	}
	// 锁定期间 fail 不延长锁定
	time.Sleep(10 * time.Millisecond)
	l.fail("u")
	if _, remain := l.lockedRemaining("u"); remain > 45*time.Millisecond {
		t.Errorf("锁定期间失败不应延长锁定: %v", remain)
	}
	// reset 清零
	l.reset("u")
	if locked, _ := l.lockedRemaining("u"); locked {
		t.Fatal("reset 后不应锁定")
	}
}

func TestLoginRateLimitIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()
	svc.SetLoginLockDuration(150 * time.Millisecond)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	login := fmt.Sprintf("itest_rl_%s", suffix)
	hash, err := HashPassword("correct-pass")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (login_name, password_hash, role) VALUES ($1, $2, 'student')`,
		login, hash); err != nil {
		t.Fatalf("创建用户: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE login_name = $1`, login)
	})

	// 前 5 次错误：ErrInvalidCredentials（第 5 次失败触发锁定）。
	for i := 0; i < 5; i++ {
		if _, _, err := svc.Login(ctx, login, "wrong"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("第 %d 次错误口令应 ErrInvalidCredentials, got %v", i+1, err)
		}
	}

	// 锁定期间：正确口令也返回 ErrTooManyAttempts。
	if _, _, err := svc.Login(ctx, login, "correct-pass"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("锁定期间正确口令应 ErrTooManyAttempts, got %v", err)
	}

	// 锁定过期后：正确口令可登录，且成功清零计数。
	time.Sleep(200 * time.Millisecond)
	if _, _, err := svc.Login(ctx, login, "correct-pass"); err != nil {
		t.Fatalf("锁定过期后应可登录: %v", err)
	}

	// 成功清零计数：再 4 次错误后第 5 次正确仍可登录（计数从零开始）。
	for i := 0; i < 4; i++ {
		_, _, _ = svc.Login(ctx, login, "wrong")
	}
	if _, _, err := svc.Login(ctx, login, "correct-pass"); err != nil {
		t.Fatalf("成功后计数清零应生效: %v", err)
	}
}
