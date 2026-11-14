package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestListRunsIntegration(t *testing.T) {
	svc, kbSvc, pool := newTestEnv(t)
	ctx := context.Background()

	suffix := "runs_" + t.Name()
	teacherID := createAppTestUser(t, pool, "itest_runs_t_"+suffix)
	studentID := createStudent(t, pool, "itest_runs_s_"+suffix)
	embed := &fakeEmbedder{dim: 4}

	if _, err := pool.Exec(ctx, `
		INSERT INTO model_profiles (name, kind, model_name, base_url, credential_ref)
		VALUES ($1, 'embedding', 'itest-model', 'http://127.0.0.1:1', 'ITEST')`,
		"itest-runs-emb-"+suffix); err != nil {
		t.Fatalf("创建测试档案: %v", err)
	}

	kbRow, err := kbSvc.CreateKB(ctx, teacherID, "运行记录测试库", "")
	if err != nil {
		t.Fatalf("创建知识库: %v", err)
	}
	seedReadyDoc(t, kbSvc, kbRow.ID, teacherID, embed)

	a, err := svc.CreateDraft(ctx, teacherID, "运行记录应用", AppConfig{
		KnowledgeBaseID: kbRow.ID, Prompt: "{{question}}", TopK: 5, HistoryTurns: 0})
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	if _, err := svc.Publish(ctx, teacherID, "teacher", false, a.ID); err != nil {
		t.Fatalf("发布: %v", err)
	}

	// 学生两次成功运行。
	chat := &fakeChat{}
	for _, q := range []string{"第一问", "第二问"} {
		if err := svc.RunApp(ctx, RunParams{
			UserID: studentID, Role: "student", AppID: a.ID, Question: q,
			Chat: chat, ChatModel: "mock-chat", Embed: embed, EmbedModel: "fake-embed",
		}, func(Event) error { return nil }); err != nil {
			t.Fatalf("运行 %s: %v", q, err)
		}
	}

	// 教师查看运行记录（本人应用）。
	runs, err := svc.ListRuns(ctx, teacherID, false, a.ID, 50)
	if err != nil {
		t.Fatalf("查询运行记录: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("应 2 条运行, got %d", len(runs))
	}
	for _, r := range runs {
		if r.Status != "succeeded" || r.Mode != "published" || r.LoginName != fmt.Sprintf("itest_runs_s_%s", suffix) {
			t.Errorf("运行记录字段错误: %+v", r)
		}
	}

	// 越权：其他教师 404。
	other := createAppTestUser(t, pool, "itest_runs_o_"+suffix)
	if _, err := svc.ListRuns(ctx, other, false, a.ID, 50); !errors.Is(err, ErrNotFound) {
		t.Errorf("越权应 404, got %v", err)
	}

	// 学生无权查看运行记录列表（服务层同样拒绝：非 owner）。
	if _, err := svc.ListRuns(ctx, studentID, false, a.ID, 50); !errors.Is(err, ErrNotFound) {
		t.Errorf("学生查看运行记录应 404, got %v", err)
	}
}
