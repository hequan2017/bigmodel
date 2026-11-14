package exam

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SubmitAttempt 主动交卷（§14.6）：幂等；过期忽略版本差异做 timeout 结算；
// 有效期内版本不一致返回 409；判分使用试卷快照，不访问外部服务。
func (s *Service) SubmitAttempt(ctx context.Context, userID, examID int64, expectedRevision int) (*AttemptResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var attemptID int64
	var status string
	var revision int
	var within bool
	err = tx.QueryRow(ctx, `
		SELECT id, status, answer_revision, (now() < deadline_at)
		FROM exam_attempts
		WHERE exam_id = $1 AND user_id = $2
		FOR UPDATE`, examID, userID,
	).Scan(&attemptID, &status, &revision, &within)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAttemptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("锁定作答: %w", err)
	}

	// 已提交：幂等返回原有结果（§14.6 第 3 步）。
	if status == AttemptSubmitted {
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return s.AttemptResult(ctx, userID, examID)
	}

	reason := SubmitReasonManual
	if !within {
		// 已到期：忽略新答案与版本差异，按已保存答案做 timeout 结算。
		reason = SubmitReasonTimeout
	} else if revision != expectedRevision {
		return nil, ErrRevisionConflict
	}

	if err := gradeAndSubmit(ctx, tx, attemptID, reason); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("提交交卷: %w", err)
	}
	return s.AttemptResult(ctx, userID, examID)
}

// gradeAndSubmit 用试卷快照判分并写入终态；同一事务内完成（§14.6 第 6-7 步）。
// 判分不访问外部服务；单选/判断精确比较（规范化后），未作答 0 分。
func gradeAndSubmit(ctx context.Context, tx pgx.Tx, attemptID int64, reason string) error {
	itemRows, err := tx.Query(ctx, `
		SELECT id, correct_answer, score FROM exam_items ei
		WHERE ei.exam_id = (SELECT exam_id FROM exam_attempts WHERE id = $1)`, attemptID)
	if err != nil {
		return fmt.Errorf("查询试卷快照: %w", err)
	}
	type scoredItem struct {
		id     int64
		answer string
		score  int
	}
	items := []scoredItem{}
	for itemRows.Next() {
		var it scoredItem
		if err := itemRows.Scan(&it.id, &it.answer, &it.score); err != nil {
			itemRows.Close()
			return fmt.Errorf("读取试卷快照: %w", err)
		}
		items = append(items, it)
	}
	if err := itemRows.Err(); err != nil {
		itemRows.Close()
		return err
	}
	itemRows.Close()

	answers := map[int64]string{}
	answerRows, err := tx.Query(ctx, `
		SELECT exam_item_id, answer FROM exam_answers WHERE attempt_id = $1`, attemptID)
	if err != nil {
		return fmt.Errorf("查询已保存答案: %w", err)
	}
	for answerRows.Next() {
		var itemID int64
		var answer string
		if err := answerRows.Scan(&itemID, &answer); err != nil {
			answerRows.Close()
			return fmt.Errorf("读取答案: %w", err)
		}
		answers[itemID] = answer
	}
	if err := answerRows.Err(); err != nil {
		answerRows.Close()
		return err
	}
	answerRows.Close()

	total := 0
	for _, it := range items {
		awarded := 0
		if ans, ok := answers[it.id]; ok &&
			strings.EqualFold(strings.TrimSpace(ans), strings.TrimSpace(it.answer)) {
			awarded = it.score
		}
		total += awarded
		if _, err := tx.Exec(ctx, `
			UPDATE exam_answers SET awarded_score = $2
			WHERE attempt_id = $1 AND exam_item_id = $3`, attemptID, awarded, it.id); err != nil {
			return fmt.Errorf("写入逐题得分: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE exam_attempts SET status = 'submitted', submitted_at = now(),
		       submit_reason = $2, total_score = $3
		WHERE id = $1 AND status = 'in_progress'`, attemptID, reason, total); err != nil {
		return fmt.Errorf("写入交卷状态: %w", err)
	}
	return nil
}

// SweepExpiredAttempts 到期自动结算（§14.7）：扫描已到期未提交作答并逐个结算。
// 只负责找到记录，最终仍调用同一判分逻辑；重启后继续扫描数据库，不依赖进程内计时器。
func (s *Service) SweepExpiredAttempts(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM exam_attempts
		WHERE status = 'in_progress' AND deadline_at <= now()
		ORDER BY id LIMIT 200`)
	if err != nil {
		return 0, fmt.Errorf("扫描到期作答: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	settled := 0
	for _, id := range ids {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return settled, err
		}
		var status string
		var expired bool
		err = tx.QueryRow(ctx, `
			SELECT status, (deadline_at <= now()) FROM exam_attempts
			WHERE id = $1 FOR UPDATE`, id,
		).Scan(&status, &expired)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			continue
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return settled, err
		}
		// 与主动交卷并发时靠同一行锁与终态检查只结算一次。
		if status != AttemptInProgress || !expired {
			_ = tx.Rollback(ctx)
			continue
		}
		if err := gradeAndSubmit(ctx, tx, id, SubmitReasonTimeout); err != nil {
			_ = tx.Rollback(ctx)
			return settled, err
		}
		if err := tx.Commit(ctx); err != nil {
			return settled, err
		}
		settled++
	}
	return settled, nil
}

// AttemptResult 学生成绩视图（§14.8）：仅总分与提交状态。
func (s *Service) AttemptResult(ctx context.Context, userID, examID int64) (*AttemptResult, error) {
	var r AttemptResult
	var submittedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT exam_id, status, total_score, submitted_at, COALESCE(submit_reason, '')
		FROM exam_attempts WHERE exam_id = $1 AND user_id = $2`, examID, userID,
	).Scan(&r.ExamID, &r.Status, &r.TotalScore, &submittedAt, &r.SubmitReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAttemptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询成绩: %w", err)
	}
	r.SubmittedAt = submittedAt
	if err := s.pool.QueryRow(ctx, `SELECT now()`).Scan(&r.ServerNow); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListExamResults 教师成绩列表：仅本人考试（§14.8）。
func (s *Service) ListExamResults(ctx context.Context, userID int64, isAdmin bool, examID int64) ([]TeacherAttemptRow, error) {
	e, err := s.checkExamAccess(ctx, userID, isAdmin, examID)
	if err != nil {
		return nil, err
	}
	_ = e
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.user_id, u.login_name, a.status, a.total_score,
		       a.answer_revision, a.submitted_at, COALESCE(a.submit_reason, '')
		FROM exam_attempts a JOIN users u ON u.id = a.user_id
		WHERE a.exam_id = $1 ORDER BY a.id`, examID)
	if err != nil {
		return nil, fmt.Errorf("查询成绩列表: %w", err)
	}
	defer rows.Close()

	out := []TeacherAttemptRow{}
	for rows.Next() {
		var r TeacherAttemptRow
		if err := rows.Scan(&r.AttemptID, &r.UserID, &r.LoginName, &r.Status, &r.TotalScore,
			&r.AnswerRevision, &r.SubmittedAt, &r.SubmitReason); err != nil {
			return nil, fmt.Errorf("读取成绩: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
