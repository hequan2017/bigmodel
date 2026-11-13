// Package config 加载服务端部署配置。
// 数据库口令、模型密钥等敏感信息一律由部署环境注入，不进入代码仓库与日志。
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config 是进程启动所需的部署配置。
type Config struct {
	HTTPAddr    string // HTTP 监听地址，例如 :8080
	DatabaseURL string // PostgreSQL 连接串
	DataDir     string // 原始资料等本地持久化目录
	LogLevel    string // debug / info / warn / error
	SessionTTL  time.Duration // 会话有效期
}

// GetEnv 取环境变量的函数类型，便于测试注入。
type GetEnv func(string) string

// Load 从环境变量读取配置；未设置时使用本地开发默认值。
func Load(get GetEnv) (Config, error) {
	if get == nil {
		get = os.Getenv
	}

	cfg := Config{
		HTTPAddr:    firstNonEmpty(get("APP_HTTP_ADDR"), ":8080"),
		DatabaseURL: firstNonEmpty(get("DATABASE_URL"), "postgres://bigmodel:bigmodel@localhost:5432/bigmodel?sslmode=disable"),
		DataDir:     firstNonEmpty(get("APP_DATA_DIR"), "./data"),
		LogLevel:    strings.ToLower(strings.TrimSpace(firstNonEmpty(get("APP_LOG_LEVEL"), "info"))),
	}

	ttlRaw := firstNonEmpty(get("APP_SESSION_TTL"), "12h")
	ttl, err := time.ParseDuration(ttlRaw)
	if err != nil || ttl <= 0 {
		return Config{}, fmt.Errorf("APP_SESSION_TTL 非法: %q", ttlRaw)
	}
	cfg.SessionTTL = ttl

	if !strings.HasPrefix(cfg.DatabaseURL, "postgres://") && !strings.HasPrefix(cfg.DatabaseURL, "postgresql://") {
		return Config{}, errors.New("DATABASE_URL 必须是 postgres:// 或 postgresql:// 连接串")
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("APP_LOG_LEVEL 不支持: %q", cfg.LogLevel)
	}
	return cfg, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
