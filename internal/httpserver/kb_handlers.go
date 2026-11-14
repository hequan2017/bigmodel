package httpserver

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/hequan2017/bigmodel/internal/api"
	"github.com/hequan2017/bigmodel/internal/audit"
	"github.com/hequan2017/bigmodel/internal/auth"
	"github.com/hequan2017/bigmodel/internal/kb"
)

// writeKBError 把知识库服务错误映射为稳定错误码。
func (s *Server) writeKBError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, kb.ErrNotFound):
		api.WriteError(w, r, http.StatusNotFound, "not_found", "知识库或文档不存在")
	case errors.Is(err, kb.ErrDuplicateName):
		api.WriteError(w, r, http.StatusConflict, "duplicate_name", "同名知识库已存在")
	case errors.Is(err, kb.ErrEmbeddingNotConfigured):
		api.WriteError(w, r, http.StatusConflict, "embedding_not_configured", "尚未配置可用的 Embedding 模型，无法创建知识库")
	case errors.Is(err, kb.ErrInvalidName):
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, kb.ErrFileTooLarge):
		api.WriteError(w, r, http.StatusRequestEntityTooLarge, "file_too_large", "文件超过 5 MiB 上限")
	case errors.Is(err, kb.ErrInvalidFile):
		api.WriteError(w, r, http.StatusBadRequest, "invalid_file", err.Error())
	case errors.Is(err, kb.ErrInvalidState):
		api.WriteError(w, r, http.StatusConflict, "invalid_state", "当前文档状态不允许该操作")
	default:
		s.log.Error("知识库操作失败", "error", err)
		api.WriteError(w, r, http.StatusInternalServerError, "internal_error", "知识库操作失败")
	}
}

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

// handleCreateKB 创建知识库（教师本人；管理员代建）。
func (s *Server) handleCreateKB(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := jsonDecode(r.Body, &req); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}

	kbRow, err := s.kb.CreateKB(r.Context(), current.ID, req.Name, req.Description)
	if err != nil {
		s.writeKBError(w, r, err)
		return
	}
	audit.Record(r.Context(), s.pool, audit.Entry{
		ActorID:      &current.ID,
		Action:       "kb.create",
		ResourceType: "knowledge_base",
		ResourceID:   strconv.FormatInt(kbRow.ID, 10),
		Result:       "success",
	})
	api.WriteJSON(w, http.StatusCreated, map[string]any{"knowledge_base": kbRow})
}

// handleListKBs 列出知识库：教师本人，管理员全部。
func (s *Server) handleListKBs(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	rows, err := s.kb.ListKBs(r.Context(), current.ID, current.Role == auth.RoleAdmin)
	if err != nil {
		s.writeKBError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"knowledge_bases": rows})
}

// handleGetKB 知识库详情与文档列表。
func (s *Server) handleGetKB(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	kbID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "知识库 ID 非法")
		return
	}
	kbRow, docs, err := s.kb.GetKB(r.Context(), current.ID, current.Role == auth.RoleAdmin, kbID)
	if err != nil {
		s.writeKBError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"knowledge_base": kbRow, "documents": docs})
}

// handleUploadDocument 上传 TXT/Markdown 文档。
func (s *Server) handleUploadDocument(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	kbID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "知识库 ID 非法")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, kb.MaxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "multipart 解析失败或超出大小限制")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "缺少 file 字段")
		return
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, kb.MaxUploadBytes+1))
	if err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "读取文件失败")
		return
	}

	doc, existing, err := s.kb.UploadDocument(r.Context(), current.ID, current.Role == auth.RoleAdmin,
		kbID, header.Filename, header.Header.Get("Content-Type"), content)
	if err != nil {
		s.writeKBError(w, r, err)
		return
	}
	audit.Record(r.Context(), s.pool, audit.Entry{
		ActorID:      &current.ID,
		Action:       "document.upload",
		ResourceType: "document",
		ResourceID:   strconv.FormatInt(doc.ID, 10),
		Result:       "success",
	})
	api.WriteJSON(w, http.StatusCreated, map[string]any{"document": doc, "existing": existing})
}

// handleDisableDocument 停用文档（幂等）。
func (s *Server) handleDisableDocument(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	kbID, ok1 := pathID(r, "id")
	docID, ok2 := pathID(r, "docID")
	if !ok1 || !ok2 {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "ID 非法")
		return
	}
	if err := s.kb.DisableDocument(r.Context(), current.ID, current.Role == auth.RoleAdmin, kbID, docID); err != nil {
		s.writeKBError(w, r, err)
		return
	}
	audit.Record(r.Context(), s.pool, audit.Entry{
		ActorID:      &current.ID,
		Action:       "document.disable",
		ResourceType: "document",
		ResourceID:   strconv.FormatInt(docID, 10),
		Result:       "success",
	})
	api.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRetryDocument 重试失败文档（仅 failed 状态）。
func (s *Server) handleRetryDocument(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	kbID, ok1 := pathID(r, "id")
	docID, ok2 := pathID(r, "docID")
	if !ok1 || !ok2 {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "ID 非法")
		return
	}
	doc, err := s.kb.RetryDocument(r.Context(), current.ID, current.Role == auth.RoleAdmin, kbID, docID)
	if err != nil {
		s.writeKBError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"document": doc})
}
