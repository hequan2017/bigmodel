// Package exam 提供等级考试后台：题库、组卷、作答与判分。
package exam

import (
	"errors"
	"time"
)

// 题型与状态。
const (
	TypeSingle = "single"
	TypeJudge  = "judge"

	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// 服务级错误，由 handler 层映射为稳定错误码。
var (
	ErrNotFound      = errors.New("题目不存在")
	ErrInvalidData   = errors.New("题目数据不合法")
	ErrInvalidState  = errors.New("当前状态不允许该操作")
	ErrInvalidID     = errors.New("ID 非法")
	ErrDuplicateStem = errors.New("同教师下存在相同题干的题目")
)

// Option 是单选题选项。
type Option struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

// Question 是题库题目的教师侧视图（含标准答案；学生接口不得返回）。
type Question struct {
	ID            int64     `json:"id"`
	OwnerID       int64     `json:"owner_id"`
	Type          string    `json:"type"`
	Stem          string    `json:"stem"`
	Options       []Option  `json:"options,omitempty"`
	CorrectAnswer string    `json:"correct_answer"`
	DefaultScore  int       `json:"default_score"`
	LevelCode     string    `json:"level_code"`
	Tags          []string  `json:"tags,omitempty"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// QuestionInput 是创建/更新题目请求体。
type QuestionInput struct {
	Type          string   `json:"type"`
	Stem          string   `json:"stem"`
	Options       []Option `json:"options"`
	CorrectAnswer string   `json:"correct_answer"`
	DefaultScore  int      `json:"default_score"`
	LevelCode     string   `json:"level_code"`
	Tags          []string `json:"tags"`
}
