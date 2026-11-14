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
	"github.com/hequan2017/bigmodel/internal/kb"
	"github.com/hequan2017/bigmodel/internal/model"
)

// buildVersion 由构建时 -ldflags -X 注入；本地运行默认 dev。
var buildVersion = "dev"

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

	modelSvc := model.NewService(pool, cfg)
	if err := modelSvc.SeedProfiles(ctx); err != nil {
		return fmt.Errorf("初始化模型配置: %w", err)
	}
	logger.Info("模型配置已就绪",
		"chat_configured", modelSvc.ChatConfigured(),
		"embedding_configured", modelSvc.EmbeddingConfigured(),
	)

	kbSvc := kb.NewService(pool, cfg.DataDir)
	srv := httpserver.New(cfg, pool, logger, modelSvc, kbSvc)
	srv.SetVersion(buildVersion, time.Now())

	// 到期作答自动结算循环（§14.7）。
	srv.StartExamSweep(ctx, 30*time.Second)

	// 单进程文档处理 worker；未配置 Embedding 上游时不启动，上传文档保持 queued。
	if modelSvc.EmbeddingConfigured() {
		docWorker := kbSvc.NewWorker(modelSvc.Embedding(), modelSvc.EmbeddingModelName(), 2*time.Second, logger)
		if stale, err := docWorker.RecoverStale(ctx); err != nil {
			logger.Warn("中断文档恢复失败", "error", err)
		} else if stale > 0 {
			logger.Info("已将上次中断的文档标记为 failed，可人工重试", "count", stale)
		}
		go docWorker.Run(ctx)
		logger.Info("文档处理 worker 已启动", "embedding_model", modelSvc.EmbeddingModelName())
	} else {
		logger.Warn("未配置 Embedding 上游，文档处理 worker 未启动，上传文档将保持 queued")
	}

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
