package auth

import "context"

type userKey struct{}
type tokenKey struct{}

// WithUser 将当前用户写入 context。
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// UserFromContext 读取当前用户；未认证时为 nil。
func UserFromContext(ctx context.Context) *User {
	u, _ := ctx.Value(userKey{}).(*User)
	return u
}

// WithToken 将原始会话令牌写入 context（仅用于登出等本人操作）。
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey{}, token)
}

// TokenFromContext 读取原始会话令牌。
func TokenFromContext(ctx context.Context) string {
	t, _ := ctx.Value(tokenKey{}).(string)
	return t
}
