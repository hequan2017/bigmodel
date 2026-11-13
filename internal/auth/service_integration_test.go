package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/config"
	"github.com/hequan2017/bigmodel/internal/database"
)

// newTestService 连接本地数据库；不可用时跳过集成测试，不阻塞其余用例。
func newTestService(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()
	cfg, err := config.Load(nil)
	if err != nil {
		t.Skipf("配置不可用，跳过集成测试: %v", err)
	}
	pool, err := database.Connect(context.Background(), cfg)
	if err != nil {
		t.Skipf("数据库不可用，跳过集成测试: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Skipf("迁移执行失败，跳过集成测试: %v", err)
	}
	return NewService(pool, 5*time.Minute), pool
}

func TestLoginFlowIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	login := fmt.Sprintf("itest_%d", time.Now().UnixNano())
	hash, err := HashPassword("p@ssw0rd")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (login_name, password_hash, role) VALUES ($1, $2, 'teacher')`,
		login, hash,
	); err != nil {
		t.Fatalf("创建测试用户: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE login_name = $1`, login) })

	token, user, err := svc.Login(ctx, login, "p@ssw0rd")
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if token == "" || user.Role != "teacher" || user.LoginName != login {
		t.Fatalf("登录结果异常: token=%v user=%+v", token != "", user)
	}

	got, err := svc.Authenticate(ctx, token)
	if err != nil || got.ID != user.ID {
		t.Fatalf("会话校验失败: err=%v got=%+v", err, got)
	}

	if _, _, err := svc.Login(ctx, login, "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("错误口令应返回 ErrInvalidCredentials, got %v", err)
	}
	if _, _, err := svc.Login(ctx, "no_such_user", "x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("未知登录名应返回 ErrInvalidCredentials, got %v", err)
	}
	if _, err := svc.Authenticate(ctx, "not-a-real-token"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("无效令牌应返回 ErrUnauthorized, got %v", err)
	}

	// 停用用户：已有会话立即失效，且无法再登录。
	if _, err := pool.Exec(ctx, `UPDATE users SET enabled = FALSE WHERE login_name = $1`, login); err != nil {
		t.Fatalf("停用用户: %v", err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("停用用户会话应失效, got %v", err)
	}
	if _, _, err := svc.Login(ctx, login, "p@ssw0rd"); !errors.Is(err, ErrUserDisabled) {
		t.Errorf("停用用户登录应返回 ErrUserDisabled, got %v", err)
	}

	if err := svc.Logout(ctx, token); err != nil {
		t.Fatalf("登出失败: %v", err)
	}
	if _, err := svc.Authenticate(ctx, token); err == nil {
		t.Error("登出后会话应失效")
	}
}
