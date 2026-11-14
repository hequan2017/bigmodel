package exam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service 提供题库管理；题目归属教师本人，管理员可见全部。
type Service struct {
	pool *pgxpool.Pool
}

// NewService 构造题库服务。
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// scanQuestion 把行扫描为教师侧视图（含标准答案）。
func scanQuestion(row pgx.Row) (*Question, error) {
	var q Question
	var options []byte
	var tags []string
	if err := row.Scan(&q.ID, &q.OwnerID, &q.Type, &q.Stem, &options, &q.CorrectAnswer,
		&q.DefaultScore, &q.LevelCode, &tags, &q.Status, &q.CreatedAt, &q.UpdatedAt); err != nil {
		return nil, err
	}
	if len(options) > 0 {
		if err := json.Unmarshal(options, &q.Options); err != nil {
			return nil, fmt.Errorf("解析选项: %w", err)
		}
	}
	if len(tags) > 0 {
		q.Tags = tags
	}
	return &q, nil
}

// checkAccess 校验题目存在且可管理；越权统一 404 防枚举。
func (s *Service) checkAccess(ctx context.Context, userID int64, isAdmin bool, questionID int64) (*Question, error) {
	rows := s.pool.QueryRow(ctx, `
		SELECT id, owner_id, type, stem, options, correct_answer,
		       default_score, level_code, tags, status, created_at, updated_at
		FROM exam_questions WHERE id = $1`, questionID)
	q, err := scanQuestion(rows)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询题目: %w", err)
	}
	if !isAdmin && q.OwnerID != userID {
		return nil, ErrNotFound
	}
	return q, nil
}

// CreateQuestion 创建题目。
func (s *Service) CreateQuestion(ctx context.Context, ownerID int64, in QuestionInput) (*Question, error) {
	in, err := normalizeAndValidate(in)
	if err != nil {
		return nil, err
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}

	var options any
	if in.Type == TypeSingle {
		payload, merr := json.Marshal(in.Options)
		if merr != nil {
			return nil, fmt.Errorf("序列化选项: %w", merr)
		}
		options = payload
	}

	q := &Question{
		OwnerID:       ownerID,
		Type:          in.Type,
		Stem:          in.Stem,
		CorrectAnswer: in.CorrectAnswer,
		DefaultScore:  in.DefaultScore,
		LevelCode:     in.LevelCode,
		Status:        StatusActive,
	}
	if in.Tags != nil {
		q.Tags = in.Tags
	}
	if in.Type == TypeSingle {
		q.Options = in.Options
	}

	err = s.pool.QueryRow(ctx, `
		INSERT INTO exam_questions (owner_id, type, stem, options, correct_answer, default_score, level_code, tags)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at`,
		ownerID, in.Type, in.Stem, options, in.CorrectAnswer, in.DefaultScore, in.LevelCode, in.Tags,
	).Scan(&q.ID, &q.CreatedAt, &q.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("创建题目: %w", err)
	}
	return q, nil
}

// UpdateQuestion 更新题目；已禁用题目不可更新。
func (s *Service) UpdateQuestion(ctx context.Context, userID int64, isAdmin bool, questionID int64, in QuestionInput) (*Question, error) {
	if _, err := s.checkAccess(ctx, userID, isAdmin, questionID); err != nil {
		return nil, err
	}
	in, err := normalizeAndValidate(in)
	if err != nil {
		return nil, err
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}

	var options any
	if in.Type == TypeSingle {
		payload, merr := json.Marshal(in.Options)
		if merr != nil {
			return nil, fmt.Errorf("序列化选项: %w", merr)
		}
		options = payload
	}

	rows := s.pool.QueryRow(ctx, `
		UPDATE exam_questions SET type = $2, stem = $3, options = $4, correct_answer = $5,
		       default_score = $6, level_code = $7, tags = $8, updated_at = now()
		WHERE id = $1 AND status <> 'disabled'
		RETURNING id, owner_id, type, stem, options, correct_answer,
		          default_score, level_code, tags, status, created_at, updated_at`,
		questionID, in.Type, in.Stem, options, in.CorrectAnswer, in.DefaultScore, in.LevelCode, in.Tags,
	)
	q, err := scanQuestion(rows)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidState // 已禁用
	}
	return q, err
}

// DisableQuestion 软删除题目（幂等）；已有试卷快照不受影响。
func (s *Service) DisableQuestion(ctx context.Context, userID int64, isAdmin bool, questionID int64) error {
	if _, err := s.checkAccess(ctx, userID, isAdmin, questionID); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE exam_questions SET status = 'disabled', updated_at = now()
		WHERE id = $1 AND status <> 'disabled'`, questionID)
	if err != nil {
		return fmt.Errorf("禁用题目: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil // 已禁用视为幂等成功
	}
	return nil
}

// ListQuestions 列出题目：教师本人 active（可带题型过滤），管理员全部。
func (s *Service) ListQuestions(ctx context.Context, userID int64, isAdmin bool, typeFilter string) ([]Question, error) {
	where := "WHERE owner_id = $1 AND status = 'active'"
	args := []any{userID}
	if isAdmin {
		where = "WHERE status = 'active'"
		args = nil
	}
	if typeFilter != "" {
		typeFilter = strings.ToLower(strings.TrimSpace(typeFilter))
		if typeFilter != TypeSingle && typeFilter != TypeJudge {
			return nil, fmt.Errorf("%w: type 过滤值非法", ErrInvalidData)
		}
		if args != nil {
			where += fmt.Sprintf(" AND type = $%d", len(args)+1)
			args = append(args, typeFilter)
		} else {
			where += " AND type = $1"
			args = []any{typeFilter}
		}
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, owner_id, type, stem, options, correct_answer,
		       default_score, level_code, tags, status, created_at, updated_at
		FROM exam_questions `+where+` ORDER BY id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("查询题目: %w", err)
	}
	defer rows.Close()

	out := []Question{}
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, fmt.Errorf("读取题目: %w", err)
		}
		out = append(out, *q)
	}
	return out, rows.Err()
}

// GetQuestion 查看题目详情（教师侧，含答案）。
func (s *Service) GetQuestion(ctx context.Context, userID int64, isAdmin bool, questionID int64) (*Question, error) {
	return s.checkAccess(ctx, userID, isAdmin, questionID)
}
