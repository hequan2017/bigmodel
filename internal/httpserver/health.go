package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/hequan2017/bigmodel/internal/api"
)

// handleLiveness 存活探针：进程能处理请求即返回 200，不检查外部依赖。
func (s *Server) handleLiveness(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadiness 就绪探针：数据库可达且本地持久目录可写才对外可用。
// 模型连通性检查单独展示，不因 Chat 上游故障让服务整体变为不可用。
func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	if err := s.checkDatabase(r.Context()); err != nil {
		s.log.Warn("readiness: 数据库不可用", "error", err)
		api.WriteError(w, r, http.StatusServiceUnavailable, "not_ready", "数据库不可用")
		return
	}
	if err := s.checkDataDir(); err != nil {
		s.log.Warn("readiness: 数据目录不可写", "error", err)
		api.WriteError(w, r, http.StatusServiceUnavailable, "not_ready", "本地数据目录不可写")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) checkDatabase(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.pool.Ping(pingCtx)
}

func (s *Server) checkDataDir() error {
	if err := os.MkdirAll(s.cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("创建数据目录 %s: %w", s.cfg.DataDir, err)
	}
	probe := filepath.Join(s.cfg.DataDir, ".write_probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("写入探测文件: %w", err)
	}
	if err := os.Remove(probe); err != nil {
		return fmt.Errorf("清理探测文件: %w", err)
	}
	return nil
}
