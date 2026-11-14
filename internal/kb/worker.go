package kb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/hequan2017/bigmodel/internal/model"
)

// Worker 是单进程文档处理循环：领取 queued 文档，
// 解析切分 → 批量向量化 → 短事务落库并置 ready；
// 失败落 failure_reason 供人工重试；停用文档不会被回置 ready。
type Worker struct {
	svc        *Service
	embed      model.EmbeddingClient
	embedModel string
	interval   time.Duration
	batchSize  int
	log        *slog.Logger
}

// NewWorker 构造文档处理 worker；embed 为已配置的 Embedding 客户端。
func (s *Service) NewWorker(embed model.EmbeddingClient, embedModel string, interval time.Duration, log *slog.Logger) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Worker{
		svc:        s,
		embed:      embed,
		embedModel: embedModel,
		interval:   interval,
		batchSize:  16,
		log:        log,
	}
}

// Run 周期排空处理队列，直到 ctx 结束。
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		for {
			more, err := w.ProcessOnce(ctx)
			if err != nil {
				w.log.Warn("文档处理循环异常", "error", err)
				break
			}
			if !more {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ProcessOnce 领取并处理一个 queued 文档；返回是否仍可能有待处理文档。
// 单个文档的处理错误会在日志记录后作为第二个返回值透传，不中断队列。
func (w *Worker) ProcessOnce(ctx context.Context) (bool, error) {
	claimed, err := w.claimDocument(ctx)
	if err != nil {
		return false, err
	}
	if claimed == nil {
		return false, nil
	}
	if err := w.processDocument(ctx, claimed); err != nil {
		w.log.Warn("文档处理失败", "document_id", claimed.ID, "error", err)
		return true, err
	}
	return true, nil
}

// RecoverStale 重启恢复：把上次中断的 processing 类状态标记为 failed，
// 允许人工重试；不宣称自动断点续传。
func (w *Worker) RecoverStale(ctx context.Context) (int64, error) {
	tag, err := w.svc.pool.Exec(ctx, `
		UPDATE documents SET status = 'failed',
		       failure_reason = '服务重启导致处理中断，请重试',
		       updated_at = now()
		WHERE status IN ('parsing', 'embedding')`)
	if err != nil {
		return 0, fmt.Errorf("恢复中断文档: %w", err)
	}
	return tag.RowsAffected(), nil
}

type claimedDoc struct {
	ID         int64
	KBID       int64
	FilePath   string
	RetryCount int
}

func (w *Worker) claimDocument(ctx context.Context) (*claimedDoc, error) {
	var c claimedDoc
	err := w.svc.pool.QueryRow(ctx, `
		WITH next AS (
			SELECT id FROM documents WHERE status = 'queued'
			ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		UPDATE documents d SET status = 'parsing', updated_at = now()
		FROM next WHERE d.id = next.id
		RETURNING d.id, d.knowledge_base_id, d.file_path, d.retry_count`,
	).Scan(&c.ID, &c.KBID, &c.FilePath, &c.RetryCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("领取文档: %w", err)
	}
	return &c, nil
}

func (w *Worker) processDocument(ctx context.Context, c *claimedDoc) error {
	// parsing：读取原始文件并切分。
	raw, err := os.ReadFile(filepath.Join(w.svc.dataDir, filepath.FromSlash(c.FilePath)))
	if err != nil {
		return w.fail(ctx, c.ID, fmt.Sprintf("原始文件读取失败: %v", err))
	}

	var chunkSize, chunkOverlap int
	if err := w.svc.pool.QueryRow(ctx,
		`SELECT chunk_size, chunk_overlap FROM knowledge_bases WHERE id = $1`, c.KBID,
	).Scan(&chunkSize, &chunkOverlap); err != nil {
		return w.fail(ctx, c.ID, fmt.Sprintf("读取切分参数失败: %v", err))
	}

	chunks := SplitText(string(raw), chunkSize, chunkOverlap)
	if len(chunks) == 0 {
		return w.fail(ctx, c.ID, "切分后无有效片段")
	}
	if len(chunks) > MaxChunksPerDoc {
		return w.fail(ctx, c.ID, fmt.Sprintf("片段数 %d 超过单文档上限 %d", len(chunks), MaxChunksPerDoc))
	}

	// embedding：批量向量化；外部模型调用不放在数据库事务内。
	if _, err := w.svc.pool.Exec(ctx, `
		UPDATE documents SET status = 'embedding', updated_at = now()
		WHERE id = $1 AND status = 'parsing'`, c.ID); err != nil {
		return fmt.Errorf("切换 embedding 状态: %w", err)
	}

	vectors := make([][]float32, 0, len(chunks))
	dimension := -1
	for start := 0; start < len(chunks); start += w.batchSize {
		end := start + w.batchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := make([]string, 0, end-start)
		for _, ch := range chunks[start:end] {
			batch = append(batch, ch.Content)
		}
		vecs, err := w.embed.Embed(ctx, batch)
		if err != nil {
			return w.failUpstream(ctx, c.ID, err)
		}
		if len(vecs) != len(batch) {
			return w.fail(ctx, c.ID, fmt.Sprintf("向量数量不符：期望 %d，返回 %d", len(batch), len(vecs)))
		}
		for _, v := range vecs {
			if dimension == -1 {
				dimension = len(v)
			}
			if len(v) != dimension {
				return w.fail(ctx, c.ID, "批次间向量维度不一致")
			}
		}
		vectors = append(vectors, vecs...)
	}

	// 短事务落库：重新检查状态（停用后禁止回置 ready），
	// 先删除失败尝试遗留的旧片段再写入，保证重试不产生重复片段。
	tx, err := w.svc.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启落库事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current string
	if err := tx.QueryRow(ctx,
		`SELECT status FROM documents WHERE id = $1 FOR UPDATE`, c.ID).Scan(&current); err != nil {
		return fmt.Errorf("复查文档状态: %w", err)
	}
	if current != StatusEmbedding {
		// 已被停用或状态漂移：放弃本次结果，保持当前状态。
		w.log.Info("文档状态已变化，放弃处理结果", "document_id", c.ID, "current", current)
		return nil
	}

	if _, err := tx.Exec(ctx, `DELETE FROM document_chunks WHERE document_id = $1`, c.ID); err != nil {
		return fmt.Errorf("清理旧片段: %w", err)
	}
	for i, ch := range chunks {
		if _, err := tx.Exec(ctx, `
			INSERT INTO document_chunks (document_id, chunk_index, content, start_offset, end_offset, heading, embedding, embedding_model, fts)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, to_tsvector('simple', $9))`,
			c.ID, ch.Index, ch.Content, ch.StartOffset, ch.EndOffset, ch.Heading,
			pgvector.NewVector(vectors[i]), w.embedModel, SegmentText(ch.Content),
		); err != nil {
			return fmt.Errorf("写入片段 %d: %w", ch.Index, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE documents SET status = 'ready', failure_reason = NULL, updated_at = now()
		WHERE id = $1`, c.ID); err != nil {
		return fmt.Errorf("置为 ready: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交落库事务: %w", err)
	}

	w.log.Info("文档处理完成",
		"document_id", c.ID,
		"chunks", len(chunks),
		"dimension", dimension,
		"retry_count", c.RetryCount,
	)
	return nil
}

// fail 把处理中的文档标记为失败并记录原因；已停用的文档不会被改动。
func (w *Worker) fail(ctx context.Context, docID int64, reason string) error {
	if err := w.markFailed(ctx, docID, reason); err != nil {
		return err
	}
	return fmt.Errorf("文档处理失败: %s", reason)
}

func (w *Worker) failUpstream(ctx context.Context, docID int64, err error) error {
	reason := err.Error()
	var ue *model.UpstreamError
	if errors.As(err, &ue) {
		reason = fmt.Sprintf("向量服务错误 [%s]: %s", ue.Code, ue.Message)
	}
	return w.fail(ctx, docID, reason)
}

func (w *Worker) markFailed(ctx context.Context, docID int64, reason string) error {
	if _, err := w.svc.pool.Exec(ctx, `
		UPDATE documents SET status = 'failed', failure_reason = $2, updated_at = now()
		WHERE id = $1 AND status IN ('parsing', 'embedding')`,
		docID, truncateReason(reason),
	); err != nil {
		return fmt.Errorf("标记失败: %w", err)
	}
	return nil
}

func truncateReason(reason string) string {
	rs := []rune(reason)
	if len(rs) > 500 {
		return string(rs[:500]) + "…"
	}
	return reason
}

// ChunkView 是片段的对外展示结构；不包含向量本体。
type ChunkView struct {
	Index       int    `json:"index"`
	Content     string `json:"content"`
	StartOffset int    `json:"start_offset"`
	EndOffset   int    `json:"end_offset"`
	Heading     string `json:"heading"`
}

// ListChunks 返回文档片段（文本与定位），供教师调试引用回溯。
func (s *Service) ListChunks(ctx context.Context, userID int64, isAdmin bool, kbID, docID int64) ([]ChunkView, error) {
	if _, err := s.checkAccess(ctx, userID, isAdmin, kbID); err != nil {
		return nil, err
	}
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM documents WHERE id = $1 AND knowledge_base_id = $2)`,
		docID, kbID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("查询文档: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}

	rows, err := s.pool.Query(ctx, `
		SELECT chunk_index, content, start_offset, end_offset, heading
		FROM document_chunks WHERE document_id = $1 ORDER BY chunk_index`, docID)
	if err != nil {
		return nil, fmt.Errorf("查询片段: %w", err)
	}
	defer rows.Close()

	out := []ChunkView{}
	for rows.Next() {
		var v ChunkView
		if err := rows.Scan(&v.Index, &v.Content, &v.StartOffset, &v.EndOffset, &v.Heading); err != nil {
			return nil, fmt.Errorf("读取片段: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ChunksForDoc 返回文档片段（文本与定位），不做用户级校验——
// 供已发布问答应用展示引用原文使用：授权由"应用已发布"建立（§4.3），
// 调用方必须先确认应用已发布且文档属于该应用绑定的知识库。
func (s *Service) ChunksForDoc(ctx context.Context, kbID, docID int64) ([]ChunkView, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM documents d
			JOIN knowledge_bases k ON k.id = d.knowledge_base_id
			WHERE d.id = $1 AND d.knowledge_base_id = $2 AND d.status = 'ready')`,
		docID, kbID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("查询文档: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}

	rows, err := s.pool.Query(ctx, `
		SELECT chunk_index, content, start_offset, end_offset, heading
		FROM document_chunks WHERE document_id = $1 ORDER BY chunk_index`, docID)
	if err != nil {
		return nil, fmt.Errorf("查询片段: %w", err)
	}
	defer rows.Close()

	out := []ChunkView{}
	for rows.Next() {
		var v ChunkView
		if err := rows.Scan(&v.Index, &v.Content, &v.StartOffset, &v.EndOffset, &v.Heading); err != nil {
			return nil, fmt.Errorf("读取片段: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
