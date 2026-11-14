package httpserver

import (
	"errors"
	"fmt"
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

// writeExamHttpError 把考试服务错误映射为 HTTP 状态码与稳定错误码。
func writeExamHttpError(err error) (string, int) {
	switch {
	case errors.Is(err, exam.ErrNotFound), errors.Is(err, exam.ErrAttemptNotFound):
		return "not_found", http.StatusNotFound
	case errors.Is(err, exam.ErrExamNotDraft):
		return "exam_not_draft", http.StatusConflict
	case errors.Is(err, exam.ErrExamDuplicate):
		return "duplicate_exam", http.StatusConflict
	case errors.Is(err, exam.ErrExamNotOpen):
		return "exam_not_open", http.StatusConflict
	case errors.Is(err, exam.ErrExamClosed):
		return "exam_closed", http.StatusConflict
	case errors.Is(err, exam.ErrAttemptExpired):
		return "attempt_expired", http.StatusConflict
	case errors.Is(err, exam.ErrAttemptSubmitted):
		return "attempt_submitted", http.StatusConflict
	case errors.Is(err, exam.ErrRevisionConflict):
		return "revision_conflict", http.StatusConflict
	case errors.Is(err, exam.ErrExamInvalidTimes),
		errors.Is(err, exam.ErrExamMissingTimes),
		errors.Is(err, exam.ErrExamNoItems),
		errors.Is(err, exam.ErrExamNoTotalScore),
		errors.Is(err, exam.ErrExamItemInvalid),
		errors.Is(err, exam.ErrInvalidData):
		return "invalid_data", http.StatusBadRequest
	case errors.Is(err, exam.ErrAnswerInvalid):
		return "invalid_answer", http.StatusBadRequest
	default:
		return "internal_error", http.StatusInternalServerError
	}
}

func (s *Server) handleExamError(w http.ResponseWriter, r *http.Request, err error) {
	code, status := writeExamHttpError(err)
	if code == "internal_error" {
		s.log.Error("考试操作失败", "error", err)
		api.WriteError(w, r, status, code, "考试操作失败")
		return
	}
	api.WriteError(w, r, status, code, err.Error())
}

// handleCreateExam 创建考试草稿。
func (s *Server) handleCreateExam(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in exam.ExamInput
	if err := jsonDecode(r.Body, &in); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}
	e, err := s.exam.CreateExam(r.Context(), current.ID, in)
	if err != nil {
		s.handleExamError(w, r, err)
		return
	}
	audit.Record(r.Context(), s.pool, audit.Entry{
		ActorID:      &current.ID,
		Action:       "exam.create",
		ResourceType: "exam",
		ResourceID:   int64ToString(e.ID),
		Result:       e.Status,
	})
	api.WriteJSON(w, http.StatusCreated, map[string]any{"exam": e})
}

// handleUpdateExam 更新考试元信息（仅草稿）。
func (s *Server) handleUpdateExam(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	examID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "考试 ID 非法")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in exam.ExamInput
	if err := jsonDecode(r.Body, &in); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}
	e, err := s.exam.UpdateExam(r.Context(), current.ID, current.Role == auth.RoleAdmin, examID, in)
	if err != nil {
		s.handleExamError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"exam": e})
}

// handleSetExamItems 整体替换草稿试卷条目（生成快照）。
func (s *Server) handleSetExamItems(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	examID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "考试 ID 非法")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var req struct {
		Items []exam.ExamItemInput `json:"items"`
	}
	if err := jsonDecode(r.Body, &req); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}
	items, err := s.exam.SetExamItems(r.Context(), current.ID, current.Role == auth.RoleAdmin, examID, req.Items)
	if err != nil {
		s.handleExamError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handlePublishExam 发布考试。
func (s *Server) handlePublishExam(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	examID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "考试 ID 非法")
		return
	}
	e, err := s.exam.PublishExam(r.Context(), current.ID, current.Role == auth.RoleAdmin, examID)
	if err != nil {
		s.handleExamError(w, r, err)
		return
	}
	audit.Record(r.Context(), s.pool, audit.Entry{
		ActorID:      &current.ID,
		Action:       "exam.publish",
		ResourceType: "exam",
		ResourceID:   int64ToString(e.ID),
		Result:       fmt.Sprintf("total_score:%d", e.TotalScore),
	})
	api.WriteJSON(w, http.StatusOK, map[string]any{"exam": e})
}

// handleDisableExam 删除草稿考试。
func (s *Server) handleDisableExam(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	examID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "考试 ID 非法")
		return
	}
	if err := s.exam.DisableExam(r.Context(), current.ID, current.Role == auth.RoleAdmin, examID); err != nil {
		s.handleExamError(w, r, err)
		return
	}
	audit.Record(r.Context(), s.pool, audit.Entry{
		ActorID:      &current.ID,
		Action:       "exam.disable",
		ResourceType: "exam",
		ResourceID:   int64ToString(examID),
		Result:       "success",
	})
	api.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleListExams 考试列表（教师/管理员）。
func (s *Server) handleListExams(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	exams, err := s.exam.ListExams(r.Context(), current.ID, current.Role == auth.RoleAdmin, r.URL.Query().Get("status"))
	if err != nil {
		s.handleExamError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"exams": exams})
}

// handleGetExam 考试详情（教师侧，含条目快照与答案）。
func (s *Server) handleGetExam(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	examID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "考试 ID 非法")
		return
	}
	e, err := s.exam.GetExam(r.Context(), current.ID, current.Role == auth.RoleAdmin, examID)
	if err != nil {
		s.handleExamError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"exam": e})
}

// handleStartAttempt 学生开始考试（幂等，不重置计时）。
func (s *Server) handleStartAttempt(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	examID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "考试 ID 非法")
		return
	}
	attempt, err := s.exam.StartAttempt(r.Context(), current.ID, examID)
	if err != nil {
		s.handleExamError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"attempt": attempt})
}

// handleGetAttempt 学生恢复作答（已保存答案 + 原始截止时间）。
func (s *Server) handleGetAttempt(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	examID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "考试 ID 非法")
		return
	}
	attempt, err := s.exam.GetAttempt(r.Context(), current.ID, examID)
	if err != nil {
		s.handleExamError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"attempt": attempt})
}

// handleSaveAnswers 学生自动保存（版本冲突控制）。
func (s *Server) handleSaveAnswers(w http.ResponseWriter, r *http.Request) {
	current := auth.UserFromContext(r.Context())
	examID, ok := pathID(r, "id")
	if !ok {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "考试 ID 非法")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var in exam.SaveAnswersInput
	if err := jsonDecode(r.Body, &in); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "bad_request", "请求体必须是 JSON")
		return
	}
	revision, savedAt, err := s.exam.SaveAttemptAnswers(r.Context(), current.ID, examID, in)
	if err != nil {
		s.handleExamError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"answer_revision": revision,
		"saved_at":        savedAt,
	})
}
