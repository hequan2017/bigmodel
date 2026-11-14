package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/migrations"
)

// Migrate 执行所有未应用的迁移；每个迁移独立事务，避免留下半成品 Schema。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// schema_migrations 使用 IF NOT EXISTS：业务表由版本记录保证只建一次，
	// 而此表本身需要在重复启动（含测试 schema 切换）时幂等存在。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("创建 schema_migrations: %w", err)
	}

	list, err := migrations.All()
	if err != nil {
		return fmt.Errorf("读取迁移文件: %w", err)
	}

	for _, m := range list {
		var applied bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`,
			m.Name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("查询迁移状态 %s: %w", m.Name, err)
		}
		if applied {
			continue
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("执行迁移 %s: %w", m.Name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, m.Name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("记录迁移 %s: %w", m.Name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("提交迁移 %s: %w", m.Name, err)
		}
	}
	return nil
}
