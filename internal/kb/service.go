package kb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service 提供知识库与文档管理；file_path 使用服务端生成的相对路径。
type Service struct {
	pool    *pgxpool.Pool
	dataDir string
}

// NewService 构造知识库服务。
func NewService(pool *pgxpool.Pool, dataDir string) *Service {
	return &Service{pool: pool, dataDir: dataDir}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// CreateKB 创建知识库，固定绑定当前可用的 Embedding 模型档案。
// 未配置 Embedding 模型时明确拒绝，不静默允许后续必然失败的库。
func (s *Service) CreateKB(ctx context.Context, ownerID int64, name, description string) (*KB, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" {
		return nil, fmt.Errorf("%w: name 不能为空", ErrInvalidName)
	}
	if len([]rune(name)) > 200 {
		return nil, fmt.Errorf("%w: name 过长", ErrInvalidName)
	}
	if len([]rune(description)) > 2000 {
		return nil, fmt.Errorf("%w: description 过长", ErrInvalidName)
	}

	var modelID int64
	var dimension *int
	err := s.pool.QueryRow(ctx, `
		SELECT id, embedding_dimension FROM model_profiles
		WHERE kind = 'embedding' AND enabled
		ORDER BY id LIMIT 1`).Scan(&modelID, &dimension)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEmbeddingNotConfigured
	}
	if err != nil {
		return nil, fmt.Errorf("查询 Embedding 模型: %w", err)
	}

	kbRow := &KB{
		OwnerID:            ownerID,
		Name:               name,
		Description:        description,
		EmbeddingModelID:   &modelID,
		EmbeddingDimension: dimension,
		ChunkSize:          600,
		ChunkOverlap:       100,
		Status:             "active",
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (owner_id, name, description, embedding_model_id, embedding_dimension)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at`,
		ownerID, name, description, &modelID, dimension,
	).Scan(&kbRow.ID, &kbRow.CreatedAt, &kbRow.UpdatedAt)
	if isUniqueViolation(err) {
		return nil, ErrDuplicateName
	}
	if err != nil {
		return nil, fmt.Errorf("创建知识库: %w", err)
	}
	return kbRow, nil
}

// ListKBs 列出知识库：教师只看本人，管理员看全部。
func (s *Service) ListKBs(ctx context.Context, userID int64, isAdmin bool) ([]KBWithCount, error) {
	where := "WHERE k.owner_id = $1"
	if isAdmin {
		where = ""
	}
	rows, err := s.pool.Query(ctx, `
		SELECT k.id, k.owner_id, k.name, k.description, k.embedding_model_id, mp.name,
		       k.embedding_dimension, k.chunk_size, k.chunk_overlap, k.status, k.created_at, k.updated_at,
		       (SELECT count(*) FROM documents d
		        WHERE d.knowledge_base_id = k.id AND d.status <> 'disabled') AS doc_count
		FROM knowledge_bases k
		LEFT JOIN model_profiles mp ON mp.id = k.embedding_model_id
		`+where+`
		ORDER BY k.id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("查询知识库: %w", err)
	}
	defer rows.Close()

	out := []KBWithCount{}
	for rows.Next() {
		var item KBWithCount
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.Name, &item.Description,
			&item.EmbeddingModelID, &item.EmbeddingModelName, &item.EmbeddingDimension,
			&item.ChunkSize, &item.ChunkOverlap, &item.Status, &item.CreatedAt, &item.UpdatedAt,
			&item.DocCount); err != nil {
			return nil, fmt.Errorf("读取知识库: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// checkAccess 校验知识库存在且可访问；无权限统一返回 ErrNotFound，减少资源枚举。
func (s *Service) checkAccess(ctx context.Context, userID int64, isAdmin bool, kbID int64) (*KB, error) {
	var kbRow KB
	err := s.pool.QueryRow(ctx, `
		SELECT id, owner_id, name, description, embedding_model_id, embedding_dimension,
		       chunk_size, chunk_overlap, status, created_at, updated_at
		FROM knowledge_bases WHERE id = $1`, kbID,
	).Scan(&kbRow.ID, &kbRow.OwnerID, &kbRow.Name, &kbRow.Description, &kbRow.EmbeddingModelID,
		&kbRow.EmbeddingDimension, &kbRow.ChunkSize, &kbRow.ChunkOverlap, &kbRow.Status,
		&kbRow.CreatedAt, &kbRow.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询知识库: %w", err)
	}
	if !isAdmin && kbRow.OwnerID != userID {
		return nil, ErrNotFound
	}
	return &kbRow, nil
}

// GetKB 返回知识库详情与文档列表（不含 file_path）。
func (s *Service) GetKB(ctx context.Context, userID int64, isAdmin bool, kbID int64) (*KB, []Document, error) {
	kbRow, err := s.checkAccess(ctx, userID, isAdmin, kbID)
	if err != nil {
		return nil, nil, err
	}
	docs, err := s.listDocuments(ctx, kbID)
	if err != nil {
		return nil, nil, err
	}
	return kbRow, docs, nil
}

func (s *Service) listDocuments(ctx context.Context, kbID int64) ([]Document, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.knowledge_base_id, d.display_name, d.content_hash, d.media_type,
		       d.char_count, d.status, d.failure_reason, d.retry_count, d.created_at, d.updated_at,
		       (SELECT count(*) FROM document_chunks c WHERE c.document_id = d.id) AS chunk_count
		FROM documents d WHERE d.knowledge_base_id = $1 ORDER BY d.id DESC`, kbID)
	if err != nil {
		return nil, fmt.Errorf("查询文档: %w", err)
	}
	defer rows.Close()

	out := []Document{}
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.KnowledgeBaseID, &d.DisplayName, &d.ContentHash, &d.MediaType,
			&d.CharCount, &d.Status, &d.FailureReason, &d.RetryCount, &d.CreatedAt, &d.UpdatedAt,
			&d.ChunkCount); err != nil {
			return nil, fmt.Errorf("读取文档: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UploadDocument 校验并保存上传文档：UTF-8 TXT/Markdown、大小上限、
// 同库内容哈希去重（已停用的同哈希文档重新入队）。
// 成功后文档状态为 queued，等待后台 worker 处理。
func (s *Service) UploadDocument(ctx context.Context, userID int64, isAdmin bool, kbID int64,
	displayName, contentType string, content []byte,
) (*Document, bool, error) {
	if _, err := s.checkAccess(ctx, userID, isAdmin, kbID); err != nil {
		return nil, false, err
	}

	cleanName, mediaType, normalized, err := normalizeAndValidate(displayName, contentType, content)
	if err != nil {
		return nil, false, err
	}

	sum := sha256.Sum256(normalized)
	hash := hex.EncodeToString(sum[:])

	// 同库内容去重：已存在且未停用 → 返回已有文档；已停用 → 重新入队。
	existing, err := s.findDocumentByHash(ctx, kbID, hash)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if existing.Status == StatusDisabled {
			requeued, err := s.requeueDocument(ctx, kbID, existing.ID)
			if err != nil {
				return nil, false, err
			}
			return requeued, true, nil
		}
		return existing, true, nil
	}

	doc := &Document{
		KnowledgeBaseID: kbID,
		DisplayName:     cleanName,
		ContentHash:     hash,
		MediaType:       mediaType,
		CharCount:       utf8.RuneCountInString(string(normalized)),
		Status:          StatusQueued,
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO documents (knowledge_base_id, display_name, content_hash, media_type, source, char_count, status)
		VALUES ($1, $2, $3, $4, 'upload', $5, $6)
		RETURNING id, created_at, updated_at`,
		kbID, cleanName, hash, mediaType, doc.CharCount, StatusQueued,
	).Scan(&doc.ID, &doc.CreatedAt, &doc.UpdatedAt)
	if isUniqueViolation(err) {
		// 并发上传同内容：退回到读取已有文档。
		existing, ferr := s.findDocumentByHash(ctx, kbID, hash)
		if ferr != nil || existing == nil {
			return nil, false, fmt.Errorf("写入文档冲突: %w", err)
		}
		return existing, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("写入文档: %w", err)
	}

	relPath := filepath.ToSlash(filepath.Join("kb",
		strconv.FormatInt(kbID, 10), strconv.FormatInt(doc.ID, 10)+strings.ToLower(filepath.Ext(cleanName))))
	if err := s.writeFile(relPath, normalized); err != nil {
		_, _ = s.pool.Exec(ctx, `DELETE FROM documents WHERE id = $1`, doc.ID)
		return nil, false, fmt.Errorf("保存文件: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE documents SET file_path = $2, updated_at = now() WHERE id = $1`,
		doc.ID, relPath); err != nil {
		return nil, false, fmt.Errorf("更新文件路径: %w", err)
	}
	return doc, false, nil
}

func (s *Service) findDocumentByHash(ctx context.Context, kbID int64, hash string) (*Document, error) {
	var d Document
	err := s.pool.QueryRow(ctx, `
		SELECT id, knowledge_base_id, display_name, content_hash, media_type,
		       char_count, status, failure_reason, retry_count, created_at, updated_at
		FROM documents WHERE knowledge_base_id = $1 AND content_hash = $2`, kbID, hash,
	).Scan(&d.ID, &d.KnowledgeBaseID, &d.DisplayName, &d.ContentHash, &d.MediaType,
		&d.CharCount, &d.Status, &d.FailureReason, &d.RetryCount, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询文档: %w", err)
	}
	return &d, nil
}

// writeFile 把规范化后的内容写入数据目录下的相对路径。
func (s *Service) writeFile(relPath string, content []byte) error {
	abs := filepath.Join(s.dataDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, content, 0o644)
}

// DisableDocument 停用文档：立即从检索与引用中排除（幂等）。
// 物理清理进入后续维护，不在本期实现。
func (s *Service) DisableDocument(ctx context.Context, userID int64, isAdmin bool, kbID, docID int64) error {
	if _, err := s.checkAccess(ctx, userID, isAdmin, kbID); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE documents SET status = 'disabled', updated_at = now()
		WHERE id = $1 AND knowledge_base_id = $2 AND status <> 'disabled'`,
		docID, kbID)
	if err != nil {
		return fmt.Errorf("停用文档: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// 已停用或不存在；已停用视为幂等成功，不存在返回 404。
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM documents WHERE id = $1 AND knowledge_base_id = $2)`,
			docID, kbID).Scan(&exists); err != nil {
			return fmt.Errorf("查询文档: %w", err)
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

// RetryDocument 把失败文档重新入队；仅 failed 状态允许重试。
func (s *Service) RetryDocument(ctx context.Context, userID int64, isAdmin bool, kbID, docID int64) (*Document, error) {
	if _, err := s.checkAccess(ctx, userID, isAdmin, kbID); err != nil {
		return nil, err
	}
	var d Document
	err := s.pool.QueryRow(ctx, `
		UPDATE documents SET status = 'queued', failure_reason = NULL,
		       retry_count = retry_count + 1, updated_at = now()
		WHERE id = $1 AND knowledge_base_id = $2 AND status = 'failed'
		RETURNING id, knowledge_base_id, display_name, content_hash, media_type,
		          char_count, status, failure_reason, retry_count, created_at, updated_at`,
		docID, kbID,
	).Scan(&d.ID, &d.KnowledgeBaseID, &d.DisplayName, &d.ContentHash, &d.MediaType,
		&d.CharCount, &d.Status, &d.FailureReason, &d.RetryCount, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidState
	}
	if err != nil {
		return nil, fmt.Errorf("重试文档: %w", err)
	}
	return &d, nil
}

func (s *Service) requeueDocument(ctx context.Context, kbID, docID int64) (*Document, error) {
	var d Document
	err := s.pool.QueryRow(ctx, `
		UPDATE documents SET status = 'queued', failure_reason = NULL, updated_at = now()
		WHERE id = $1 AND knowledge_base_id = $2 AND status = 'disabled'
		RETURNING id, knowledge_base_id, display_name, content_hash, media_type,
		          char_count, status, failure_reason, retry_count, created_at, updated_at`,
		docID, kbID,
	).Scan(&d.ID, &d.KnowledgeBaseID, &d.DisplayName, &d.ContentHash, &d.MediaType,
		&d.CharCount, &d.Status, &d.FailureReason, &d.RetryCount, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("重新入队文档: %w", err)
	}
	return &d, nil
}
