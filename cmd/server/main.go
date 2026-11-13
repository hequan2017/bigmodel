// Command server 是平台唯一的进程：HTTP API 由本入口启动，
// 文档处理与到期结算等后台循环由后续功能在同一进程中接入。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hequan2017/bigmodel/internal/config"
	"github.com/hequan2017/bigmodel/internal/database"
	"github.com/hequan2017/bigmodel/internal/httpserver"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("加载配置: %w", err)
	}

	logger, err := newLogger(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("初始化日志: %w", err)
	}
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := database.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("执行数据库迁移: %w", err)
	}
	logger.Info("数据库迁移完成")

	srv := httpserver.New(cfg, pool, logger)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.ListenAndServe()
	}()
	logger.Info("HTTP 服务已启动", "addr", cfg.HTTPAddr)

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP 服务异常退出: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("HTTP 服务关闭: %w", err)
	}
	logger.Info("HTTP 服务已优雅退出")
	return nil
}

func newLogger(level string) (*slog.Logger, error) {
	var slogLevel slog.Level
	switch level {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	case "info":
		slogLevel = slog.LevelInfo
	default:
		return nil, fmt.Errorf("不支持的日志级别: %q", level)
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel})), nil
}
