// Package httpserver 组装 HTTP 路由、中间件与业务 handler。
// 本期使用标准库路由，不引入框架。
package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/api"
	"github.com/hequan2017/bigmodel/internal/auth"
	"github.com/hequan2017/bigmodel/internal/config"
	"github.com/hequan2017/bigmodel/internal/model"
)

// Server 持有运行期依赖，全部路由通过它注册。
type Server struct {
	cfg    config.Config
	pool   *pgxpool.Pool
	auth   *auth.Service
	models *model.Service
	log    *slog.Logger
	http   *http.Server
}

// New 构造 HTTP 服务；models 为模型接入服务。
func New(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger, models *model.Service) *Server {
	s := &Server{cfg: cfg, pool: pool, log: log, models: models}
	s.auth = auth.NewService(pool, cfg.SessionTTL)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleLiveness)
	mux.HandleFunc("GET /readyz", s.handleReadiness)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("GET /api/auth/me", s.requireAuth(s.handleMe))
	mux.HandleFunc("GET /api/admin/models", s.requireAuth(s.requireRole(s.handleListModels, auth.RoleAdmin)))
	mux.HandleFunc("POST /api/admin/models/chat/test", s.requireAuth(s.requireRole(s.handleTestChat, auth.RoleAdmin)))
	mux.HandleFunc("POST /api/admin/models/embedding/test", s.requireAuth(s.requireRole(s.handleTestEmbedding, auth.RoleAdmin)))
	mux.HandleFunc("/", s.handleNotFound)

	s.http = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           s.wrap(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// ListenAndServe 启动监听。
func (s *Server) ListenAndServe() error {
	return s.http.ListenAndServe()
}

// Shutdown 优雅关闭。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// handleNotFound 让未匹配路由也返回统一 JSON 错误结构。
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, r, http.StatusNotFound, "not_found", "接口不存在")
}
