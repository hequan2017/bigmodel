package exam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxTitleRunes = 200

// parseExamInput 解析并校验考试元信息；草稿期允许时间字段缺省。
func parseExamInput(in ExamInput) (title, levelCode string, openAt, closeAt *time.Time, duration *int, err error) {
	title = strings.TrimSpace(in.Title)
	if title == "" {
		return "", "", nil, nil, nil, fmt.Errorf("%w: title 不能为空", ErrInvalidData)
	}
	if len([]rune(title)) > maxTitleRunes {
		return "", "", nil, nil, nil, fmt.Errorf("%w: title 过长", ErrInvalidData)
	}
	levelCode = strings.TrimSpace(in.LevelCode)

	parse := func(s *string, field string) (*time.Time, error) {
		if s == nil || strings.TrimSpace(*s) == "" {
			return nil, nil
		}
		t, perr := time.Parse(time.RFC3339, strings.TrimSpace(*s))
		if perr != nil {
			return nil, fmt.Errorf("%w: %s 不是合法的 RFC3339 时间", ErrInvalidData, field)
		}
		return &t, nil
	}
	if openAt, err = parse(in.OpenAt, "open_at"); err != nil {
		return "", "", nil, nil, nil, err
	}
	if closeAt, err = parse(in.CloseAt, "close_at"); err != nil {
		return "", "", nil, nil, nil, err
	}
	if openAt != nil && closeAt != nil && !closeAt.After(*openAt) {
		return "", "", nil, nil, nil, ErrExamInvalidTimes
	}
	if in.DurationSeconds != nil {
		if *in.DurationSeconds <= 0 {
			return "", "", nil, nil, nil, fmt.Errorf("%w: duration_seconds 必须为正整数", ErrInvalidData)
		}
		d := *in.DurationSeconds
		duration = &d
	}
	return title, levelCode, openAt, closeAt, duration, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// checkExamAccess 校验考试存在且可管理；越权统一 404。
func (s *Service) checkExamAccess(ctx context.Context, userID int64, isAdmin bool, examID int64) (*examRow, error) {
	e, err := s.getExamRow(ctx, examID)
	if err != nil {
		return nil, err
	}
	if !isAdmin && e.OwnerID != userID {
		return nil, ErrNotFound
	}
	return e, nil
}

type examRow struct {
	ID, OwnerID                  int64
	Title, LevelCode, Status     string
	OpenAt, CloseAt, PublishedAt *time.Time
	DurationSeconds              *int
	CreatedAt, UpdatedAt         time.Time
}

const examColumns = `id, owner_id, title, level_code, status, open_at, close_at, duration_seconds, published_at, created_at, updated_at`

func (s *Service) getExamRow(ctx context.Context, examID int64) (*examRow, error) {
	var r examRow
	err := s.pool.QueryRow(ctx,
		`SELECT `+examColumns+` FROM exams WHERE id = $1`, examID,
	).Scan(&r.ID, &r.OwnerID, &r.Title, &r.LevelCode, &r.Status, &r.OpenAt, &r.CloseAt,
		&r.DurationSeconds, &r.PublishedAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询考试: %w", err)
	}
	return &r, nil
}

func (s *Service) loadItems(ctx context.Context, examID int64) ([]ExamItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, exam_id, source_question_id, order_no, type, stem, options, correct_answer, score
		FROM exam_items WHERE exam_id = $1 ORDER BY order_no`, examID)
	if err != nil {
		return nil, fmt.Errorf("查询试卷条目: %w", err)
	}
	defer rows.Close()

	out := []ExamItem{}
	for rows.Next() {
		var it ExamItem
		var options []byte
		if err := rows.Scan(&it.ID, &it.ExamID, &it.SourceQuestionID, &it.OrderNo, &it.Type,
			&it.Stem, &options, &it.CorrectAnswer, &it.Score); err != nil {
			return nil, fmt.Errorf("读取试卷条目: %w", err)
		}
		if len(options) > 0 {
			if err := json.Unmarshal(options, &it.Options); err != nil {
				return nil, fmt.Errorf("解析条目选项: %w", err)
			}
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func toExam(r *examRow, items []ExamItem) *Exam {
	total := 0
	for _, it := range items {
		total += it.Score
	}
	return &Exam{
		ID: r.ID, OwnerID: r.OwnerID, Title: r.Title, LevelCode: r.LevelCode, Status: r.Status,
		OpenAt: r.OpenAt, CloseAt: r.CloseAt, DurationSeconds: r.DurationSeconds,
		PublishedAt: r.PublishedAt, TotalScore: total, Items: items,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// CreateExam 创建考试草稿。
func (s *Service) CreateExam(ctx context.Context, ownerID int64, in ExamInput) (*Exam, error) {
	title, levelCode, openAt, closeAt, duration, err := parseExamInput(in)
	if err != nil {
		return nil, err
	}
	e := &examRow{OwnerID: ownerID, Title: title, LevelCode: levelCode, Status: ExamStatusDraft,
		OpenAt: openAt, CloseAt: closeAt, DurationSeconds: duration}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO exams (owner_id, title, level_code, open_at, close_at, duration_seconds)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`,
		ownerID, title, levelCode, openAt, closeAt, duration,
	).Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrExamDuplicate
		}
		return nil, fmt.Errorf("创建考试: %w", err)
	}
	return toExam(e, nil), nil
}

