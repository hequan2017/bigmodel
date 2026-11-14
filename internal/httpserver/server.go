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
	"github.com/hequan2017/bigmodel/internal/app"
	"github.com/hequan2017/bigmodel/internal/auth"
	"github.com/hequan2017/bigmodel/internal/config"
	"github.com/hequan2017/bigmodel/internal/exam"
	"github.com/hequan2017/bigmodel/internal/kb"
	"github.com/hequan2017/bigmodel/internal/model"
)

// Server 持有运行期依赖，全部路由通过它注册。
type Server struct {
	cfg       config.Config
	pool      *pgxpool.Pool
	auth      *auth.Service
	models    *model.Service
	kb        *kb.Service
	apps      *app.Service
	exam      *exam.Service
	log       *slog.Logger
	http      *http.Server
	version   string
	startedAt time.Time
}

// SetVersion 设置构建版本与启动时间，供 /api/version 展示（运维排障用）。
func (s *Server) SetVersion(version string, startedAt time.Time) {
	s.version = version
	s.startedAt = startedAt
}

// New 构造 HTTP 服务；models 为模型接入服务，kbSvc 为知识库服务。
func New(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger, models *model.Service, kbSvc *kb.Service) *Server {
	s := &Server{cfg: cfg, pool: pool, log: log, models: models, kb: kbSvc}
	s.auth = auth.NewService(pool, cfg.SessionTTL)
	s.apps = app.NewService(pool, s.kb)
	s.exam = exam.NewService(pool)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleLiveness)
	mux.HandleFunc("GET /readyz", s.handleReadiness)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("GET /api/auth/me", s.requireAuth(s.handleMe))
	mux.HandleFunc("GET /api/admin/models", s.requireAuth(s.requireRole(s.handleListModels, auth.RoleAdmin)))
	mux.HandleFunc("GET /api/admin/audit", s.requireAuth(s.requireRole(s.handleAdminAudit, auth.RoleAdmin)))
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("POST /api/admin/models/chat/test", s.requireAuth(s.requireRole(s.handleTestChat, auth.RoleAdmin)))
	mux.HandleFunc("POST /api/admin/models/embedding/test", s.requireAuth(s.requireRole(s.handleTestEmbedding, auth.RoleAdmin)))

	kbRoles := []string{auth.RoleTeacher, auth.RoleAdmin}
	mux.HandleFunc("POST /api/prompt/validate", s.requireAuth(s.requireRole(s.handleValidatePrompt, kbRoles...)))
	mux.HandleFunc("POST /api/prompt/render", s.requireAuth(s.requireRole(s.handleRenderPrompt, kbRoles...)))
	mux.HandleFunc("POST /api/kb", s.requireAuth(s.requireRole(s.handleCreateKB, kbRoles...)))
	mux.HandleFunc("GET /api/kb", s.requireAuth(s.requireRole(s.handleListKBs, kbRoles...)))
	mux.HandleFunc("GET /api/kb/{id}", s.requireAuth(s.requireRole(s.handleGetKB, kbRoles...)))
	mux.HandleFunc("POST /api/kb/{id}/documents", s.requireAuth(s.requireRole(s.handleUploadDocument, kbRoles...)))
	mux.HandleFunc("DELETE /api/kb/{id}/documents/{docID}", s.requireAuth(s.requireRole(s.handleDisableDocument, kbRoles...)))
	mux.HandleFunc("POST /api/kb/{id}/documents/{docID}/retry", s.requireAuth(s.requireRole(s.handleRetryDocument, kbRoles...)))
	mux.HandleFunc("GET /api/kb/{id}/documents/{docID}/chunks", s.requireAuth(s.requireRole(s.handleListChunks, kbRoles...)))
	mux.HandleFunc("POST /api/kb/{id}/search", s.requireAuth(s.requireRole(s.handleSearchKB, kbRoles...)))

	mux.HandleFunc("POST /api/apps", s.requireAuth(s.requireRole(s.handleCreateApp, kbRoles...)))
	mux.HandleFunc("GET /api/apps", s.requireAuth(s.handleListApps))
	mux.HandleFunc("GET /api/apps/{id}", s.requireAuth(s.handleGetApp))
	mux.HandleFunc("PUT /api/apps/{id}", s.requireAuth(s.requireRole(s.handleUpdateApp, kbRoles...)))
	mux.HandleFunc("POST /api/apps/{id}/publish", s.requireAuth(s.requireRole(s.handlePublishApp, kbRoles...)))
	mux.HandleFunc("DELETE /api/apps/{id}", s.requireAuth(s.requireRole(s.handleDisableApp, kbRoles...)))
	mux.HandleFunc("POST /api/apps/{id}/run", s.requireAuth(s.handleRunApp))
	mux.HandleFunc("GET /api/apps/{id}/messages", s.requireAuth(s.handleAppMessages))
	mux.HandleFunc("GET /api/apps/{id}/kb-documents/{docID}/chunks", s.requireAuth(s.handleReferenceChunks))

	mux.HandleFunc("POST /api/exam/questions", s.requireAuth(s.requireRole(s.handleCreateQuestion, kbRoles...)))
	mux.HandleFunc("GET /api/exam/questions", s.requireAuth(s.requireRole(s.handleListQuestions, kbRoles...)))
	mux.HandleFunc("GET /api/exam/questions/{id}", s.requireAuth(s.requireRole(s.handleGetQuestion, kbRoles...)))
	mux.HandleFunc("PUT /api/exam/questions/{id}", s.requireAuth(s.requireRole(s.handleUpdateQuestion, kbRoles...)))
	mux.HandleFunc("DELETE /api/exam/questions/{id}", s.requireAuth(s.requireRole(s.handleDisableQuestion, kbRoles...)))

	mux.HandleFunc("POST /api/exam/exams", s.requireAuth(s.requireRole(s.handleCreateExam, kbRoles...)))
	mux.HandleFunc("GET /api/exam/exams", s.requireAuth(s.requireRole(s.handleListExams, kbRoles...)))
	mux.HandleFunc("GET /api/exam/exams/{id}", s.requireAuth(s.requireRole(s.handleGetExam, kbRoles...)))
	mux.HandleFunc("PUT /api/exam/exams/{id}", s.requireAuth(s.requireRole(s.handleUpdateExam, kbRoles...)))
	mux.HandleFunc("PUT /api/exam/exams/{id}/items", s.requireAuth(s.requireRole(s.handleSetExamItems, kbRoles...)))
	mux.HandleFunc("POST /api/exam/exams/{id}/publish", s.requireAuth(s.requireRole(s.handlePublishExam, kbRoles...)))
	mux.HandleFunc("DELETE /api/exam/exams/{id}", s.requireAuth(s.requireRole(s.handleDisableExam, kbRoles...)))

	mux.HandleFunc("POST /api/exam/exams/{id}/attempts", s.requireAuth(s.requireRole(s.handleStartAttempt, auth.RoleStudent)))
	mux.HandleFunc("GET /api/exam/exams/{id}/attempts", s.requireAuth(s.requireRole(s.handleGetAttempt, auth.RoleStudent)))
	mux.HandleFunc("PUT /api/exam/exams/{id}/attempts/answers", s.requireAuth(s.requireRole(s.handleSaveAnswers, auth.RoleStudent)))
	mux.HandleFunc("POST /api/exam/exams/{id}/attempts/submit", s.requireAuth(s.requireRole(s.handleSubmitAttempt, auth.RoleStudent)))
	mux.HandleFunc("GET /api/exam/exams/{id}/attempts/result", s.requireAuth(s.requireRole(s.handleAttemptResult, auth.RoleStudent)))
	mux.HandleFunc("GET /api/exam/exams/{id}/results", s.requireAuth(s.requireRole(s.handleExamResults, kbRoles...)))
	mux.HandleFunc("GET /api/exam/exams/{id}/attempts/{attemptID}", s.requireAuth(s.requireRole(s.handleAttemptDetail, kbRoles...)))
	mux.HandleFunc("GET /api/exam/exams/{id}/stats", s.requireAuth(s.requireRole(s.handleExamStats, kbRoles...)))
	mux.HandleFunc("GET /api/exam/exams/{id}/results/export", s.requireAuth(s.requireRole(s.handleResultsExport, kbRoles...)))
	mux.HandleFunc("GET /api/exam/available", s.requireAuth(s.requireRole(s.handleAvailableExams, auth.RoleStudent)))

	// 学生端页面同源托管（§15.1）；非 GET 的根路径仍走统一 JSON 404。
	mux.Handle("GET /", s.staticHandler())
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

// handleVersion 返回构建版本与启动时间（无认证，无敏感信息）。
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"version":    s.version,
		"started_at": s.startedAt,
	})
}
