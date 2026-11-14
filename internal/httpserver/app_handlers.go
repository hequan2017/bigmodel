package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/hequan2017/bigmodel/internal/api"
	"github.com/hequan2017/bigmodel/internal/app"
	"github.com/hequan2017/bigmodel/internal/audit"
	"github.com/hequan2017/bigmodel/internal/auth"
)

// appErrorCode 把应用服务错误映射为稳定错误码与说明。
func appErrorCode(err error) (string, string) {
	switch {
	case errors.Is(err, app.ErrNotFound):
		return "not_found", "应用不存在或不可见"
	case errors.Is(err, app.ErrNotRunnable):
		return "app_not_runnable", "应用当前不可运行"
	case errors.Is(err, app.ErrDuplicateName):
		return "duplicate_name", "同名应用已存在"
	case errors.Is(err, app.ErrInvalidState):
		return "invalid_state", "当前状态不允许该操作"
	case errors.Is(err, app.ErrInvalidConfig):
		return "invalid_config", err.Error()
	case errors.Is(err, app.ErrInvalidName):
		return "invalid_name", err.Error()
	case errors.Is(err, app.ErrInvalidQuestion):
		return "invalid_question", err.Error()
	case errors.Is(err, app.ErrChatNotConfigured):
		return "chat_not_configured", "尚未配置 Chat 上游，无法运行应用"
	case errors.Is(err, app.ErrEmbeddingNotConfigured):
		return "embedding_not_configured", "尚未配置 Embedding 上游，无法运行应用"
	default:
		return "internal_error", "应用操作失败"
	}
}

func appErrorStatus(code string) int {
	switch code {
	case "not_found":
		return http.StatusNotFound
	case "app_not_runnable", "duplicate_name", "invalid_state",
		"chat_not_configured", "embedding_not_configured":
		return http.StatusConflict
	case "invalid_config", "invalid_name", "invalid_question":
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) writeAppError(w http.ResponseWriter, r *http.Request, err error) {
	code, msg := appErrorCode(err)
	api.WriteError(w, r, appErrorStatus(code), code, msg)
}

// decodeAppConfig 严格解析配置：未知字段拒绝（§9.1 后端 Schema 校验）。
func decodeAppConfig(raw json.RawMessage) (app.AppConfig, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg app.AppConfig
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("%w: %s", app.ErrInvalidConfig, err)
	}
	if dec.More() {
		return cfg, fmt.Errorf("%w: config 含多余内容", app.ErrInvalidConfig)
	}
	return cfg, nil
}

type appWriteRequest struct {
	Name   string          `json:"name"`
	Config json.RawMessage `json:"config"`
}

// handleCreateApp 创建应用草稿（教师/管理员）。
func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var req appWriteRequest
	if err := jsonDecode(r.Body, &req); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}
	cfg, err := decodeAppConfig(req.Config)
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	a, err := s.apps.CreateDraft(r.Context(), current.ID, req.Name, cfg)
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	audit.Record(r.Context(), s.pool, audit.Entry{
		ActorID:      &current.ID,
		Action:       "app.create",
		ResourceType: "app",
		ResourceID:   fmt.Sprintf("%d", a.ID),
		Result:       "success",
	})
	api.WriteJSON(w, http.StatusCreated, map[string]any{"app": a})
}

// handleUpdateApp 更新草稿配置（已发布快照不受影响）。
func (s *Server) handleUpdateApp(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	appID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "应用 ID 非法")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var req appWriteRequest
	if err := jsonDecode(r.Body, &req); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}
	cfg, err := decodeAppConfig(req.Config)
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	a, err := s.apps.UpdateDraft(r.Context(), current.ID, current.Role, current.Role == auth.RoleAdmin, appID, cfg)
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"app": a})
}

// handlePublishApp 发布快照。
func (s *Server) handlePublishApp(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	appID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "应用 ID 非法")
		return
	}
	a, err := s.apps.Publish(r.Context(), current.ID, current.Role, current.Role == auth.RoleAdmin, appID)
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	audit.Record(r.Context(), s.pool, audit.Entry{
		ActorID:      &current.ID,
		Action:       "app.publish",
		ResourceType: "app",
		ResourceID:   fmt.Sprintf("%d", a.ID),
		Result:       fmt.Sprintf("revision:%d", a.PublishedRevision),
	})
	api.WriteJSON(w, http.StatusOK, map[string]any{"app": a})
}

// handleDisableApp 停用应用（拒绝新运行）。
func (s *Server) handleDisableApp(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	appID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "应用 ID 非法")
		return
	}
	if err := s.apps.Disable(r.Context(), current.ID, current.Role, current.Role == auth.RoleAdmin, appID); err != nil {
		s.writeAppError(w, r, err)
		return
	}
	audit.Record(r.Context(), s.pool, audit.Entry{
		ActorID:      &current.ID,
		Action:       "app.disable",
		ResourceType: "app",
		ResourceID:   fmt.Sprintf("%d", appID),
		Result:       "success",
	})
	api.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleListApps 应用列表：学生仅见已发布应用。
func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	rows, err := s.apps.List(r.Context(), current.ID, current.Role, current.Role == auth.RoleAdmin)
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"apps": rows})
}

// handleGetApp 应用详情；学生不可见草稿应用。
func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	appID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "应用 ID 非法")
		return
	}
	a, err := s.apps.Get(r.Context(), current.ID, current.Role, current.Role == auth.RoleAdmin, appID)
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"app": a})
}

// writeSSE 写一个 SSE 事件并立即冲刷；写失败返回错误以终止上游。
func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// handleRunApp SSE 流式问答（§9.4 事件流）。
func (s *Server) handleRunApp(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	appID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "应用 ID 非法")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Question       string `json:"question"`
		ConversationID string `json:"conversation_id"`
	}
	if err := jsonDecode(r.Body, &req); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		api.WriteError(w, r, http.StatusInternalServerError, "internal_error", "当前连接不支持流式输出")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emit := func(ev app.Event) error {
		return writeSSE(w, flusher, ev.Type, ev.Payload)
	}
	err := s.apps.RunApp(r.Context(), app.RunParams{
		UserID:         current.ID,
		Role:           current.Role,
		AppID:          appID,
		Question:       req.Question,
		ConversationID: req.ConversationID,
		Chat:           s.models.Chat(),
		ChatModel:      s.models.ChatModelName(),
		Embed:          s.models.Embedding(),
		EmbedModel:     s.models.EmbeddingModelName(),
	}, emit)
	if err != nil {
		// 流开始前的配置/权限错误：以 error 事件返回（§9.4）。
		code, msg := appErrorCode(err)
		_ = writeSSE(w, flusher, "error", map[string]any{"code": code, "message": msg})
	}
}
