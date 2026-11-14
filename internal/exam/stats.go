package exam

import (
	"context"
	"fmt"
)

// ExamStats 是教师侧考试统计聚合（§16.1 必要统计在现有列表呈现）。
type ExamStats struct {
	ExamID             int64      `json:"exam_id"`
	Title              string     `json:"title"`
	Status             string     `json:"status"`
	SubmittedAttempts  int        `json:"submitted_attempts"`
	InProgressAttempts int        `json:"in_progress_attempts"`
	AvgScore           float64    `json:"avg_score"`
	MaxScore           int        `json:"max_score"`
	MinScore           int        `json:"min_score"`
	Items              []ItemStat `json:"items"`
}

// ItemStat 是单题答对率统计。
type ItemStat struct {
	OrderNo   int     `json:"order_no"`
	Stem      string  `json:"stem"`
	FullScore int     `json:"full_score"`
	Attempted int     `json:"attempted"`
	Correct   int     `json:"correct"`
	Wrong     int     `json:"wrong"`
	ErrorRate float64 `json:"error_rate"`
}

// GetExamStats 聚合本人考试（或管理员全量）的提交情况与逐题答对率。
// 仅统计已提交作答；未作答题目不计入 answered。
func (s *Service) GetExamStats(ctx context.Context, userID int64, isAdmin bool, examID int64) (*ExamStats, error) {
	e, err := s.checkExamAccess(ctx, userID, isAdmin, examID)
	if err != nil {
		return nil, err
	}

	// 提交情况。
	var submitted, inProgress int
	var maxScore, minScore int
	avg := 0.0
	err = s.pool.QueryRow(ctx, `
		WITH t AS (SELECT total_score, status FROM exam_attempts WHERE exam_id = $1)
		SELECT count(*) FILTER (WHERE status = 'submitted'),
		       count(*) FILTER (WHERE status = 'in_progress'),
		       COALESCE(max(total_score) FILTER (WHERE status = 'submitted'), 0),
		       COALESCE(min(total_score) FILTER (WHERE status = 'submitted'), 0),
		       COALESCE(avg(total_score) FILTER (WHERE status = 'submitted'), 0)
		FROM t`, examID,
	).Scan(&submitted, &inProgress, &maxScore, &minScore, &avg)
	if err != nil {
		return nil, fmt.Errorf("聚合提交情况: %w", err)
	}

	// 试卷快照。
	itemRows, err := s.pool.Query(ctx, `
		SELECT order_no, stem, score FROM exam_items WHERE exam_id = $1 ORDER BY order_no`, examID)
	if err != nil {
		return nil, fmt.Errorf("查询试卷快照: %w", err)
	}
	type item struct {
		orderNo int
		stem    string
		score   int
	}
	snaps := []item{}
	for itemRows.Next() {
		var it item
		if err := itemRows.Scan(&it.orderNo, &it.stem, &it.score); err != nil {
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

	// 已提交作答的逐题得分。
	answerRows, err := s.pool.Query(ctx, `
		SELECT ea.exam_item_id, ea.awarded_score
		FROM exam_answers ea
		JOIN exam_attempts a ON a.id = ea.attempt_id
		WHERE a.exam_id = $1 AND a.status = 'submitted'`, examID)
	if err != nil {
		return nil, fmt.Errorf("查询逐题得分: %w", err)
	}
	awarded := map[int64]int{}
	for answerRows.Next() {
		var itemID int64
		var score int
		if err := answerRows.Scan(&itemID, &score); err != nil {
			answerRows.Close()
			return nil, fmt.Errorf("读取逐题得分: %w", err)
		}
		awarded[itemID] = score
	}
	if err := answerRows.Err(); err != nil {
		answerRows.Close()
		return nil, err
	}
	answerRows.Close()

	// 条目 ID 映射。
	idRows, err := s.pool.Query(ctx, `
		SELECT id, order_no FROM exam_items WHERE exam_id = $1`, examID)
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

	// 单条查询聚合每题作答与满分人数（attempted / correct）。
	statRows, err := s.pool.Query(ctx, `
		SELECT ea.exam_item_id, count(*) AS attempted,
		       count(*) FILTER (WHERE ea.awarded_score = ei.score) AS correct
		FROM exam_answers ea
		JOIN exam_attempts a ON a.id = ea.attempt_id
		JOIN exam_items ei ON ei.id = ea.exam_item_id
		WHERE a.exam_id = $1 AND a.status = 'submitted'
		GROUP BY ea.exam_item_id`, examID)
	if err != nil {
		return nil, fmt.Errorf("聚合逐题统计: %w", err)
	}
	statByItem := map[int64]struct {
		attempted, correct int
	}{}
	for statRows.Next() {
		var itemID int64
		var attempted, correct int
		if err := statRows.Scan(&itemID, &attempted, &correct); err != nil {
			statRows.Close()
			return nil, fmt.Errorf("读取逐题统计: %w", err)
		}
		statByItem[itemID] = struct {
			attempted, correct int
		}{attempted, correct}
	}
	if err := statRows.Err(); err != nil {
		statRows.Close()
		return nil, err
	}
	statRows.Close()

	items := make([]ItemStat, 0, len(snaps))
	for _, sn := range snaps {
		attempted, correct := 0, 0
		if itemID, ok := idByOrder[sn.orderNo]; ok {
			if st, ok := statByItem[itemID]; ok {
				attempted, correct = st.attempted, st.correct
			}
		}
		wrong := attempted - correct
		errRate := 0.0
		if attempted > 0 {
			errRate = float64(wrong) / float64(attempted) * 100
		}
		items = append(items, ItemStat{
			OrderNo: sn.orderNo, Stem: sn.stem, FullScore: sn.score,
			Attempted: attempted, Correct: correct, Wrong: wrong, ErrorRate: errRate,
		})
	}

	return &ExamStats{
		ExamID: e.ID, Title: e.Title, Status: e.Status,
		SubmittedAttempts: submitted, InProgressAttempts: inProgress,
		AvgScore: avg, MaxScore: maxScore, MinScore: minScore,
		Items: items,
	}, nil
}
