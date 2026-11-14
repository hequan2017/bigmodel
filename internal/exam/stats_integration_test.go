package exam

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestExamStatsIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	teacherID := createExamUser(t, pool, "itest_st_t_"+suffix, "teacher")
	s1 := createExamUser(t, pool, "itest_st_s1_"+suffix, "student")
	s2 := createExamUser(t, pool, "itest_st_s2_"+suffix, "student")
	s3 := createExamUser(t, pool, "itest_st_s3_"+suffix, "student")

	examID, item1ID, item2ID := seedPublishedExam(t, svc, teacherID, "统计测试", -time.Hour, time.Hour, time.Hour)

	// 学生1：单选对、判断错 → 总分 5。
	if _, err := svc.StartAttempt(ctx, s1, examID); err != nil {
		t.Fatalf("开始1: %v", err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, s1, examID, SaveAnswersInput{
		ExpectedRevision: 1,
		Answers:          []AnswerInput{{ItemID: item1ID, Answer: "A"}, {ItemID: item2ID, Answer: "false"}},
	}); err != nil {
		t.Fatalf("保存1: %v", err)
	}
	if _, err := svc.SubmitAttempt(ctx, s1, examID, 2); err != nil {
		t.Fatalf("交卷1: %v", err)
	}

	// 学生2：单选错、判断对 → 总分 5。
	if _, err := svc.StartAttempt(ctx, s2, examID); err != nil {
		t.Fatalf("开始2: %v", err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, s2, examID, SaveAnswersInput{
		ExpectedRevision: 1,
		Answers:          []AnswerInput{{ItemID: item1ID, Answer: "B"}, {ItemID: item2ID, Answer: "true"}},
	}); err != nil {
		t.Fatalf("保存2: %v", err)
	}
	if _, err := svc.SubmitAttempt(ctx, s2, examID, 2); err != nil {
		t.Fatalf("交卷2: %v", err)
	}

	// 学生3：作答中，不提交 → in_progress。
	if _, err := svc.StartAttempt(ctx, s3, examID); err != nil {
		t.Fatalf("开始3: %v", err)
	}

	stats, err := svc.GetExamStats(ctx, teacherID, false, examID)
	if err != nil {
		t.Fatalf("统计: %v", err)
	}
	if stats.SubmittedAttempts != 2 || stats.InProgressAttempts != 1 {
		t.Errorf("提交/进行中人数错误: %+v", stats)
	}
	// seedPublishedExam 的题目分值为 7 与 3：s1 得 7（单选对判断错），s2 得 3。
	if stats.AvgScore != 5 || stats.MaxScore != 7 || stats.MinScore != 3 {
		t.Errorf("分数统计错误: %+v", stats)
	}
	if len(stats.Items) != 2 {
		t.Fatalf("条目统计数 %d", len(stats.Items))
	}
	first := stats.Items[0]
	if first.Attempted != 2 || first.Correct != 1 || first.Wrong != 1 || first.ErrorRate != 50 {
		t.Errorf("第 1 题统计错误: %+v", first)
	}
	second := stats.Items[1]
	if second.Attempted != 2 || second.Correct != 1 || second.ErrorRate != 50 {
		t.Errorf("第 2 题统计错误: %+v", second)
	}

	// 越权：其他教师不可见。
	other := createExamUser(t, pool, "itest_st_o_"+suffix, "teacher")
	if _, err := svc.GetExamStats(ctx, other, false, examID); !errors.Is(err, ErrNotFound) {
		t.Errorf("越权统计应 404, got %v", err)
	}
}
