// Package database 提供 PostgreSQL 连接池与迁移执行。
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/config"
)

// Connect 建立连接池并确认可达。
// 连接数上限是单实例演示规模的初始值，后续按容量测试结果调整。
func Connect(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("解析数据库连接串: %w", err)
	}
	return connectWithConfig(ctx, poolCfg)
}

// EnsureSchema 创建独立 schema（测试隔离用）。
func EnsureSchema(ctx context.Context, cfg config.Config, schema string) error {
	pool, err := Connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, pgx.Identifier{schema}.Sanitize())); err != nil {
		return fmt.Errorf("创建 schema: %w", err)
	}
	return nil
}

// ConnectWithSchema 建立把 search_path 限定到指定 schema 的连接池（测试隔离用），
// 使迁移与业务表落在独立 schema 中，不污染共享开发数据。
func ConnectWithSchema(ctx context.Context, cfg config.Config, schema string) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("解析数据库连接串: %w", err)
	}
	poolCfg.ConnConfig.RuntimeParams["search_path"] = schema
	return connectWithConfig(ctx, poolCfg)
}

// DropSchema 删除 schema 及其全部对象（测试清理用）。
func DropSchema(ctx context.Context, cfg config.Config, schema string) error {
	pool, err := Connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, pgx.Identifier{schema}.Sanitize())); err != nil {
		return fmt.Errorf("删除 schema: %w", err)
	}
	return nil
}

func connectWithConfig(ctx context.Context, poolCfg *pgxpool.Config) (*pgxpool.Pool, error) {
	poolCfg.MaxConns = 10
	poolCfg.MinConns = 1
	poolCfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("创建连接池: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("连接数据库: %w", err)
	}
	return pool, nil
}
