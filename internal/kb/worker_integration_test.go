package kb

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/model"
)

// fakeEmbedder 是测试用确定性向量化客户端（dim 固定），仅用于验证 worker 逻辑。
type fakeEmbedder struct {
	dim   int
	calls int
	fail  bool
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.fail {
		return nil, &model.UpstreamError{Code: "upstream_status", Status: 500, Message: "模拟上游故障"}
	}
	out := make([][]float32, len(texts))
	for i := range out {
		v := make([]float32, f.dim)
		for j := range v {
			v[j] = float32(j+1) / 10
		}
		out[i] = v
	}
	return out, nil
}

// createTestUser 创建临时教师用户。
func createTestUser(t *testing.T, pool *pgxpool.Pool, login string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (login_name, password_hash, role) VALUES ($1, 'x', 'teacher') RETURNING id`,
		login,
	).Scan(&id); err != nil {
		t.Fatalf("创建测试用户 %s: %v", login, err)
	}
	return id
}

// drainQueue 已由 schema 隔离取代：每个测试使用独立 schema，队列天然干净。

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestWorkerFlowIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userID := createTestUser(t, pool, "itest_w_"+suffix)

	kbRow, err := svc.CreateKB(ctx, userID, "Worker测试库", "")
	if err != nil {
		t.Fatalf("创建知识库: %v", err)
	}

	// 足够长、含段落与二级标题的正文，必然产生多片段。
	var b strings.Builder
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, "## 第%d节\n\n", i+1)
		b.WriteString(strings.Repeat(fmt.Sprintf("第%d节的正文内容，包含中文字符与标点。", i+1), 60))
		b.WriteString("\n\n")
	}
	content := []byte(b.String())
	doc, _, err := svc.UploadDocument(ctx, userID, false, kbRow.ID, "long.md", "text/markdown", content)
	if err != nil {
		t.Fatalf("上传文档: %v", err)
	}

	worker := svc.NewWorker(&fakeEmbedder{dim: 4}, "fake-embed", time.Second, newTestLogger())
	more, err := worker.ProcessOnce(ctx)
	if err != nil || !more {
		t.Fatalf("处理一次: more=%v err=%v", more, err)
	}

	var status string
	var reason *string
	if err := pool.QueryRow(ctx,
		`SELECT status, failure_reason FROM documents WHERE id = $1`, doc.ID).Scan(&status, &reason); err != nil {
		t.Fatalf("查询文档状态: %v", err)
	}
	if status != StatusReady {
		t.Fatalf("文档应为 ready, got %s reason=%v", status, reason)
	}

	want := len(SplitText(string(content), kbRow.ChunkSize, kbRow.ChunkOverlap))
	var chunkCount int
	var dim int
	if err := pool.QueryRow(ctx,
		`SELECT count(*), (SELECT vector_dims(embedding) FROM document_chunks WHERE document_id = $1 LIMIT 1)
		 FROM document_chunks WHERE document_id = $1`, doc.ID).Scan(&chunkCount, &dim); err != nil {
		t.Fatalf("查询片段: %v", err)
	}
	if chunkCount != want || want < 2 {
		t.Errorf("片段数 %d 与期望 %d 不符", chunkCount, want)
	}
	if dim != 4 {
		t.Errorf("向量维度 = %d", dim)
	}

	// 重试替换：置为 failed → 重试 → 再处理，片段数不重复。
	if _, err := pool.Exec(ctx, `UPDATE documents SET status = 'failed', failure_reason = '模拟' WHERE id = $1`, doc.ID); err != nil {
		t.Fatalf("置为 failed: %v", err)
	}
	if _, err := svc.RetryDocument(ctx, userID, false, kbRow.ID, doc.ID); err != nil {
		t.Fatalf("重试: %v", err)
	}
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("再次处理: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM document_chunks WHERE document_id = $1`, doc.ID).Scan(&chunkCount); err != nil {
		t.Fatalf("查询片段: %v", err)
	}
	if chunkCount != want {
		t.Errorf("重试后片段数应替换而非追加: %d != %d", chunkCount, want)
	}

	// 片段查询接口：教师本人可见，他人不可见。
	views, err := svc.ListChunks(ctx, userID, false, kbRow.ID, doc.ID)
	if err != nil || len(views) != want {
		t.Errorf("ListChunks: len=%d err=%v", len(views), err)
	}
	otherID := createTestUser(t, pool, "itest_w_flow_other_"+suffix)
	if _, err := svc.ListChunks(ctx, otherID, false, kbRow.ID, doc.ID); !isNotFound(err) {
		t.Errorf("他人访问应 404, got %v", err)
	}
}

func TestWorkerFailureAndDisableGuardIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userID := createTestUser(t, pool, "itest_w2_"+suffix)

	kbRow, err := svc.CreateKB(ctx, userID, "Worker失败测试库", "")
	if err != nil {
		t.Fatalf("创建知识库: %v", err)
	}

	// 上游故障 → failed 且有可读原因；重试换正常客户端后成功。
	badWorker := svc.NewWorker(&fakeEmbedder{dim: 4, fail: true}, "fake-embed", time.Second, newTestLogger())
	doc, _, err := svc.UploadDocument(ctx, userID, false, kbRow.ID, "a.md", "text/markdown", []byte("第一段内容。\n\n第二段内容。"))
	if err != nil {
		t.Fatalf("上传: %v", err)
	}
	if _, err := badWorker.ProcessOnce(ctx); err == nil {
		t.Fatal("上游故障应返回错误")
	}
	var status string
	var reason *string
	if err := pool.QueryRow(ctx,
		`SELECT status, failure_reason FROM documents WHERE id = $1`, doc.ID).Scan(&status, &reason); err != nil {
		t.Fatalf("查询状态: %v", err)
	}
	if status != StatusFailed || reason == nil || !strings.Contains(*reason, "向量服务错误") {
		t.Errorf("失败状态/原因错误: %s %v", status, reason)
	}
	if _, _, err := svc.GetKB(ctx, userID, false, kbRow.ID); err != nil {
		t.Fatalf("查询文档列表: %v", err)
	}

	if _, err := svc.RetryDocument(ctx, userID, false, kbRow.ID, doc.ID); err != nil {
		t.Fatalf("重试: %v", err)
	}
	goodWorker := svc.NewWorker(&fakeEmbedder{dim: 4}, "fake-embed", time.Second, newTestLogger())
	if _, err := goodWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("修复后处理: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM documents WHERE id = $1`, doc.ID).Scan(&status); err != nil {
		t.Fatalf("查询状态: %v", err)
	}
	if status != StatusReady {
		t.Errorf("重试后应为 ready, got %s", status)
	}

	// 停用保护：领取后停用，处理结果被丢弃且不回置 ready。
	doc2, _, err := svc.UploadDocument(ctx, userID, false, kbRow.ID, "b.md", "text/markdown", []byte("另一篇内容。\n\n第二段。"))
	if err != nil {
		t.Fatalf("上传: %v", err)
	}
	claimed, err := goodWorker.claimDocument(ctx)
	if err != nil || claimed == nil {
		t.Fatalf("领取文档: %v %v", claimed, err)
	}
	if claimed.ID != doc2.ID {
		// 队列里可能存在其他 queued 文档；把领取到的正好用于测试也可。
		t.Logf("领取到其他文档 %d（期望 %d）", claimed.ID, doc2.ID)
	}
	if _, err := pool.Exec(ctx, `UPDATE documents SET status = 'disabled' WHERE id = $1`, claimed.ID); err != nil {
		t.Fatalf("停用: %v", err)
	}
	if err := goodWorker.processDocument(ctx, claimed); err != nil {
		t.Fatalf("处理已停用文档不应报错: %v", err)
	}
	var chunks int
	if err := pool.QueryRow(ctx, `SELECT status FROM documents WHERE id = $1`, claimed.ID).Scan(&status); err != nil {
		t.Fatalf("查询状态: %v", err)
	}
	if status != StatusDisabled {
		t.Errorf("停用文档不应被回置 ready, got %s", status)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM document_chunks WHERE document_id = $1`, claimed.ID).Scan(&chunks); err != nil {
		t.Fatalf("查询片段: %v", err)
	}
	if chunks != 0 {
		t.Errorf("停用文档不应写入片段, got %d", chunks)
	}

	// 重启恢复：processing 状态标记 failed。
	if _, err := pool.Exec(ctx, `UPDATE documents SET status = 'embedding' WHERE id = $1 AND status <> 'disabled'`, doc.ID); err != nil {
		t.Fatalf("模拟中断: %v", err)
	}
	n, err := goodWorker.RecoverStale(ctx)
	if err != nil || n == 0 {
		t.Fatalf("恢复: n=%d err=%v", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM documents WHERE id = $1`, doc.ID).Scan(&status); err != nil {
		t.Fatalf("查询状态: %v", err)
	}
	if status != StatusFailed {
		t.Errorf("中断文档应为 failed, got %s", status)
	}
}
