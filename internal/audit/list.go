package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EventView 是审计事件的展示结构（不含敏感信息：无口令、无完整令牌、无密钥）。
type EventView struct {
	EventID      int64     `json:"event_id"`
	ActorID      *int64    `json:"actor_id"`
	ActorName    string    `json:"actor_name"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Result       string    `json:"result"`
	RequestID    string    `json:"request_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// ListEvents 按时间倒序返回最近 limit 条审计事件（管理员查看用）。
// 本实现提供基础留痕的查看能力，不宣称防篡改审计。
func ListEvents(ctx context.Context, pool *pgxpool.Pool, limit int) ([]EventView, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := pool.Query(ctx, `
		SELECT a.event_id, a.actor_id, COALESCE(u.login_name, ''),
		       a.action, a.resource_type, a.resource_id, a.result, a.request_id, a.created_at
		FROM audit_events a
		LEFT JOIN users u ON u.id = a.actor_id
		ORDER BY a.event_id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询审计事件: %w", err)
	}
	defer rows.Close()

	out := []EventView{}
	for rows.Next() {
		var e EventView
		if err := rows.Scan(&e.EventID, &e.ActorID, &e.ActorName, &e.Action,
			&e.ResourceType, &e.ResourceID, &e.Result, &e.RequestID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("读取审计事件: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
