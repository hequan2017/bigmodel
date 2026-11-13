// Package audit 提供关键操作留痕。
// 说明：本实现提供基础留痕能力，不宣称防篡改审计；
// 与业务变更强绑定的关键事件应由调用方在同一事务内写入。
package audit

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/api"
)

// Entry 是一条待记录的审计事件。
type Entry struct {
	ActorID      *int64
	Action       string
	ResourceType string
	ResourceID   string
	Result       string
	RequestID    string
}

// Record 写入审计事件。写入失败只记日志，不影响主流程；
// 不记录口令、完整令牌或模型密钥。
func Record(ctx context.Context, pool *pgxpool.Pool, e Entry) {
	if e.RequestID == "" {
		e.RequestID = api.RequestIDFromContext(ctx)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_events (actor_id, action, resource_type, resource_id, result, request_id)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		e.ActorID, e.Action, e.ResourceType, e.ResourceID, e.Result, e.RequestID,
	); err != nil {
		slog.WarnContext(ctx, "审计事件写入失败", "action", e.Action, "error", err)
	}
}
