package exam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// StartAttempt 开始考试（幂等）：校验考试已发布且当前时间在开放窗口内，
// deadline_at = min(now+时长, close_at) 全部以数据库时间为准（§14.4）。
// 重复开始通过唯一约束返回原有作答，不重置计时。
func (s *Service) StartAttempt(ctx context.Context, userID, examID int64) (*StudentAttempt, error) {
	var title, levelCode, status string
	var openAt, closeAt *time.Time
	var duration *int
	err := s.pool.QueryRow(ctx, `
		SELECT title, level_code, status, open_at, close_at, duration_seconds
		FROM exams WHERE id = $1`, examID,
	).Scan(&title, &levelCode, &status, &openAt, &closeAt, &duration)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound // 未发布/不存在统一 404 防枚举
	}
	if err != nil {
		return nil, fmt.Errorf("查询考试: %w", err)
	}
	if status != ExamStatusPublished {
		return nil, ErrNotFound
	}
	if openAt == nil || closeAt == nil || duration == nil {
		return nil, ErrExamMissingTimes
	}

	var dbNow time.Time
	if err := s.pool.QueryRow(ctx, `SELECT now()`).Scan(&dbNow); err != nil {
		return nil, fmt.Errorf("查询数据库时间: %w", err)
	}
	if dbNow.Before(*openAt) {
		return nil, ErrExamNotOpen
	}
	if dbNow.After(*closeAt) {
		return nil, ErrExamClosed
	}

	// 原子开始：唯一约束保证每人每场一次，冲突时不重置计时。
	var attemptID int64
	var deadline time.Time
	var revision int
	err = s.pool.QueryRow(ctx, `
		INSERT INTO exam_attempts (exam_id, user_id, deadline_at)
		VALUES ($1, $2, LEAST(now() + make_interval(secs => $3), $4))
		ON CONFLICT (exam_id, user_id) DO NOTHING
		RETURNING id, deadline_at, answer_revision`,
		examID, userID, *duration, closeAt,
	).Scan(&attemptID, &deadline, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		// 已存在作答：返回原有记录。
		err = s.pool.QueryRow(ctx, `
			SELECT id, deadline_at, answer_revision FROM exam_attempts
			WHERE exam_id = $1 AND user_id = $2`, examID, userID,
		).Scan(&attemptID, &deadline, &revision)
		if err != nil {
			return nil, fmt.Errorf("查询已有作答: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("开始作答: %w", err)
	}

	return s.buildStudentAttempt(ctx, userID, examID, title, levelCode, attemptID, deadline, revision)
}

// GetAttempt 恢复作答：返回已保存答案与原始截止时间（§15.2）。
// 查询作答同时触发到期检查（§14.7）：已到期的作答先做 timeout 结算。
func (s *Service) GetAttempt(ctx context.Context, userID, examID int64) (*StudentAttempt, error) {
	if _, serr := s.SweepExpiredAttempts(ctx); serr != nil {
		// 结算失败不阻断恢复视图。
		_ = serr
	}
	var attemptID int64
	var title, levelCode string
	var deadline time.Time
	var revision int
	err := s.pool.QueryRow(ctx, `
		SELECT a.id, a.deadline_at, a.answer_revision, e.title, e.level_code
		FROM exam_attempts a JOIN exams e ON e.id = a.exam_id
		WHERE a.exam_id = $1 AND a.user_id = $2`, examID, userID,
	).Scan(&attemptID, &deadline, &revision, &title, &levelCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAttemptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询作答: %w", err)
	}
	return s.buildStudentAttempt(ctx, userID, examID, title, levelCode, attemptID, deadline, revision)
}

// buildStudentAttempt 组装学生视图：试卷条目无答案 + 已保存答案 + 服务器时间。
func (s *Service) buildStudentAttempt(ctx context.Context, userID, examID int64,
	title, levelCode string, attemptID int64, deadline time.Time, revision int,
) (*StudentAttempt, error) {
	var status string
	var startedAt time.Time
	var submittedAt *time.Time
	var totalScore *int
	var closeAt time.Time
	if err := s.pool.QueryRow(ctx, `
		SELECT a.status, a.started_at, a.submitted_at, a.total_score, e.close_at
		FROM exam_attempts a JOIN exams e ON e.id = a.exam_id
		WHERE a.id = $1`, attemptID,
	).Scan(&status, &startedAt, &submittedAt, &totalScore, &closeAt); err != nil {
		return nil, fmt.Errorf("查询作答状态: %w", err)
	}

	// 试卷条目：来自发布快照，剥离标准答案。
	items := []StudentItem{}
	snapshots, err := s.loadItems(ctx, examID)
	if err != nil {
		return nil, err
	}
	for _, it := range snapshots {
		opts := it.Options // 选项本身不涉答案
		items = append(items, StudentItem{
			ID: it.ID, OrderNo: it.OrderNo, Type: it.Type, Stem: it.Stem,
			Options: opts, Score: it.Score,
		})
	}

	// 已保存答案。
	saved := map[string]string{}
	rows, err := s.pool.Query(ctx, `
		SELECT exam_item_id, answer FROM exam_answers WHERE attempt_id = $1`, attemptID)
	if err != nil {
		return nil, fmt.Errorf("查询已保存答案: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var itemID int64
		var answer string
		if err := rows.Scan(&itemID, &answer); err != nil {
			return nil, fmt.Errorf("读取已保存答案: %w", err)
		}
		saved[fmt.Sprintf("%d", itemID)] = answer
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var serverNow time.Time
	if err := s.pool.QueryRow(ctx, `SELECT now()`).Scan(&serverNow); err != nil {
		return nil, fmt.Errorf("查询数据库时间: %w", err)
	}

	return &StudentAttempt{
		ExamID:         examID,
		Title:          title,
		LevelCode:      levelCode,
		Status:         status,
		StartedAt:      startedAt,
		DeadlineAt:     deadline,
		CloseAt:        closeAt,
		ServerNow:      serverNow,
		AnswerRevision: revision,
		TotalScore:     totalScore,
		SubmittedAt:    submittedAt,
		Items:          items,
		SavedAnswers:   saved,
	}, nil
}

// SaveAttemptAnswers 自动保存（§14.5）：事务 + 行锁 + 数据库时间截止检查 +
// 版本冲突控制；校验条目归属与答案合法性后整体替换快照。
func (s *Service) SaveAttemptAnswers(ctx context.Context, userID, examID int64, in SaveAnswersInput) (int, time.Time, error) {
	var zeroTime time.Time
	if in.ExpectedRevision <= 0 {
		return 0, zeroTime, fmt.Errorf("%w: expected_revision 非法", ErrAnswerInvalid)
	}

	answerMap := map[int64]string{}
	for _, a := range in.Answers {
		a.Answer = strings.TrimSpace(a.Answer)
		if a.ItemID <= 0 || a.Answer == "" {
			return 0, zeroTime, fmt.Errorf("%w: item_id/answer 不能为空", ErrAnswerInvalid)
		}
		if _, dup := answerMap[a.ItemID]; dup {
			return 0, zeroTime, fmt.Errorf("%w: item_id %d 重复", ErrAnswerInvalid, a.ItemID)
		}
		answerMap[a.ItemID] = strings.ToLower(a.Answer)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, zeroTime, fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 行锁内读取，并以数据库时刻做截止检查（§14.4：不要把等锁前的时间当判定时间）。
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
		return 0, zeroTime, ErrAttemptNotFound
	}
	if err != nil {
		return 0, zeroTime, fmt.Errorf("锁定作答: %w", err)
	}
	if !within {
		return 0, zeroTime, ErrAttemptExpired
	}
	if status != AttemptInProgress {
		return 0, zeroTime, ErrAttemptSubmitted
	}
	if revision != in.ExpectedRevision {
		return 0, zeroTime, ErrRevisionConflict
	}

	// 校验答案：条目必须属于本卷，答案符合题型。
	rows, err := tx.Query(ctx, `
		SELECT id, type, options FROM exam_items WHERE exam_id = $1`, examID)
	if err != nil {
		return 0, zeroTime, fmt.Errorf("查询试卷条目: %w", err)
	}
	itemTypes := map[int64]string{}
	itemKeys := map[int64]map[string]bool{}
	for rows.Next() {
		var itemID int64
		var itemType string
		var options []byte
		if err := rows.Scan(&itemID, &itemType, &options); err != nil {
			rows.Close()
			return 0, zeroTime, fmt.Errorf("读取试卷条目: %w", err)
		}
		itemTypes[itemID] = itemType
		if itemType == TypeSingle && len(options) > 0 {
			var opts []Option
			if jsonErr := json.Unmarshal(options, &opts); jsonErr == nil {
				keys := map[string]bool{}
				for _, o := range opts {
					keys[o.Key] = true
				}
				itemKeys[itemID] = keys
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, zeroTime, err
	}
	rows.Close()

	for itemID, answer := range answerMap {
		itemType, ok := itemTypes[itemID]
		if !ok {
			return 0, zeroTime, fmt.Errorf("%w: 条目 %d 不属于本试卷", ErrAnswerInvalid, itemID)
		}
		switch itemType {
		case TypeSingle:
			keys, ok := itemKeys[itemID]
			if !ok || !keys[strings.ToUpper(answer)] {
				return 0, zeroTime, fmt.Errorf("%w: 条目 %d 答案不是有效选项键", ErrAnswerInvalid, itemID)
			}
			answerMap[itemID] = strings.ToUpper(answer)
		case TypeJudge:
			if answer != "true" && answer != "false" {
				return 0, zeroTime, fmt.Errorf("%w: 条目 %d 答案只能为 true/false", ErrAnswerInvalid, itemID)
			}
		default:
			return 0, zeroTime, fmt.Errorf("%w: 条目 %d 题型非法", ErrAnswerInvalid, itemID)
		}
	}

	// 整体替换答案快照；快照未包含的题目视为未作答。
	if _, err := tx.Exec(ctx, `DELETE FROM exam_answers WHERE attempt_id = $1`, attemptID); err != nil {
		return 0, zeroTime, fmt.Errorf("清理旧答案: %w", err)
	}
	for itemID, answer := range answerMap {
		if _, err := tx.Exec(ctx, `
			INSERT INTO exam_answers (attempt_id, exam_item_id, answer)
			VALUES ($1, $2, $3)`, attemptID, itemID, answer,
		); err != nil {
			return 0, zeroTime, fmt.Errorf("保存答案: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE exam_attempts SET answer_revision = answer_revision + 1 WHERE id = $1`, attemptID); err != nil {
		return 0, zeroTime, fmt.Errorf("递增版本: %w", err)
	}

	var newRevision int
	var savedAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT answer_revision, now() FROM exam_attempts WHERE id = $1`, attemptID,
	).Scan(&newRevision, &savedAt); err != nil {
		return 0, zeroTime, fmt.Errorf("查询新版本: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, zeroTime, fmt.Errorf("提交保存: %w", err)
	}
	return newRevision, savedAt, nil
}
