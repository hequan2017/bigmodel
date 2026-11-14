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

	// 正常检索（显式向量模式）：只返回 ready 文档片段，引用可回溯。
	cands, err := svc.Search(ctx, userID, false, kbRow.ID,
		SearchRequest{Question: "什么是检索增强生成", TopK: 3, Mode: ModeVector}, good, "fake-embed")
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

func TestFullTextAndHybridIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userID := createTestUser(t, pool, "itest_fts_"+suffix)

	good := &fakeEmbedder{dim: 4}
	worker := svc.NewWorker(good, "fake-embed", time.Second, newTestLogger())

	kbRow, err := svc.CreateKB(ctx, userID, "全文检索测试库", "")
	if err != nil {
		t.Fatalf("创建知识库: %v", err)
	}
	doc, _, err := svc.UploadDocument(ctx, userID, false, kbRow.ID, "fts.md", "text/markdown",
		[]byte("# 全文检索通道\n\n检索增强生成是一种结合信息检索与大语言模型的教学技术。\n\n"+
			strings.Repeat("补充说明细节。", 40)))
	if err != nil {
		t.Fatalf("上传文档: %v", err)
	}
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("处理文档: %v", err)
	}

	// worker 处理后所有片段都应写入 fts。
	var nullFts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM document_chunks`).Scan(&nullFts); err != nil {
		t.Fatalf("查询片段: %v", err)
	}
	if nullFts == 0 {
		t.Fatal("测试前置失败：片段为空")
	}

	// 全文检索命中：按词项相关性排序，不依赖 Embedding。
	cands, err := svc.Search(ctx, userID, false, kbRow.ID,
		SearchRequest{Question: "检索增强生成", TopK: 3, Mode: ModeFullText}, good, "fake-embed")
	if err != nil {
		t.Fatalf("全文检索: %v", err)
	}
	if len(cands) == 0 {
		t.Fatal("全文检索应命中")
	}
	for _, c := range cands {
		if c.Channel != ChannelFullText || c.Score <= 0 {
			t.Errorf("全文候选元数据错误: %+v", c)
		}
		if c.DocumentID != doc.ID {
			t.Errorf("候选文档不符: %d != %d", c.DocumentID, doc.ID)
		}
	}

	// 全文检索不命中：词元均未出现在语料中。
	noHit, err := svc.Search(ctx, userID, false, kbRow.ID,
		SearchRequest{Question: "量子区块链元宇宙xyzq", TopK: 3, Mode: ModeFullText}, good, "fake-embed")
	if err != nil {
		t.Fatalf("全文检索（无命中）: %v", err)
	}
	if len(noHit) != 0 {
		t.Errorf("不相关查询应 0 命中, got %d", len(noHit))
	}

	// 混合召回：默认模式即 hybrid；按 ChunkID 去重，通道标识与 RRF 得分正确。
	hyb, stats, err := svc.SearchForApp(ctx, kbRow.ID,
		SearchRequest{Question: "检索增强生成的原理", TopK: 5}, good, "fake-embed")
	if err != nil {
		t.Fatalf("混合召回: %v", err)
	}
	if len(hyb) == 0 {
		t.Fatal("混合召回应有候选")
	}
	seen := map[int64]int{}
	for _, c := range hyb {
		seen[c.ChunkID]++
		if c.Channel != ChannelHybrid || c.RrfScore <= 0 || c.Rank != len(seen) {
			t.Errorf("混合候选元数据错误: %+v", c)
		}
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("混合召回应按 ChunkID 去重, chunk %d 出现 %d 次", id, n)
		}
	}
	if len(stats) != 2 {
		t.Fatalf("应返回两路通道统计, got %d", len(stats))
	}
	for _, st := range stats {
		if (st.Channel == ChannelVector || st.Channel == ChannelFullText) && st.Candidates > 0 {
			continue
		}
		t.Errorf("通道统计缺失或为空: %+v", st)
	}

	// 降级：Embedding 未配置时 hybrid 退化为纯全文检索并记录原因。
	dg, dgStats, err := svc.SearchForApp(ctx, kbRow.ID,
		SearchRequest{Question: "检索增强生成", TopK: 3}, nil, "")
	if err != nil {
		t.Fatalf("混合降级: %v", err)
	}
	if len(dg) == 0 || dg[0].Channel != ChannelFullText {
		t.Errorf("降级应返回全文候选, got %+v", dg)
	}
	degraded := false
	for _, st := range dgStats {
		if st.Channel == ChannelVector && st.DegradeReason != "" {
			degraded = true
		}
	}
	if !degraded {
		t.Errorf("降级原因未记录: %+v", dgStats)
	}

	// 重建全文索引：不调用 Embedding，重建后仍命中。
	before := good.calls
	n, err := svc.ReindexFullText(ctx, userID, false, kbRow.ID)
	if err != nil {
		t.Fatalf("重建全文索引: %v", err)
	}
	if n <= 0 {
		t.Errorf("重建应覆盖片段, got %d", n)
	}
	if good.calls != before {
		t.Errorf("重建不应调用 Embedding: %d -> %d", before, good.calls)
	}
	after, err := svc.Search(ctx, userID, false, kbRow.ID,
		SearchRequest{Question: "检索增强生成", TopK: 3, Mode: ModeFullText}, good, "fake-embed")
	if err != nil {
		t.Fatalf("重建后全文检索: %v", err)
	}
	if len(after) == 0 {
		t.Error("重建后全文检索应命中")
	}

	// 非法模式。
	if _, err := svc.Search(ctx, userID, false, kbRow.ID,
		SearchRequest{Question: "任意", Mode: "bogus"}, good, "fake-embed"); !errors.Is(err, ErrInvalidMode) {
		t.Errorf("非法模式应 ErrInvalidMode, got %v", err)
	}
}