// UpdateExam 更新考试元信息；仅草稿可改。
func (s *Service) UpdateExam(ctx context.Context, userID int64, isAdmin bool, examID int64, in ExamInput) (*Exam, error) {
	if _, err := s.checkExamAccess(ctx, userID, isAdmin, examID); err != nil {
		return nil, err
	}
	title, levelCode, openAt, closeAt, duration, err := parseExamInput(in)
	if err != nil {
		return nil, err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE exams SET title = $2, level_code = $3, open_at = $4, close_at = $5,
		       duration_seconds = $6, updated_at = now()
		WHERE id = $1 AND status = 'draft'`, examID, title, levelCode, openAt, closeAt, duration)
	if err != nil {
		return nil, fmt.Errorf("更新考试: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrExamNotDraft
	}
	r, err := s.getExamRow(ctx, examID)
	if err != nil {
		return nil, err
	}
	items, err := s.loadItems(ctx, examID)
	if err != nil {
		return nil, err
	}
	return toExam(r, items), nil
}

// SetExamItems 整体替换草稿试卷条目，并从题库生成题面/选项/答案快照（§14.3）。
func (s *Service) SetExamItems(ctx context.Context, userID int64, isAdmin bool, examID int64, inputs []ExamItemInput) ([]ExamItem, error) {
	e, err := s.checkExamAccess(ctx, userID, isAdmin, examID)
	if err != nil {
		return nil, err
	}
	if e.Status != ExamStatusDraft {
		return nil, ErrExamNotDraft
	}
	if len(inputs) > 100 {
		return nil, fmt.Errorf("%w: 试卷最多 100 题", ErrExamItemInvalid)
	}

	seenOrder := map[int]bool{}
	for _, it := range inputs {
		if it.SourceQuestionID <= 0 {
			return nil, fmt.Errorf("%w: source_question_id 非法", ErrExamItemInvalid)
		}
		if it.OrderNo <= 0 || seenOrder[it.OrderNo] {
			return nil, fmt.Errorf("%w: order_no 必须为正且唯一", ErrExamItemInvalid)
		}
		seenOrder[it.OrderNo] = true
		if it.Score < 0 {
			return nil, fmt.Errorf("%w: score 必须为非负整数", ErrExamItemInvalid)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 逐题校验归属与状态并生成快照。
	items := make([]ExamItem, 0, len(inputs))
	for _, in := range inputs {
		rows := tx.QueryRow(ctx, `
			SELECT id, owner_id, type, stem, options, correct_answer, status
			FROM exam_questions WHERE id = $1`, in.SourceQuestionID)
		var sq Question
		var sqOptions []byte
		if err := rows.Scan(&sq.ID, &sq.OwnerID, &sq.Type, &sq.Stem, &sqOptions,
			&sq.CorrectAnswer, &sq.Status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("%w: 题目 %d 不存在", ErrExamItemInvalid, in.SourceQuestionID)
			}
			return nil, fmt.Errorf("查询题目: %w", err)
		}
		if sq.OwnerID != e.OwnerID {
			return nil, fmt.Errorf("%w: 题目 %d 不属于本人题库", ErrExamItemInvalid, in.SourceQuestionID)
		}
		if sq.Status != StatusActive {
			return nil, fmt.Errorf("%w: 题目 %d 已停用", ErrExamItemInvalid, in.SourceQuestionID)
		}

		var itemOptions []Option
		if len(sqOptions) > 0 {
			if err := json.Unmarshal(sqOptions, &itemOptions); err != nil {
				return nil, fmt.Errorf("解析题目选项: %w", err)
			}
		}
		items = append(items, ExamItem{
			SourceQuestionID: sq.ID, OrderNo: in.OrderNo, Type: sq.Type, Stem: sq.Stem,
			Options: itemOptions, CorrectAnswer: sq.CorrectAnswer, Score: in.Score,
		})
	}

	if _, err := tx.Exec(ctx, `DELETE FROM exam_items WHERE exam_id = $1`, examID); err != nil {
		return nil, fmt.Errorf("清理旧条目: %w", err)
	}
	for _, it := range items {
		var optionsPayload any
		if len(it.Options) > 0 {
			payload, merr := json.Marshal(it.Options)
			if merr != nil {
				return nil, fmt.Errorf("序列化条目选项: %w", merr)
			}
			optionsPayload = payload
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO exam_items (exam_id, source_question_id, order_no, type, stem, options, correct_answer, score)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			examID, it.SourceQuestionID, it.OrderNo, it.Type, it.Stem, optionsPayload, it.CorrectAnswer, it.Score,
		); err != nil {
			return nil, fmt.Errorf("写入条目: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE exams SET updated_at = now() WHERE id = $1`, examID); err != nil {
		return nil, fmt.Errorf("更新考试时间: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("提交条目: %w", err)
	}

	saved, err := s.loadItems(ctx, examID)
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// PublishExam 发布考试：锁行校验题目/总分/时间窗口，冻结为 published（§14.3）。
func (s *Service) PublishExam(ctx context.Context, userID int64, isAdmin bool, examID int64) (*Exam, error) {
	if _, err := s.checkExamAccess(ctx, userID, isAdmin, examID); err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM exams WHERE id = $1 FOR UPDATE`, examID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("锁定考试: %w", err)
	}
	if status != ExamStatusDraft {
		return nil, ErrExamNotDraft
	}

	items, err := s.loadItems(ctx, examID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrExamNoItems
	}
	total := 0
	for _, it := range items {
		if it.Type != TypeSingle && it.Type != TypeJudge {
			return nil, fmt.Errorf("%w: 条目 %d 题型非法", ErrExamItemInvalid, it.OrderNo)
		}
		if it.Stem == "" || it.CorrectAnswer == "" {
			return nil, fmt.Errorf("%w: 条目 %d 快照不完整", ErrExamItemInvalid, it.OrderNo)
		}
		total += it.Score
	}
	if total <= 0 {
		return nil, ErrExamNoTotalScore
	}

	var openAt, closeAt *time.Time
	var duration *int
	if err := tx.QueryRow(ctx,
		`SELECT open_at, close_at, duration_seconds FROM exams WHERE id = $1`, examID,
	).Scan(&openAt, &closeAt, &duration); err != nil {
		return nil, fmt.Errorf("查询时间窗口: %w", err)
	}
	if openAt == nil || closeAt == nil || duration == nil || *duration <= 0 {
		return nil, ErrExamMissingTimes
	}
	if !closeAt.After(*openAt) {
		return nil, ErrExamInvalidTimes
	}

	if _, err := tx.Exec(ctx, `
		UPDATE exams SET status = 'published', published_at = now(), updated_at = now()
		WHERE id = $1`, examID); err != nil {
		return nil, fmt.Errorf("发布考试: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("提交发布: %w", err)
	}

	r, err := s.getExamRow(ctx, examID)
	if err != nil {
		return nil, err
	}
	return toExam(r, items), nil
}

// DisableExam 删除草稿考试（幂等）；已发布考试不可删除。
func (s *Service) DisableExam(ctx context.Context, userID int64, isAdmin bool, examID int64) error {
	e, err := s.checkExamAccess(ctx, userID, isAdmin, examID)
	if err != nil {
		return err
	}
	if e.Status != ExamStatusDraft {
		return ErrExamNotDraft
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE exams SET status = 'disabled', updated_at = now() WHERE id = $1 AND status = 'draft'`, examID); err != nil {
		return fmt.Errorf("删除考试: %w", err)
	}
	return nil
}

// ListExams 考试列表：教师本人，管理员全部；按状态过滤可选。
func (s *Service) ListExams(ctx context.Context, userID int64, isAdmin bool, statusFilter string) ([]Exam, error) {
	where := "WHERE owner_id = $1"
	args := []any{userID}
	if isAdmin {
		where = ""
	}
	if statusFilter != "" {
		statusFilter = strings.ToLower(strings.TrimSpace(statusFilter))
		switch statusFilter {
		case ExamStatusDraft, ExamStatusPublished, ExamStatusClosed, ExamStatusDisabled:
		default:
			return nil, fmt.Errorf("%w: status 过滤值非法", ErrInvalidData)
		}
		if args != nil {
			where += fmt.Sprintf(" AND status = $%d", len(args)+1)
			args = append(args, statusFilter)
		} else {
			where = "WHERE status = $1"
			args = []any{statusFilter}
		}
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+examColumns+` FROM exams `+where+` ORDER BY id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("查询考试: %w", err)
	}
	defer rows.Close()

	out := []Exam{}
	for rows.Next() {
		var r examRow
		if err := rows.Scan(&r.ID, &r.OwnerID, &r.Title, &r.LevelCode, &r.Status, &r.OpenAt,
			&r.CloseAt, &r.DurationSeconds, &r.PublishedAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("读取考试: %w", err)
		}
		out = append(out, *toExam(&r, nil))
	}
	return out, rows.Err()
}

// GetExam 考试详情（教师侧，含条目快照与答案）。
func (s *Service) GetExam(ctx context.Context, userID int64, isAdmin bool, examID int64) (*Exam, error) {
	e, err := s.checkExamAccess(ctx, userID, isAdmin, examID)
	if err != nil {
		return nil, err
	}
	items, err := s.loadItems(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	return toExam(e, items), nil
}

// ListAvailableExams 学生可参加考试列表：仅已发布考试，附带开放窗口状态
// 与本人作答状态（§15.1）。
func (s *Service) ListAvailableExams(ctx context.Context, studentID int64) ([]AvailableExam, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.title, e.level_code, e.open_at, e.close_at, e.duration_seconds,
		       CASE WHEN now() < e.open_at THEN 'not_open'
		            WHEN now() > e.close_at THEN 'closed'
		            ELSE 'open' END AS window,
		       COALESCE(a.status, 'none') AS attempt_status, a.total_score
		FROM exams e
		LEFT JOIN exam_attempts a ON a.exam_id = e.id AND a.user_id = $1
		WHERE e.status = 'published'
		ORDER BY e.id DESC`, studentID)
	if err != nil {
		return nil, fmt.Errorf("查询可参加考试: %w", err)
	}
	defer rows.Close()

	out := []AvailableExam{}
	for rows.Next() {
		var a AvailableExam
		if err := rows.Scan(&a.ID, &a.Title, &a.LevelCode, &a.OpenAt, &a.CloseAt,
			&a.DurationSeconds, &a.Window, &a.AttemptStatus, &a.TotalScore); err != nil {
			return nil, fmt.Errorf("读取可参加考试: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
