package audit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/config"
	"github.com/hequan2017/bigmodel/internal/database"
)

func newAuditTestPool(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	cfg, err := config.Load(nil)
	if err != nil {
		t.Skipf("配置不可用，跳过集成测试: %v", err)
	}
	ctx := context.Background()
	h := fnv32a(t.Name())
	schema := fmt.Sprintf("itest_audit_%x_%d", h, time.Now().UnixNano())
	if err := database.EnsureSchema(ctx, cfg, schema); err != nil {
		t.Skipf("创建测试 schema 失败: %v", err)
	}
	migPool, err := database.ConnectWithSchema(ctx, cfg, schema, false)
	if err != nil {
		_ = database.DropSchema(ctx, cfg, schema)
		t.Skipf("连接迁移 schema 失败: %v", err)
	}
	if err := database.Migrate(ctx, migPool); err != nil {
		migPool.Close()
		_ = database.DropSchema(ctx, cfg, schema)
		t.Skipf("测试 schema 迁移失败: %v", err)
	}
	migPool.Close()
	pool, err := database.ConnectWithSchema(ctx, cfg, schema, true)
	if err != nil {
		_ = database.DropSchema(ctx, cfg, schema)
		t.Skipf("连接测试 schema 失败: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_ = database.DropSchema(ctx, cfg, schema)
	})
	return pool, func() {}
}

func fnv32a(s string) uint32 {
	var h uint32 = 2166136261
	for _, c := range []byte(s) {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

func TestListEventsIntegration(t *testing.T) {
	pool, _ := newAuditTestPool(t)
	ctx := context.Background()

	var actorID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (login_name, password_hash, role) VALUES ($1, 'x', 'teacher') RETURNING id`,
		fmt.Sprintf("itest_audit_%d", time.Now().UnixNano()),
	).Scan(&actorID); err != nil {
		t.Fatalf("创建用户: %v", err)
	}

	for i := 0; i < 3; i++ {
		Record(ctx, pool, Entry{
			ActorID:      &actorID,
			Action:       fmt.Sprintf("test.action.%d", i),
			ResourceType: "exam",
			ResourceID:   fmt.Sprintf("%d", i),
			Result:       "success",
		})
	}

	events, err := ListEvents(ctx, pool, 100, "")
	if err != nil {
		t.Fatalf("查询审计事件: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("应返回 3 条事件, got %d", len(events))
	}
	// 倒序：最新在前。
	if events[0].Action != "test.action.2" || events[2].Action != "test.action.0" {
		t.Errorf("排序错误: %v, %v", events[0].Action, events[2].Action)
	}
	if events[0].ActorName == "" || events[0].Result != "success" {
		t.Errorf("操作者关联/结果错误: %+v", events[0])
	}

	// limit 生效。
	few, err := ListEvents(ctx, pool, 2, "")
	if err != nil || len(few) != 2 {
		t.Errorf("limit=2 应返回 2 条: %v err=%v", few, err)
	}

	// 未知操作者（actor 为 NULL）不报错。
	Record(ctx, pool, Entry{Action: "test.anonymous", Result: "failed"})
	events, err = ListEvents(ctx, pool, 10, "")
	if err != nil {
		t.Fatalf("含匿名事件查询: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Action == "test.anonymous" && e.ActorName == "" {
			found = true
		}
	}
	if !found {
		t.Error("匿名事件应存在且操作者为空")
	}
}

func TestListEventsActionFilterIntegration(t *testing.T) {
	pool, _ := newAuditTestPool(t)
	ctx := context.Background()

	var actorID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (login_name, password_hash, role) VALUES ($1, 'x', 'teacher') RETURNING id`,
		fmt.Sprintf("itest_filter_%d", time.Now().UnixNano()),
	).Scan(&actorID); err != nil {
		t.Fatalf("创建用户: %v", err)
	}
	Record(ctx, pool, Entry{ActorID: &actorID, Action: "filter.a", Result: "success"})
	Record(ctx, pool, Entry{ActorID: &actorID, Action: "filter.b", Result: "success"})

	events, err := ListEvents(ctx, pool, 100, "filter.a")
	if err != nil {
		t.Fatalf("过滤查询: %v", err)
	}
	if len(events) != 1 || events[0].Action != "filter.a" {
		t.Errorf("过滤结果错误: %+v", events)
	}
}
