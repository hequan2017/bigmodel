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

// ConnectWithSchema 建立指向指定 schema 的连接池（测试隔离用）。
// withPublic=true 时 search_path 追加 public，供运行期解析 pgvector 操作符与函数；
// 迁移请使用 withPublic=false，保证 CREATE TABLE 落在测试 schema 内
// （search_path 含 public 时，同名关系会干扰建表与 IF NOT EXISTS 判断）。
func ConnectWithSchema(ctx context.Context, cfg config.Config, schema string, withPublic bool) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("解析数据库连接串: %w", err)
	}
	searchPath := schema
	if withPublic {
		searchPath += ", public"
	}
	poolCfg.ConnConfig.RuntimeParams["search_path"] = searchPath
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
