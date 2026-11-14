package migrations

import (
	"strings"
	"testing"
)

func TestAllSortedWithInit(t *testing.T) {
	list, err := All()
	if err != nil {
		t.Fatalf("读取迁移: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("没有任何迁移文件")
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Name >= list[i].Name {
			t.Fatalf("迁移未按名称排序: %s >= %s", list[i-1].Name, list[i].Name)
		}
	}
	first := list[0]
	if first.Name != "001_init.sql" {
		t.Fatalf("首个迁移应为 001_init.sql，实际 %s", first.Name)
	}
	if !strings.Contains(first.SQL, "CREATE EXTENSION IF NOT EXISTS vector") {
		t.Error("首个迁移应启用 pgvector 扩展")
	}
	if !strings.Contains(first.SQL, "CREATE TABLE users") {
		t.Error("首个迁移应包含 users 表")
	}
	if !strings.Contains(first.SQL, "CREATE TABLE audit_events") {
		t.Error("首个迁移应包含 audit_events 表")
	}
}
