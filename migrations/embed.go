// Package migrations 内嵌全部 SQL 迁移文件，供启动时按序执行。
package migrations

import (
	"embed"
	"sort"
	"strings"
)

//go:embed *.sql
var files embed.FS

// Migration 是一个按文件名升序执行的 SQL 迁移。
type Migration struct {
	Name string
	SQL  string
}

// All 返回全部迁移，按文件名升序排列。
func All() ([]Migration, error) {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	list := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		sql, err := files.ReadFile(name)
		if err != nil {
			return nil, err
		}
		list = append(list, Migration{Name: name, SQL: string(sql)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}
