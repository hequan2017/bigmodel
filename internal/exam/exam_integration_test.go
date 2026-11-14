package exam

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestExamFlowIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	teacherA := createExamUser(t, pool, "itest_x_a_"+suffix, "teacher")
	teacherB := createExamUser(t, pool, "itest_x_b_"+suffix, "teacher")

	// 准备题目：教师 A 两道，教师 B 一道。
	q1, err := svc.CreateQuestion(ctx, teacherA, QuestionInput{
		Type: "single", Stem: "单选一",
		Options:       []Option{{Key: "A", Text: "对"}, {Key: "B", Text: "错2"}, {Key: "C", Text: "错3"}},
		CorrectAnswer: "A", DefaultScore: 5,
	})
	if err != nil {
		t.Fatalf("创建题目1: %v", err)
	}
	q2, err := svc.CreateQuestion(ctx, teacherA, QuestionInput{
		Type: "judge", Stem: "判断一", CorrectAnswer: "true", DefaultScore: 3,
	})
	if err != nil {
		t.Fatalf("创建题目2: %v", err)
	}
	qB, err := svc.CreateQuestion(ctx, teacherB, QuestionInput{
		Type: "judge", Stem: "他人题目", CorrectAnswer: "false", DefaultScore: 1,
	})
	if err != nil {
		t.Fatalf("创建他人题目: %v", err)
	}

	// 创建草稿（无时间窗口也可保存草稿）。
	draft, err := svc.CreateExam(ctx, teacherA, ExamInput{Title: "AI 等级考试 L1", LevelCode: "L1"})
	if err != nil {
		t.Fatalf("创建考试: %v", err)
	}
	if _, err := svc.CreateExam(ctx, teacherA, ExamInput{Title: "AI 等级考试 L1"}); !errors.Is(err, ErrExamDuplicate) {
		t.Errorf("同名考试应 ErrExamDuplicate, got %v", err)
	}

	// 时间校验。
	badTime := draft.ID
	if _, err := svc.UpdateExam(ctx, teacherA, false, badTime, ExamInput{
		Title:  "AI 等级考试 L1",
		OpenAt: ptrStr("2026-11-20T09:00:00+08:00"), CloseAt: ptrStr("2026-11-20T08:00:00+08:00"),
	}); !errors.Is(err, ErrExamInvalidTimes) {
		t.Errorf("close<=open 应 ErrExamInvalidTimes, got %v", err)
	}

	// 条目校验：他人题目 / 停用题目 / 重复题号。
	if _, err := svc.SetExamItems(ctx, teacherA, false, draft.ID, []ExamItemInput{
		{SourceQuestionID: qB.ID, OrderNo: 1, Score: 5},
	}); !errors.Is(err, ErrExamItemInvalid) {
		t.Errorf("他人题目应 ErrExamItemInvalid, got %v", err)
	}
	if _, err := svc.SetExamItems(ctx, teacherA, false, draft.ID, []ExamItemInput{
		{SourceQuestionID: q1.ID, OrderNo: 1, Score: 5},
		{SourceQuestionID: q2.ID, OrderNo: 1, Score: 3},
	}); !errors.Is(err, ErrExamItemInvalid) {
		t.Errorf("重复题号应 ErrExamItemInvalid, got %v", err)
	}

	// 正确设置条目 → 快照包含题面/选项/答案。
	items, err := svc.SetExamItems(ctx, teacherA, false, draft.ID, []ExamItemInput{
		{SourceQuestionID: q1.ID, OrderNo: 2, Score: 7},
		{SourceQuestionID: q2.ID, OrderNo: 1, Score: 3},
	})
	if err != nil {
		t.Fatalf("设置条目: %v", err)
	}
	if len(items) != 2 || items[0].OrderNo != 1 || items[0].CorrectAnswer != "true" {
		t.Errorf("条目快照错误: %+v", items)
	}
	if items[1].Options == nil || len(items[1].Options) != 3 {
		t.Errorf("单选条目选项快照缺失: %+v", items[1])
	}

	// 缺时间窗口不能发布。
	if _, err := svc.PublishExam(ctx, teacherA, false, draft.ID); !errors.Is(err, ErrExamMissingTimes) {
		t.Errorf("缺时间窗口应 ErrExamMissingTimes, got %v", err)
	}

	// 补全时间与时长后发布。
	future := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	futureEnd := time.Now().Add(26 * time.Hour).Format(time.RFC3339)
	if _, err := svc.UpdateExam(ctx, teacherA, false, draft.ID, ExamInput{
		Title: "AI 等级考试 L1", LevelCode: "L1",
		OpenAt: &future, CloseAt: &futureEnd, DurationSeconds: ptrInt(3600),
	}); err != nil {
		t.Fatalf("更新考试时间: %v", err)
	}
	published, err := svc.PublishExam(ctx, teacherA, false, draft.ID)
	if err != nil || published.Status != ExamStatusPublished || published.TotalScore != 10 || published.PublishedAt == nil {
		t.Fatalf("发布: %+v err=%v", published, err)
	}

	// 发布后不可改。
	if _, err := svc.UpdateExam(ctx, teacherA, false, draft.ID, ExamInput{Title: "改名"}); !errors.Is(err, ErrExamNotDraft) {
		t.Errorf("发布后改元信息应 ErrExamNotDraft, got %v", err)
	}
	if _, err := svc.SetExamItems(ctx, teacherA, false, draft.ID, []ExamItemInput{
		{SourceQuestionID: q1.ID, OrderNo: 1, Score: 5},
	}); !errors.Is(err, ErrExamNotDraft) {
		t.Errorf("发布后改条目应 ErrExamNotDraft, got %v", err)
	}
	if _, err := svc.PublishExam(ctx, teacherA, false, draft.ID); !errors.Is(err, ErrExamNotDraft) {
		t.Errorf("重复发布应 ErrExamNotDraft, got %v", err)
	}
	if err := svc.DisableExam(ctx, teacherA, false, draft.ID); !errors.Is(err, ErrExamNotDraft) {
		t.Errorf("发布后删除应 ErrExamNotDraft, got %v", err)
	}

	// 快照独立于题库：修改原题不影响已发布条目。
	if _, err := svc.UpdateQuestion(ctx, teacherA, false, q1.ID, QuestionInput{
		Type: "single", Stem: "单选一（已改）",
		Options:       []Option{{Key: "A", Text: "对"}, {Key: "B", Text: "错2"}, {Key: "C", Text: "错3"}},
		CorrectAnswer: "C", DefaultScore: 9,
	}); err != nil {
		t.Fatalf("修改原题: %v", err)
	}
	detail, err := svc.GetExam(ctx, teacherA, false, draft.ID)
	if err != nil {
		t.Fatalf("查询详情: %v", err)
	}
	for _, it := range detail.Items {
		if it.SourceQuestionID == q1.ID && (it.CorrectAnswer != "A" || it.Score != 7 || it.Stem != "单选一") {
			t.Errorf("发布快照被题库修改影响: %+v", it)
		}
	}

	// 越权：教师 B 不可见教师 A 的考试；管理员可见。
	if _, err := svc.GetExam(ctx, teacherB, false, draft.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("越权访问应 404, got %v", err)
	}
	if _, err := svc.GetExam(ctx, teacherB, true, draft.ID); err != nil {
		t.Errorf("管理员应可访问: %v", err)
	}

	// 草稿删除。
	d2, err := svc.CreateExam(ctx, teacherA, ExamInput{Title: "待删草稿"})
	if err != nil {
		t.Fatalf("创建草稿2: %v", err)
	}
	if err := svc.DisableExam(ctx, teacherA, false, d2.ID); err != nil {
		t.Fatalf("删除草稿: %v", err)
	}
	listA, err := svc.ListExams(ctx, teacherA, false, "draft")
	if err != nil || len(listA) != 0 {
		t.Errorf("已删草稿不应出现在 draft 列表: %v err=%v", listA, err)
	}
}

func ptrStr(s string) *string { return &s }
func ptrInt(v int) *int       { return &v }
