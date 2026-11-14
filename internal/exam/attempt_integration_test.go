package exam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func seedPublishedExam(t *testing.T, svc *Service, teacherID int64, title string, openOffset, closeOffset, duration time.Duration) (examID, q1ID, q2ID int64) {
	t.Helper()
	ctx := context.Background()
	q1, err := svc.CreateQuestion(ctx, teacherID, QuestionInput{
		Type: "single", Stem: title + " 单选",
		Options:       []Option{{Key: "A", Text: "甲"}, {Key: "B", Text: "乙"}, {Key: "C", Text: "丙"}},
		CorrectAnswer: "A", DefaultScore: 7,
	})
	if err != nil {
		t.Fatalf("创建单选: %v", err)
	}
	q2, err := svc.CreateQuestion(ctx, teacherID, QuestionInput{
		Type: "judge", Stem: title + " 判断", CorrectAnswer: "true", DefaultScore: 3,
	})
	if err != nil {
		t.Fatalf("创建判断: %v", err)
	}
	open := time.Now().Add(openOffset).Format(time.RFC3339)
	close := time.Now().Add(closeOffset).Format(time.RFC3339)
	seconds := int(duration.Seconds())
	e, err := svc.CreateExam(ctx, teacherID, ExamInput{
		Title: title, LevelCode: "L1", OpenAt: &open, CloseAt: &close, DurationSeconds: &seconds,
	})
	if err != nil {
		t.Fatalf("创建考试: %v", err)
	}
	if _, err := svc.SetExamItems(ctx, teacherID, false, e.ID, []ExamItemInput{
		{SourceQuestionID: q1.ID, OrderNo: 1, Score: 7},
		{SourceQuestionID: q2.ID, OrderNo: 2, Score: 3},
	}); err != nil {
		t.Fatalf("设置条目: %v", err)
	}
	if _, err := svc.PublishExam(ctx, teacherID, false, e.ID); err != nil {
		t.Fatalf("发布考试: %v", err)
	}
	return e.ID, q1.ID, q2.ID
}

func TestAttemptFlowIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	teacherID := createExamUser(t, pool, "itest_t_a_"+suffix, "teacher")
	studentID := createExamUser(t, pool, "itest_t_s_"+suffix, "student")

	examID, _, _ := seedPublishedExam(t, svc, teacherID, "作答流程测试", -time.Hour, time.Hour, time.Hour)

	// 开始：rev 1，截止 ≈ now+1h，试卷无标准答案。
	a1, err := svc.StartAttempt(ctx, studentID, examID)
	if err != nil {
		t.Fatalf("开始考试: %v", err)
	}
	if a1.AnswerRevision != 1 || len(a1.Items) != 2 || a1.Status != AttemptInProgress {
		t.Fatalf("开始结果异常: %+v", a1)
	}
	if !a1.DeadlineAt.After(a1.ServerNow.Add(55 * time.Minute)) {
		t.Errorf("截止时间应约为 now+1h: %v", a1.DeadlineAt)
	}
	raw, _ := json.Marshal(a1)
	if strings.Contains(string(raw), "correct_answer") {
		t.Error("学生视图不得包含标准答案")
	}

	// 重复开始：不重置计时。
	time.Sleep(1100 * time.Millisecond)
	a2, err := svc.StartAttempt(ctx, studentID, examID)
	if err != nil || !a2.DeadlineAt.Equal(a1.DeadlineAt) || a2.StartedAt.Equal(a1.StartedAt) && false {
		t.Fatalf("重复开始应返回原作答且不重置计时: %v vs %v err=%v", a2.DeadlineAt, a1.DeadlineAt, err)
	}

	// 保存答案。
	rev, savedAt, err := svc.SaveAttemptAnswers(ctx, studentID, examID, SaveAnswersInput{
		ExpectedRevision: 1,
		Answers: []AnswerInput{
			{ItemID: a1.Items[0].ID, Answer: "a"},
			{ItemID: a1.Items[1].ID, Answer: "True"},
		},
	})
	if err != nil || rev != 2 {
		t.Fatalf("保存答案: rev=%d err=%v", rev, err)
	}
	if savedAt.IsZero() {
		t.Error("应返回 saved_at")
	}

	a3, err := svc.GetAttempt(ctx, studentID, examID)
	if err != nil {
		t.Fatalf("恢复作答: %v", err)
	}
	if a3.AnswerRevision != 2 ||
		a3.SavedAnswers[fmt.Sprintf("%d", a1.Items[0].ID)] != "A" ||
		a3.SavedAnswers[fmt.Sprintf("%d", a1.Items[1].ID)] != "true" {
		t.Errorf("恢复数据错误: rev=%d saved=%v", a3.AnswerRevision, a3.SavedAnswers)
	}

	// 版本冲突：携带旧版本保存 → 409，不覆盖。
	if _, _, err := svc.SaveAttemptAnswers(ctx, studentID, examID, SaveAnswersInput{
		ExpectedRevision: 1,
		Answers:          []AnswerInput{{ItemID: a1.Items[0].ID, Answer: "B"}},
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Errorf("旧版本应 ErrRevisionConflict, got %v", err)
	}
	a4, _ := svc.GetAttempt(ctx, studentID, examID)
	if a4.SavedAnswers[fmt.Sprintf("%d", a1.Items[0].ID)] != "A" {
		t.Errorf("冲突保存不应覆盖答案: %v", a4.SavedAnswers)
	}

	// 答案合法性。
	if _, _, err := svc.SaveAttemptAnswers(ctx, studentID, examID, SaveAnswersInput{
		ExpectedRevision: 2, Answers: []AnswerInput{{ItemID: a1.Items[0].ID, Answer: "E"}},
	}); !errors.Is(err, ErrAnswerInvalid) {
		t.Errorf("非法选项键应 ErrAnswerInvalid, got %v", err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, studentID, examID, SaveAnswersInput{
		ExpectedRevision: 2, Answers: []AnswerInput{{ItemID: a1.Items[1].ID, Answer: "maybe"}},
	}); !errors.Is(err, ErrAnswerInvalid) {
		t.Errorf("非法判断答案应 ErrAnswerInvalid, got %v", err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, studentID, examID, SaveAnswersInput{
		ExpectedRevision: 2, Answers: []AnswerInput{{ItemID: 99999, Answer: "A"}},
	}); !errors.Is(err, ErrAnswerInvalid) {
		t.Errorf("非本卷条目应 ErrAnswerInvalid, got %v", err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, studentID, examID, SaveAnswersInput{
		ExpectedRevision: 2,
		Answers: []AnswerInput{
			{ItemID: a1.Items[0].ID, Answer: "A"},
			{ItemID: a1.Items[0].ID, Answer: "B"},
		},
	}); !errors.Is(err, ErrAnswerInvalid) {
		t.Errorf("重复条目应 ErrAnswerInvalid, got %v", err)
	}

	// 截止后保存被拒绝且不改动答案。
	if _, err := pool.Exec(ctx,
		`UPDATE exam_attempts SET deadline_at = now() - interval '1 second' WHERE exam_id = $1 AND user_id = $2`,
		examID, studentID); err != nil {
		t.Fatalf("模拟过期: %v", err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, studentID, examID, SaveAnswersInput{
		ExpectedRevision: 2, Answers: []AnswerInput{{ItemID: a1.Items[0].ID, Answer: "B"}},
	}); !errors.Is(err, ErrAttemptExpired) {
		t.Errorf("过期保存应 ErrAttemptExpired, got %v", err)
	}
	a5, _ := svc.GetAttempt(ctx, studentID, examID)
	if a5.SavedAnswers[fmt.Sprintf("%d", a1.Items[0].ID)] != "A" {
		t.Errorf("过期保存不应改动答案: %v", a5.SavedAnswers)
	}

	// 未开始的学生保存 → 404。
	other := createExamUser(t, pool, "itest_t_s2_"+suffix, "student")
	if _, _, err := svc.SaveAttemptAnswers(ctx, other, examID, SaveAnswersInput{
		ExpectedRevision: 1, Answers: []AnswerInput{{ItemID: a1.Items[0].ID, Answer: "A"}},
	}); !errors.Is(err, ErrAttemptNotFound) {
		t.Errorf("未开始作答应 ErrAttemptNotFound, got %v", err)
	}
}

func TestAttemptWindowIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	teacherID := createExamUser(t, pool, "itest_w1_"+suffix, "teacher")
	studentID := createExamUser(t, pool, "itest_w1_s_"+suffix, "student")

	// 未开放（未来窗口）。
	e1, _, _ := seedPublishedExam(t, svc, teacherID, "未来考试", time.Hour, 2*time.Hour, time.Hour)
	if _, err := svc.StartAttempt(ctx, studentID, e1); !errors.Is(err, ErrExamNotOpen) {
		t.Errorf("未开放应 ErrExamNotOpen, got %v", err)
	}

	// 已截止（过去窗口）。
	e2, _, _ := seedPublishedExam(t, svc, teacherID, "过期考试", -3*time.Hour, -time.Hour, time.Hour)
	if _, err := svc.StartAttempt(ctx, studentID, e2); !errors.Is(err, ErrExamClosed) {
		t.Errorf("已截止应 ErrExamClosed, got %v", err)
	}

	// 草稿考试 → 404 防枚举。
	e3, err := svc.CreateExam(ctx, teacherID, ExamInput{Title: "草稿考试 " + suffix})
	if err != nil {
		t.Fatalf("创建草稿: %v", err)
	}
	if _, err := svc.StartAttempt(ctx, studentID, e3.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("草稿考试应 404, got %v", err)
	}

	// deadline = min(now+时长, close)：close 早于时长时取 close。
	shortClose := 90 * time.Second
	e4, _, _ := seedPublishedExam(t, svc, teacherID, "短窗口考试", -time.Minute, shortClose, time.Hour)
	a, err := svc.StartAttempt(ctx, studentID, e4)
	if err != nil {
		t.Fatalf("开始: %v", err)
	}
	now := time.Now()
	if a.DeadlineAt.Sub(now) > shortClose+5*time.Second || a.DeadlineAt.Before(now) {
		t.Errorf("截止应取 min(时长, close): deadline=%v", a.DeadlineAt)
	}
}
