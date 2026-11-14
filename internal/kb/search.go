package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pgvector/pgvector-go"

	"github.com/hequan2017/bigmodel/internal/model"
)

const (
	// DefaultTopK / MaxTopK 是检索返回候选数的默认值与上限。
	DefaultTopK = 5
	MaxTopK     = 20

	// MaxQuestionRunes 是检索问题的长度上限。
	MaxQuestionRunes = 1000

	// ChannelVector 标识向量语义检索通道；混合召回为后续范围。
	ChannelVector = "vector"
)

// 服务级错误，由 handler 层映射为稳定错误码。
var (
	ErrInvalidQuestion   = errors.New("问题不合法")
	ErrDimensionMismatch = errors.New("查询向量维度与知识库索引不一致")
)

// SearchRequest 是向量检索请求。
type SearchRequest struct {
	Question string
	TopK     int
}

// Citation 是引用的原文定位，基于规范化文本的 rune 偏移。
type Citation struct {
	StartOffset int    `json:"start_offset"`
	EndOffset   int    `json:"end_offset"`
	Heading     string `json:"heading"`
}

// Candidate 是统一候选结构（§7.3）：
// 引用仅说明来源有效，不自动保证回答被证据支持，仍需评测。
type Candidate struct {
	ChunkID        int64    `json:"chunk_id"`
	DocumentID     int64    `json:"document_id"`
	DocumentTitle  string   `json:"document_title"`
	Content        string   `json:"content"`
	Source         Citation `json:"source_locator"`
	Channel        string   `json:"retrieval_channel"`
	Rank           int      `json:"rank"`
	Distance       float64  `json:"distance"`
	Score          float64  `json:"score"`
	EmbeddingModel string   `json:"model_version"`
}

// Search 使用知识库绑定的 Embedding 模型做向量检索。
// 前置条件：服务层已完成授权校验；仅返回 ready 文档的片段
// （停用文档立即不可检索）；授权范围不由客户端提交的参数决定。
func (s *Service) Search(ctx context.Context, userID int64, isAdmin bool, kbID int64,
	req SearchRequest, embed model.EmbeddingClient, embedModelName string,
) ([]Candidate, error) {
	if _, err := s.checkAccess(ctx, userID, isAdmin, kbID); err != nil {
		return nil, err
	}
	if embed == nil {
		return nil, ErrEmbeddingNotConfigured
	}

	question := strings.TrimSpace(req.Question)
	qRunes := []rune(question)
	if len(qRunes) == 0 {
		return nil, fmt.Errorf("%w: 问题不能为空", ErrInvalidQuestion)
	}
	if len(qRunes) > MaxQuestionRunes {
		return nil, fmt.Errorf("%w: 问题超过 %d 字符", ErrInvalidQuestion, MaxQuestionRunes)
	}
	topK := req.TopK
	if topK <= 0 {
		topK = DefaultTopK
	}
	if topK > MaxTopK {
		topK = MaxTopK
	}

	vecs, err := embed.Embed(ctx, []string{question})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("%w: 查询向量返回异常", ErrDimensionMismatch)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.document_id, d.display_name, c.content, c.heading,
		       c.start_offset, c.end_offset, c.embedding <=> $1 AS distance, c.embedding_model
		FROM document_chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE d.knowledge_base_id = $2 AND d.status = 'ready'
		ORDER BY c.embedding <=> $1
		LIMIT $3`,
		pgvector.NewVector(vecs[0]), kbID, topK,
	)
	if err != nil {
		if isDimensionError(err) {
			return nil, fmt.Errorf("%w: %s", ErrDimensionMismatch, pgErrMessage(err))
		}
		return nil, fmt.Errorf("向量检索: %w", err)
	}
	defer rows.Close()

	out := []Candidate{}
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.ChunkID, &c.DocumentID, &c.DocumentTitle, &c.Content, &c.Source.Heading,
			&c.Source.StartOffset, &c.Source.EndOffset, &c.Distance, &c.EmbeddingModel); err != nil {
			return nil, fmt.Errorf("读取候选: %w", err)
		}
		c.Channel = ChannelVector
		c.Rank = len(out) + 1
		c.Score = 1 - c.Distance // 余弦相似度：distance ∈ [0,2]
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		if isDimensionError(err) {
			return nil, fmt.Errorf("%w: %s", ErrDimensionMismatch, pgErrMessage(err))
		}
		return nil, fmt.Errorf("遍历候选: %w", err)
	}
	return out, nil
}

func isDimensionError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// pgvector：不同维度向量比较报 "different dimensions"/"dimensions do not match"
		msg := strings.ToLower(pgErr.Message)
		return strings.Contains(msg, "dimension")
	}
	return false
}

func pgErrMessage(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Message
	}
	return ""
}
