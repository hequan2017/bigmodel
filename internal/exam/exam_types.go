package exam

import (
	"errors"
	"time"
)

// 考试状态。
const (
	ExamStatusDraft     = "draft"
	ExamStatusPublished = "published"
	ExamStatusClosed    = "closed"
	ExamStatusDisabled  = "disabled"
)

// 服务级错误（考试部分），由 handler 层映射为稳定错误码。
var (
	ErrExamDuplicate    = errors.New("同名考试已存在")
	ErrExamNotDraft     = errors.New("已发布考试不可修改，如需调整请另建一场")
	ErrExamInvalidTimes = errors.New("时间窗口非法：close_at 必须晚于 open_at")
	ErrExamMissingTimes = errors.New("发布前必须设置开放时间、截止时间与作答时长")
	ErrExamNoItems      = errors.New("发布前至少需要一道试题")
	ErrExamNoTotalScore = errors.New("发布前试卷总分必须大于 0")
	ErrExamItemInvalid  = errors.New("试卷条目不合法")
)

// Exam 是考试的教师侧视图（含试卷条目快照与标准答案；学生接口不得返回答案）。
type Exam struct {
	ID              int64      `json:"id"`
	OwnerID         int64      `json:"owner_id"`
	Title           string     `json:"title"`
	LevelCode       string     `json:"level_code"`
	Status          string     `json:"status"`
	OpenAt          *time.Time `json:"open_at,omitempty"`
	CloseAt         *time.Time `json:"close_at,omitempty"`
	DurationSeconds *int       `json:"duration_seconds,omitempty"`
	PublishedAt     *time.Time `json:"published_at,omitempty"`
	TotalScore      int        `json:"total_score"`
	Items           []ExamItem `json:"items,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// ExamItem 是试卷条目：题面/选项/答案均为发布时的快照（§14.2）。
type ExamItem struct {
	ID               int64    `json:"id"`
	ExamID           int64    `json:"exam_id"`
	SourceQuestionID int64    `json:"source_question_id"`
	OrderNo          int      `json:"order_no"`
	Type             string   `json:"type"`
	Stem             string   `json:"stem"`
	Options          []Option `json:"options,omitempty"`
	CorrectAnswer    string   `json:"correct_answer"`
	Score            int      `json:"score"`
}

// ExamInput 是创建/更新考试元信息的请求体；时间为 RFC3339 字符串。
type ExamInput struct {
	Title           string  `json:"title"`
	LevelCode       string  `json:"level_code"`
	OpenAt          *string `json:"open_at"`
	CloseAt         *string `json:"close_at"`
	DurationSeconds *int    `json:"duration_seconds"`
}

// ExamItemInput 是设置试卷条目的请求项。
type ExamItemInput struct {
	SourceQuestionID int64 `json:"source_question_id"`
	OrderNo          int   `json:"order_no"`
	Score            int   `json:"score"`
}
