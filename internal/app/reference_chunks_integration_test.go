package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

)

func TestReferenceChunksIntegration(t *testing.T) {
	svc, kbSvc, pool := newTestEnv(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	teacherID := createAppTestUser(t, pool, "itest_ref_t_"+suffix)
	studentID := createStudent(t, pool, "itest_ref_s_"+suffix)
	embed := &fakeEmbedder{dim: 4}

	if _, err := pool.Exec(ctx, `
		INSERT INTO model_profiles (name, kind, model_name, base_url, credential_ref)
		VALUES ($1, 'embedding', 'itest-model', 'http://127.0.0.1:1', 'ITEST')`,
		"itest-ref-emb-"+suffix); err != nil {
		t.Fatalf("创建测试档案: %v", err)
	}

	kbRow, err := kbSvc.CreateKB(ctx, teacherID, "引用原文测试库", "")
	if err != nil {
		t.Fatalf("创建知识库: %v", err)
	}
	// 一份 ready 文档。
	doc, _, err := kbSvc.UploadDocument(ctx, teacherID, false, kbRow.ID, "原文.md", "text/markdown",
		[]byte(strings.Repeat("引用原文测试段落内容。", 40)))
	if err != nil {
		t.Fatalf("上传: %v", err)
	}
	worker := kbSvc.NewWorker(embed, "fake-embed", time.Second, testLogger())
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("处理: %v", err)
	}

	a, err := svc.CreateDraft(ctx, teacherID, "引用原文应用", AppConfig{
		KnowledgeBaseID: kbRow.ID, Prompt: "{{question}}", TopK: 5, HistoryTurns: 1})
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	if _, err := svc.Publish(ctx, teacherID, "teacher", false, a.ID); err != nil {
		t.Fatalf("发布: %v", err)
	}

	// 学生通过已发布应用查看引用原文。
	chunks, err := svc.ReferenceChunks(ctx, studentID, "student", false, a.ID, doc.ID)
	if err != nil {
		t.Fatalf("学生查看引用原文: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("应返回至少一个片段")
	}

	// 草稿应用（未发布）→ 学生 404。
	draft, err := svc.CreateDraft(ctx, teacherID, "引用原文草稿", AppConfig{
		KnowledgeBaseID: kbRow.ID, Prompt: "{{question}}", TopK: 5, HistoryTurns: 1})
	if err != nil {
		t.Fatalf("创建草稿: %v", err)
	}
	if _, err := svc.ReferenceChunks(ctx, studentID, "student", false, draft.ID, doc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("草稿应用应 404, got %v", err)
	}

	// 文档不属于该应用绑定知识库 → 404。
	kbOther, err := kbSvc.CreateKB(ctx, teacherID, "其他库", "")
	if err != nil {
		t.Fatalf("创建其他库: %v", err)
	}
	docOther, _, err := kbSvc.UploadDocument(ctx, teacherID, false, kbOther.ID, "other.md", "text/markdown",
		[]byte("其他库文档内容。"))
	if err != nil {
		t.Fatalf("上传 other: %v", err)
	}
	if _, err := svc.ReferenceChunks(ctx, studentID, "student", false, a.ID, docOther.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("跨库文档应 404, got %v", err)
	}

	// 教师（本人）按草稿配置查看 → OK。
	if _, err := svc.ReferenceChunks(ctx, teacherID, "teacher", false, a.ID, doc.ID); err != nil {
		t.Errorf("教师查看应成功: %v", err)
	}

}
