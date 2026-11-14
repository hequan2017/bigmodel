package exam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AttemptDetail 是教师查看的学生作答详情（§14.8 教师侧）：
// 逐题展示学生答案、正确答案与得分；仅考试所有者与管理员可查。
type AttemptDetail struct {
	AttemptID    int64             `json:"attempt_id"`
	ExamID       int64             `json:"exam_id"`
	LoginName    string            `json:"login_name"`
	Status       string            `json:"status"`
	TotalScore   *int              `json:"total_score,omitempty"`
	SubmittedAt  *time.Time        `json:"submitted_at,omitempty"`
	SubmitReason string            `json:"submit_reason,omitempty"`
	AnswerRev    int               `json:"answer_revision"`
	Items        []AttemptDetailIt `json:"items"`
}

// AttemptDetailIt 是逐题明细。
type AttemptDetailIt struct {
	OrderNo       int      `json:"order_no"`
	Type          string   `json:"type"`
	Stem          string   `json:"stem"`
	Options       []Option `json:"options,omitempty"`
	StudentAnswer string   `json:"student_answer"`
	CorrectAnswer string   `json:"correct_answer"`
	Awarded       int      `json:"awarded"`
	FullScore     int      `json:"full_score"`
}

// GetAttemptDetail 查询某次作答的逐题明细。
// 授权：仅考试所有者与管理员；作答必须属于该考试。
func (s *Service) GetAttemptDetail(ctx context.Context, userID int64, isAdmin bool, examID, attemptID int64) (*AttemptDetail, error) {
	if _, err := s.checkExamAccess(ctx, userID, isAdmin, examID); err != nil {
		return nil, err
	}

	var loginName, status string
	var totalScore *int
	var submittedAt *time.Time
	var reason string
	var revision int
	err := s.pool.QueryRow(ctx, `
		SELECT u.login_name, a.status, a.total_score, a.submitted_at,
		       COALESCE(a.submit_reason, ''), a.answer_revision
		FROM exam_attempts a JOIN users u ON u.id = a.user_id
		WHERE a.id = $1 AND a.exam_id = $2`, attemptID, examID,
	).Scan(&loginName, &status, &totalScore, &submittedAt, &reason, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询作答: %w", err)
	}

	// 试卷快照。
	itemRows, err := s.pool.Query(ctx, `
		SELECT order_no, type, stem, options, correct_answer, score
		FROM exam_items WHERE exam_id = $1 ORDER BY order_no`, examID)
	if err != nil {
		return nil, fmt.Errorf("查询试卷快照: %w", err)
	}
	type snap struct {
		orderNo int
		typ     string
		stem    string
		options []byte
		correct string
		score   int
	}
	snaps := []snap{}
	for itemRows.Next() {
		var it snap
		if err := itemRows.Scan(&it.orderNo, &it.typ, &it.stem, &it.options, &it.correct, &it.score); err != nil {
			itemRows.Close()
			return nil, fmt.Errorf("读取试卷快照: %w", err)
		}
		snaps = append(snaps, it)
	}
	if err := itemRows.Err(); err != nil {
		itemRows.Close()
		return nil, err
	}
	itemRows.Close()

	// 学生答案与得分。
	answers := map[int64]struct {
		answer  string
		awarded int
	}{}
	answerRows, err := s.pool.Query(ctx, `
		SELECT exam_item_id, answer, awarded_score FROM exam_answers WHERE attempt_id = $1`, attemptID)
	if err != nil {
		return nil, fmt.Errorf("查询学生答案: %w", err)
	}
	for answerRows.Next() {
		var itemID int64
		var answer string
		var awarded int
		if err := answerRows.Scan(&itemID, &answer, &awarded); err != nil {
			answerRows.Close()
			return nil, fmt.Errorf("读取学生答案: %w", err)
		}
		answers[itemID] = struct {
			answer  string
			awarded int
		}{answer, awarded}
	}
	if err := answerRows.Err(); err != nil {
		answerRows.Close()
		return nil, err
	}
	answerRows.Close()

	// 快照条目 ID（用于关联学生答案）。
	idRows, err := s.pool.Query(ctx, `
		SELECT id, order_no FROM exam_items WHERE exam_id = $1 ORDER BY order_no`, examID)
	if err != nil {
		return nil, fmt.Errorf("查询条目 ID: %w", err)
	}
	idByOrder := map[int]int64{}
	for idRows.Next() {
		var id int64
		var orderNo int
		if err := idRows.Scan(&id, &orderNo); err != nil {
			idRows.Close()
			return nil, err
		}
		idByOrder[orderNo] = id
	}
	if err := idRows.Err(); err != nil {
		idRows.Close()
		return nil, err
	}
	idRows.Close()

	items := make([]AttemptDetailIt, 0, len(snaps))
	for _, sn := range snaps {
		var opts []Option
		if len(sn.options) > 0 {
			if err := json.Unmarshal(sn.options, &opts); err != nil {
				return nil, fmt.Errorf("解析选项: %w", err)
			}
		}
		studentAnswer := "未作答"
		awarded := 0
		if a, ok := answers[idByOrder[sn.orderNo]]; ok && a.answer != "" {
			studentAnswer = a.answer
			awarded = a.awarded
		}
		items = append(items, AttemptDetailIt{
			OrderNo:       sn.orderNo,
			Type:          sn.typ,
			Stem:          sn.stem,
			Options:       opts,
			StudentAnswer: studentAnswer,
			CorrectAnswer: sn.correct,
			Awarded:       awarded,
			FullScore:     sn.score,
		})
	}

	return &AttemptDetail{
		AttemptID:    attemptID,
		ExamID:       examID,
		LoginName:    loginName,
		Status:       status,
		TotalScore:   totalScore,
		SubmittedAt:  submittedAt,
		SubmitReason: reason,
		AnswerRev:    revision,
		Items:        items,
	}, nil
}
