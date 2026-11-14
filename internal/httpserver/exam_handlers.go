package httpserver

import (
	"errors"
	"net/http"

	"github.com/hequan2017/bigmodel/internal/api"
	"github.com/hequan2017/bigmodel/internal/audit"
	"github.com/hequan2017/bigmodel/internal/auth"
	"github.com/hequan2017/bigmodel/internal/exam"
)

// writeExamError 把题库服务错误映射为稳定错误码。
func (s *Server) writeExamError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, exam.ErrNotFound):
		api.WriteError(w, r, http.StatusNotFound, "not_found", "题目不存在")
	case errors.Is(err, exam.ErrInvalidData):
		api.WriteError(w, r, http.StatusBadRequest, "invalid_data", err.Error())
	case errors.Is(err, exam.ErrInvalidState):
		api.WriteError(w, r, http.StatusConflict, "invalid_state", "已停用题目不可修改")
	default:
		s.log.Error("题库操作失败", "error", err)
		api.WriteError(w, r, http.StatusInternalServerError, "internal_error", "题库操作失败")
	}
}

// handleCreateQuestion 创建题目（教师/管理员）。
func (s *Server) handleCreateQuestion(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in exam.QuestionInput
	if err := jsonDecode(r.Body, &in); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}
	q, err := s.exam.CreateQuestion(r.Context(), current.ID, in)
	if err != nil {
		s.writeExamError(w, r, err)
		return
	}
	audit.Record(r.Context(), s.pool, audit.Entry{
		ActorID:      &current.ID,
		Action:       "exam.question.create",
		ResourceType: "exam_question",
		ResourceID:   int64ToString(q.ID),
		Result:       q.Type,
	})
	api.WriteJSON(w, http.StatusCreated, map[string]any{"question": q})
}

// handleUpdateQuestion 更新题目。
func (s *Server) handleUpdateQuestion(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	questionID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "题目 ID 非法")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in exam.QuestionInput
	if err := jsonDecode(r.Body, &in); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}
	q, err := s.exam.UpdateQuestion(r.Context(), current.ID, current.Role == auth.RoleAdmin, questionID, in)
	if err != nil {
		s.writeExamError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"question": q})
}

// handleDisableQuestion 停用题目（幂等软删除）。
func (s *Server) handleDisableQuestion(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	questionID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "题目 ID 非法")
		return
	}
	if err := s.exam.DisableQuestion(r.Context(), current.ID, current.Role == auth.RoleAdmin, questionID); err != nil {
		s.writeExamError(w, r, err)
		return
	}
	audit.Record(r.Context(), s.pool, audit.Entry{
		ActorID:      &current.ID,
		Action:       "exam.question.disable",
		ResourceType: "exam_question",
		ResourceID:   int64ToString(questionID),
		Result:       "success",
	})
	api.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleListQuestions 题目列表（可按题型过滤）。
func (s *Server) handleListQuestions(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	questions, err := s.exam.ListQuestions(r.Context(), current.ID, current.Role == auth.RoleAdmin, r.URL.Query().Get("type"))
	if err != nil {
		s.writeExamError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"questions": questions})
}

// handleGetQuestion 题目详情（教师侧，含标准答案）。
func (s *Server) handleGetQuestion(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	questionID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "题目 ID 非法")
		return
	}
	q, err := s.exam.GetQuestion(r.Context(), current.ID, current.Role == auth.RoleAdmin, questionID)
	if err != nil {
		s.writeExamError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"question": q})
}
