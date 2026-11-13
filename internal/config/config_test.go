package config

import (
	"testing"
	"time"
)

func envOf(m map[string]string) GetEnv {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(envOf(map[string]string{}))
	if err != nil {
		t.Fatalf("默认配置应可用: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.DatabaseURL != "postgres://bigmodel:bigmodel@localhost:5432/bigmodel?sslmode=disable" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.DataDir != "./data" {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(envOf(map[string]string{
		"APP_HTTP_ADDR": ":9090",
		"DATABASE_URL":  "postgresql://u:p@db:5432/x?sslmode=require",
		"APP_DATA_DIR":  "/var/lib/bigmodel",
		"APP_LOG_LEVEL": "DEBUG",
		"APP_SESSION_TTL": "24h",
	}))
	if err != nil {
		t.Fatalf("覆盖配置应可用: %v", err)
	}
	if cfg.HTTPAddr != ":9090" || cfg.DataDir != "/var/lib/bigmodel" {
		t.Errorf("覆盖未生效: %+v", cfg)
	}
	if cfg.DatabaseURL != "postgresql://u:p@db:5432/x?sslmode=require" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel 应统一小写, got %q", cfg.LogLevel)
	}
	if cfg.SessionTTL != 24*time.Hour {
		t.Errorf("SessionTTL = %v", cfg.SessionTTL)
	}
}

func TestLoadBlankEnvFallsBackToDefault(t *testing.T) {
	cfg, err := Load(envOf(map[string]string{
		"APP_DATA_DIR": "   ",
	}))
	if err != nil {
		t.Fatalf("空白环境变量应回退默认值: %v", err)
	}
	if cfg.DataDir != "./data" {
		t.Errorf("DataDir 应为默认值, got %q", cfg.DataDir)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"不支持的数据库协议", map[string]string{"DATABASE_URL": "mysql://u:p@localhost/db"}},
		{"不支持的日志级别", map[string]string{"APP_LOG_LEVEL": "verbose"}},
		{"非法会话时长", map[string]string{"APP_SESSION_TTL": "abc"}},
	}
	for _, tc := range cases {
		if _, err := Load(envOf(tc.env)); err == nil {
			t.Errorf("%s: 应返回错误", tc.name)
		}
	}
}
