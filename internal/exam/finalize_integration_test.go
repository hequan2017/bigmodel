package exam

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFinalizeFlowIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	teacherID := createExamUser(t, pool, "itest_f_t_"+suffix, "teacher")
	student1 := createExamUser(t, pool, "itest_f_s1_"+suffix, "student")
	student2 := createExamUser(t, pool, "itest_f_s2_"+suffix, "student")
	student3 := createExamUser(t, pool, "itest_f_s3_"+suffix, "student")
	student4 := createExamUser(t, pool, "itest_f_s4_"+suffix, "student")

	examID, item1ID, item2ID := seedPublishedExam(t, svc, teacherID, "判分测试", -time.Hour, time.Hour, time.Hour)

	// 学生1：一对一错 → 总分 7。
	if _, err := svc.StartAttempt(ctx, student1, examID); err != nil {
		t.Fatalf("开始: %v", err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, student1, examID, SaveAnswersInput{
		ExpectedRevision: 1,
		Answers:          []AnswerInput{{ItemID: item1ID, Answer: "A"}, {ItemID: item2ID, Answer: "false"}},
	}); err != nil {
		t.Fatalf("保存: %v", err)
	}
	res1, err := svc.SubmitAttempt(ctx, student1, examID, 2)
	if err != nil {
		t.Fatalf("交卷: %v", err)
	}
	if res1.Status != AttemptSubmitted || res1.SubmitReason != SubmitReasonManual ||
		res1.TotalScore == nil || *res1.TotalScore != 7 {
		t.Fatalf("交卷结果错误: %+v", res1)
	}

	// 幂等：重复交卷返回相同结果。
	res1b, err := svc.SubmitAttempt(ctx, student1, examID, 2)
	if err != nil || res1b.TotalScore == nil || *res1b.TotalScore != 7 {
		t.Fatalf("重复交卷应幂等: %+v err=%v", res1b, err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, student1, examID, SaveAnswersInput{
		ExpectedRevision: 2, Answers: []AnswerInput{{ItemID: item1ID, Answer: "A"}},
	}); !errors.Is(err, ErrAttemptSubmitted) {
		t.Errorf("交卷后保存应 ErrAttemptSubmitted, got %v", err)
	}

	// 逐题得分落库。
	var awarded1, awarded2 int
	if err := pool.QueryRow(ctx,
		`SELECT awarded_score FROM exam_answers WHERE attempt_id = $1 AND exam_item_id = $2`,
		attemptIDOf(t, pool, student1, examID), item1ID).Scan(&awarded1); err != nil {
		t.Fatalf("查询逐题得分: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT awarded_score FROM exam_answers WHERE attempt_id = $1 AND exam_item_id = $2`,
		attemptIDOf(t, pool, student1, examID), item2ID).Scan(&awarded2); err != nil {
		t.Fatalf("查询逐题得分: %v", err)
	}
	if awarded1 != 7 || awarded2 != 0 {
		t.Errorf("逐题得分错误: item1=%d item2=%d", awarded1, awarded2)
	}

	// 学生2：只答判断题（未作答题目得 0 分）→ 总分 3。
	if _, err := svc.StartAttempt(ctx, student2, examID); err != nil {
		t.Fatalf("开始2: %v", err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, student2, examID, SaveAnswersInput{
		ExpectedRevision: 1, Answers: []AnswerInput{{ItemID: item2ID, Answer: "true"}},
	}); err != nil {
		t.Fatalf("保存2: %v", err)
	}
	res2, err := svc.SubmitAttempt(ctx, student2, examID, 2)
	if err != nil || res2.TotalScore == nil || *res2.TotalScore != 3 {
		t.Fatalf("未作答计 0 分: %+v err=%v", res2, err)
	}

	// 学生3：有效期内版本不一致 → 409。
	if _, err := svc.StartAttempt(ctx, student3, examID); err != nil {
		t.Fatalf("开始3: %v", err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, student3, examID, SaveAnswersInput{
		ExpectedRevision: 1, Answers: []AnswerInput{{ItemID: item1ID, Answer: "A"}},
	}); err != nil {
		t.Fatalf("保存3: %v", err)
	}
	if _, err := svc.SubmitAttempt(ctx, student3, examID, 1); !errors.Is(err, ErrRevisionConflict) {
		t.Errorf("版本不一致交卷应 409, got %v", err)
	}

	// 学生4：过期后交卷 → timeout 结算（忽略版本差异，按已保存答案判分）。
	if _, err := svc.StartAttempt(ctx, student4, examID); err != nil {
		t.Fatalf("开始4: %v", err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, student4, examID, SaveAnswersInput{
		ExpectedRevision: 1,
		Answers:          []AnswerInput{{ItemID: item1ID, Answer: "A"}, {ItemID: item2ID, Answer: "true"}},
	}); err != nil {
		t.Fatalf("保存4: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE exam_attempts SET deadline_at = now() - interval '1 second'
		 WHERE exam_id = $1 AND user_id = $2`, examID, student4); err != nil {
		t.Fatalf("模拟过期: %v", err)
	}
	res4, err := svc.SubmitAttempt(ctx, student4, examID, 999) // 版本差异应被忽略
	if err != nil {
		t.Fatalf("过期交卷: %v", err)
	}
	if res4.SubmitReason != SubmitReasonTimeout || res4.TotalScore == nil || *res4.TotalScore != 10 {
		t.Fatalf("timeout 结算错误: %+v", res4)
	}

	// 成绩视图：学生查本人；教师查本人考试列表。
	view, err := svc.AttemptResult(ctx, student1, examID)
	if err != nil || view.Status != AttemptSubmitted || view.SubmitReason != SubmitReasonManual {
		t.Fatalf("学生成绩视图: %+v err=%v", view, err)
	}
	results, err := svc.ListExamResults(ctx, teacherID, false, examID)
	if err != nil || len(results) != 4 {
		t.Fatalf("教师成绩列表: len=%d err=%v", len(results), err)
	}
	otherTeacher := createExamUser(t, pool, "itest_f_other_"+suffix, "teacher")
	if _, err := svc.ListExamResults(ctx, otherTeacher, false, examID); !errors.Is(err, ErrNotFound) {
		t.Errorf("他人查成绩应 404, got %v", err)
	}
}

func TestSweepIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	teacherID := createExamUser(t, pool, "itest_sw_t_"+suffix, "teacher")
	studentID := createExamUser(t, pool, "itest_sw_s_"+suffix, "student")

	examID, item1ID, item2ID := seedPublishedExam(t, svc, teacherID, "扫描测试", -time.Hour, time.Hour, time.Hour)
	if _, err := svc.StartAttempt(ctx, studentID, examID); err != nil {
		t.Fatalf("开始: %v", err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, studentID, examID, SaveAnswersInput{
		ExpectedRevision: 1,
		Answers:          []AnswerInput{{ItemID: item1ID, Answer: "A"}, {ItemID: item2ID, Answer: "true"}},
	}); err != nil {
		t.Fatalf("保存: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE exam_attempts SET deadline_at = now() - interval '5 seconds'
		 WHERE exam_id = $1 AND user_id = $2`, examID, studentID); err != nil {
		t.Fatalf("模拟过期: %v", err)
	}

	// 查询作答触发到期检查（§14.7）：GET 即结算。
	a, err := svc.GetAttempt(ctx, studentID, examID)
	if err != nil {
		t.Fatalf("恢复: %v", err)
	}
	if a.Status != AttemptSubmitted {
		t.Fatalf("查询应触发 timeout 结算: %s", a.Status)
	}
	if a.TotalScore == nil || *a.TotalScore != 10 {
		t.Errorf("扫描结算总分错误: %v", a.TotalScore)
	}

	// 再跑扫描：已结算的不会重复处理。
	n, err := svc.SweepExpiredAttempts(ctx)
	if err != nil || n != 0 {
		t.Errorf("重复扫描应 0, got %d err=%v", n, err)
	}
}

// attemptIDOf 查询学生的作答 ID。
func attemptIDOf(t *testing.T, pool *pgxpool.Pool, userID, examID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM exam_attempts WHERE user_id = $1 AND exam_id = $2`, userID, examID,
	).Scan(&id); err != nil {
		t.Fatalf("查询作答 ID: %v", err)
	}
	return id
}
