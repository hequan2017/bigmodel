package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSearchIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userID := createTestUser(t, pool, "itest_s_"+suffix)
	otherID := createTestUser(t, pool, "itest_s_other_"+suffix)

	good := &fakeEmbedder{dim: 4}
	worker := svc.NewWorker(good, "fake-embed", time.Second, newTestLogger())

	kbRow, err := svc.CreateKB(ctx, userID, "检索测试库", "")
	if err != nil {
		t.Fatalf("创建知识库: %v", err)
	}

	// 两份长文档 → 多片段；其中第二份随后被停用。
	var b strings.Builder
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, "## 第%d节\n\n", i+1)
		b.WriteString(strings.Repeat(fmt.Sprintf("第%d节讲解检索增强生成的原理与实践。", i+1), 60))
		b.WriteString("\n\n")
	}
	doc1, _, err := svc.UploadDocument(ctx, userID, false, kbRow.ID, "rag.md", "text/markdown", []byte(b.String()))
	if err != nil {
		t.Fatalf("上传文档1: %v", err)
	}
	doc2, _, err := svc.UploadDocument(ctx, userID, false, kbRow.ID, "extra.md", "text/markdown",
		[]byte("附录内容，将被停用。"+strings.Repeat("附录补充说明。", 80)))
	if err != nil {
		t.Fatalf("上传文档2: %v", err)
	}
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("处理文档1: %v", err)
	}
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("处理文档2: %v", err)
	}
	if err := svc.DisableDocument(ctx, userID, false, kbRow.ID, doc2.ID); err != nil {
		t.Fatalf("停用文档2: %v", err)
	}

	// 正常检索：只返回 ready 文档片段，引用可回溯。
	cands, err := svc.Search(ctx, userID, false, kbRow.ID,
		SearchRequest{Question: "什么是检索增强生成", TopK: 3}, good, "fake-embed")
	if err != nil {
		t.Fatalf("检索: %v", err)
	}
	if len(cands) == 0 || len(cands) > 3 {
		t.Fatalf("候选数 %d 不在 [1,3]", len(cands))
	}
	for i, c := range cands {
		if c.Rank != i+1 || c.Channel != ChannelVector || c.EmbeddingModel != "fake-embed" {
			t.Errorf("候选元数据错误: %+v", c)
		}
		if c.DocumentID == doc2.ID {
			t.Error("已停用文档不应出现在候选中")
		}
		if c.Source.EndOffset <= c.Source.StartOffset {
			t.Errorf("引用定位非法: %+v", c.Source)
		}
		if c.Content == "" {
			t.Error("候选内容为空")
		}
	}

	// 维度不匹配：查询向量 8 维 vs 片段 4 维。
	bad := &fakeEmbedder{dim: 8}
	if _, err := svc.Search(ctx, userID, false, kbRow.ID,
		SearchRequest{Question: "维度测试", TopK: 3}, bad, "fake-embed-8"); !errors.Is(err, ErrDimensionMismatch) {
		t.Errorf("维度不匹配应返回 ErrDimensionMismatch, got %v", err)
	}

	// 越权访问：统一 404。
	if _, err := svc.Search(ctx, otherID, false, kbRow.ID,
		SearchRequest{Question: "越权"}, good, "fake-embed"); !errors.Is(err, ErrNotFound) {
		t.Errorf("越权应 404, got %v", err)
	}

	// 问题校验。
	if _, err := svc.Search(ctx, userID, false, kbRow.ID,
		SearchRequest{Question: "   "}, good, "fake-embed"); !errors.Is(err, ErrInvalidQuestion) {
		t.Errorf("空问题应 ErrInvalidQuestion, got %v", err)
	}
	long := strings.Repeat("问", MaxQuestionRunes+1)
	if _, err := svc.Search(ctx, userID, false, kbRow.ID,
		SearchRequest{Question: long}, good, "fake-embed"); !errors.Is(err, ErrInvalidQuestion) {
		t.Errorf("超长问题应 ErrInvalidQuestion, got %v", err)
	}

	// topK 上限与默认值不产生错误。
	if _, err := svc.Search(ctx, userID, false, kbRow.ID,
		SearchRequest{Question: "要点", TopK: 999}, good, "fake-embed"); err != nil {
		t.Errorf("超限 topK 应收敛而不报错: %v", err)
	}

	_ = doc1
}
