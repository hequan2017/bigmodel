// Package auth 提供登录认证、会话管理与角色权限。
// 会话令牌为不透明随机串，服务端只保存其 SHA-256 哈希。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/audit"
)

// 角色常量：本期固定三种角色，不做可配置权限编辑器。
const (
	RoleAdmin   = "admin"
	RoleTeacher = "teacher"
	RoleStudent = "student"
)

// 服务级错误，由 handler 层映射为稳定错误码。
var (
	ErrInvalidCredentials = errors.New("登录名或密码不正确")
	ErrUserDisabled       = errors.New("账号已停用")
	ErrUnauthorized       = errors.New("未登录或会话已失效")
	ErrTooManyAttempts    = errors.New("失败次数过多，账号已临时锁定")
)

// Authenticator 校验会话令牌并返回当前用户，供权限中间件依赖。
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*User, error)
}

// User 是认证相关的用户信息。
type User struct {
	ID           int64     `json:"id"`
	LoginName    string    `json:"login_name"`
	Role         string    `json:"role"`
	Enabled      bool      `json:"-"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// dummyHash 用于未知登录名的比较，避免响应时间差异泄漏账号存在性。
var dummyHash = func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), bcrypt.MinCost)
	return h
}()

// Service 基于 PostgreSQL 的认证服务。
type Service struct {
	pool    *pgxpool.Pool
	ttl     time.Duration
	limiter *loginLimiter
}

// NewService 构造认证服务；ttl 为会话有效期。
func NewService(pool *pgxpool.Pool, ttl time.Duration) *Service {
	return &Service{pool: pool, ttl: ttl, limiter: newLoginLimiter()}
}

// SetLoginLockDuration 设置连续失败后的锁定时长（测试可注入更短时长）。
func (s *Service) SetLoginLockDuration(d time.Duration) {
	s.limiter.lockDuration = d
}

// Login 校验登录名与密码，成功时创建会话并返回令牌。
func (s *Service) Login(ctx context.Context, loginName, password string) (string, *User, error) {
	if locked, remain := s.limiter.lockedRemaining(loginName); locked {
		s.recordLoginAudit(ctx, nil, "locked", loginName)
		return "", nil, fmt.Errorf("%w: 请约 %d 秒后重试", ErrTooManyAttempts, int(remain.Seconds())+1)
	}

	user, err := s.findUserByLogin(ctx, loginName)
	if err != nil {
		return "", nil, err
	}

	if user == nil {
		// 未知登录名也执行一次比较，避免响应时间差异泄漏账号存在性。
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		s.limiter.fail(loginName)
		s.recordLoginAudit(ctx, nil, "failed", loginName)
		return "", nil, ErrInvalidCredentials
	}
	if !user.Enabled {
		s.recordLoginAudit(ctx, &user.ID, "failed", loginName)
		return "", nil, ErrUserDisabled
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		s.limiter.fail(loginName)
		s.recordLoginAudit(ctx, &user.ID, "failed", loginName)
		return "", nil, ErrInvalidCredentials
	}
	s.limiter.reset(loginName)

	token, err := newToken()
	if err != nil {
		return "", nil, err
	}
	// 建会话的同时顺手清理过期会话；单实例演示规模下足够。
	if _, err := s.pool.Exec(ctx, `
		WITH expired AS (DELETE FROM user_sessions WHERE expires_at <= now())
		INSERT INTO user_sessions (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)`,
		user.ID, hashToken(token), time.Now().Add(s.ttl),
	); err != nil {
		return "", nil, fmt.Errorf("创建会话: %w", err)
	}

	s.recordLoginAudit(ctx, &user.ID, "success", loginName)
	return token, user, nil
}

// Logout 删除会话；重复登出幂等。
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM user_sessions WHERE token_hash = $1`, hashToken(token)); err != nil {
		return fmt.Errorf("删除会话: %w", err)
	}
	return nil
}

// Authenticate 校验令牌；无效或过期返回 ErrUnauthorized。
func (s *Service) Authenticate(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrUnauthorized
	}
	user, err := s.findSessionUser(ctx, hashToken(token))
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrUnauthorized
	}
	return user, nil
}

func (s *Service) findUserByLogin(ctx context.Context, loginName string) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT id, login_name, password_hash, role, enabled, created_at
		FROM users WHERE login_name = $1`, loginName,
	).Scan(&u.ID, &u.LoginName, &u.PasswordHash, &u.Role, &u.Enabled, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户: %w", err)
	}
	return &u, nil
}

func (s *Service) findSessionUser(ctx context.Context, tokenHash string) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.login_name, u.role, u.enabled, u.created_at
		FROM user_sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now() AND u.enabled`, tokenHash,
	).Scan(&u.ID, &u.LoginName, &u.Role, &u.Enabled, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询会话: %w", err)
	}
	return &u, nil
}

func (s *Service) recordLoginAudit(ctx context.Context, actorID *int64, result, loginName string) {
	audit.Record(ctx, s.pool, audit.Entry{
		ActorID:      actorID,
		Action:       "auth.login",
		ResourceType: "user",
		ResourceID:   loginName,
		Result:       result,
	})
}

func newToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("生成会话令牌: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
