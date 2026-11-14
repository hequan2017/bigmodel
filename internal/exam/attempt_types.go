package exam

import (
	"errors"
	"time"
)

// 作答状态与提交原因。
const (
	AttemptInProgress = "in_progress"
	AttemptSubmitted  = "submitted"

	SubmitReasonManual  = "manual"
	SubmitReasonTimeout = "timeout"
)

// 作答服务级错误。
var (
	ErrAttemptNotFound  = errors.New("作答不存在或尚未开始")
	ErrExamNotOpen      = errors.New("考试尚未开放")
	ErrExamClosed       = errors.New("考试已截止")
	ErrAttemptExpired   = errors.New("作答已超过截止时间")
	ErrAttemptSubmitted = errors.New("试卷已提交，不能再保存答案")
	ErrRevisionConflict = errors.New("答案版本冲突，请先拉取最新状态再重试")
	ErrAnswerInvalid    = errors.New("答案不合法")
)

// StudentAttempt 是学生作答视图：绝不包含标准答案（§14.3）。
type StudentAttempt struct {
	ExamID         int64             `json:"exam_id"`
	Title          string            `json:"title"`
	LevelCode      string            `json:"level_code"`
	Status         string            `json:"status"`
	StartedAt      time.Time         `json:"started_at"`
	DeadlineAt     time.Time         `json:"deadline_at"`
	ServerNow      time.Time         `json:"server_now"`
	AnswerRevision int               `json:"answer_revision"`
	TotalScore     *int              `json:"total_score,omitempty"`
	SubmittedAt    *time.Time        `json:"submitted_at,omitempty"`
	Items          []StudentItem     `json:"items"`
	SavedAnswers   map[string]string `json:"saved_answers"`
}

// StudentItem 是学生可见的试卷条目：仅题面/选项/顺序/分值。
type StudentItem struct {
	ID      int64    `json:"item_id"`
	OrderNo int      `json:"order_no"`
	Type    string   `json:"type"`
	Stem    string   `json:"stem"`
	Options []Option `json:"options,omitempty"`
	Score   int      `json:"score"`
}

// AnswerInput 是单题答案。
type AnswerInput struct {
	ItemID int64  `json:"item_id"`
	Answer string `json:"answer"`
}

// SaveAnswersInput 是自动保存请求体（§14.5）。
type SaveAnswersInput struct {
	ExpectedRevision int           `json:"expected_revision"`
	Answers          []AnswerInput `json:"answers"`
}
