package exam

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestAttemptDetailIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	teacherID := createExamUser(t, pool, "itest_d_t_"+suffix, "teacher")
	otherTeacher := createExamUser(t, pool, "itest_d_o_"+suffix, "teacher")
	studentID := createExamUser(t, pool, "itest_d_s_"+suffix, "student")

	examID, item1ID, _ := seedPublishedExam(t, svc, teacherID, "作答详情测试", -time.Hour, time.Hour, time.Hour)

	// 学生1：一对一错 → 总分 7；学生2：未作答直接到期结算 → 全 0。
	if _, err := svc.StartAttempt(ctx, studentID, examID); err != nil {
		t.Fatalf("开始: %v", err)
	}
	if _, _, err := svc.SaveAttemptAnswers(ctx, studentID, examID, SaveAnswersInput{
		ExpectedRevision: 1,
		Answers:          []AnswerInput{{ItemID: item1ID, Answer: "A"}},
	}); err != nil {
		t.Fatalf("保存: %v", err)
	}
	if _, err := svc.SubmitAttempt(ctx, studentID, examID, 2); err != nil {
		t.Fatalf("交卷: %v", err)
	}
	attemptID := attemptIDOf(t, pool, studentID, examID)

	// 教师查看逐题明细。
	detail, err := svc.GetAttemptDetail(ctx, teacherID, false, examID, attemptID)
	if err != nil {
		t.Fatalf("查询详情: %v", err)
	}
	if detail.LoginName != fmt.Sprintf("itest_d_s_%s", suffix) || detail.Status != AttemptSubmitted {
		t.Errorf("详情头部错误: %+v", detail)
	}
	if len(detail.Items) != 2 {
		t.Fatalf("明细条目数 %d", len(detail.Items))
	}
	first := detail.Items[0]
	if first.StudentAnswer != "A" || first.CorrectAnswer != "A" || first.Awarded != 7 || first.FullScore != 7 {
		t.Errorf("第 1 题明细错误: %+v", first)
	}
	second := detail.Items[1]
	if second.StudentAnswer != "未作答" || second.Awarded != 0 {
		t.Errorf("未作答明细错误: %+v", second)
	}
	if len(first.Options) != 3 {
		t.Errorf("选项快照缺失: %+v", first.Options)
	}

	// 越权：其他教师不可见。
	if _, err := svc.GetAttemptDetail(ctx, otherTeacher, false, examID, attemptID); !errors.Is(err, ErrNotFound) {
		t.Errorf("越权应 404, got %v", err)
	}
	// 作答不属于该考试 → 404。
	if _, err := svc.GetAttemptDetail(ctx, teacherID, false, examID+9999, attemptID); !errors.Is(err, ErrNotFound) {
		t.Errorf("考试不匹配应 404, got %v", err)
	}
}
