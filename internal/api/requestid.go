// Package api 定义对外 HTTP 响应的统一结构与请求追踪 ID。
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type contextKey struct{}

// NewRequestID 生成 32 字符的请求追踪 ID。
func NewRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand 不可用时退化为纳秒时间戳，保证日志仍可关联。
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// WithRequestID 将请求 ID 写入 context。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// RequestIDFromContext 读取请求 ID；不存在时返回空串。
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
