// Command seed 创建三种角色的预置演示账号（幂等，重复执行会重置口令）。
// 口令通过 APP_SEED_*_PASSWORD 环境变量覆盖，默认值仅用于本地演示。
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hequan2017/bigmodel/internal/auth"
	"github.com/hequan2017/bigmodel/internal/config"
	"github.com/hequan2017/bigmodel/internal/database"
)

type account struct {
	LoginName string
	Password  string
	Role      string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "初始化账号失败:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("加载配置: %w", err)
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := database.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("执行迁移: %w", err)
	}

	accounts := []account{
		{LoginName: "admin", Password: firstNonEmpty(os.Getenv("APP_SEED_ADMIN_PASSWORD"), "admin@123"), Role: auth.RoleAdmin},
		{LoginName: "teacher", Password: firstNonEmpty(os.Getenv("APP_SEED_TEACHER_PASSWORD"), "teacher@123"), Role: auth.RoleTeacher},
		{LoginName: "student", Password: firstNonEmpty(os.Getenv("APP_SEED_STUDENT_PASSWORD"), "student@123"), Role: auth.RoleStudent},
	}
	for _, a := range accounts {
		hash, err := auth.HashPassword(a.Password)
		if err != nil {
			return fmt.Errorf("生成口令哈希 %s: %w", a.LoginName, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO users (login_name, password_hash, role)
			VALUES ($1, $2, $3)
			ON CONFLICT (login_name) DO UPDATE
			SET password_hash = EXCLUDED.password_hash, enabled = TRUE`,
			a.LoginName, hash, a.Role,
		); err != nil {
			return fmt.Errorf("创建账号 %s: %w", a.LoginName, err)
		}
		fmt.Printf("已就绪账号: %s (%s)\n", a.LoginName, a.Role)
	}
	fmt.Println("提示：口令可通过 APP_SEED_*_PASSWORD 环境变量覆盖")
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
