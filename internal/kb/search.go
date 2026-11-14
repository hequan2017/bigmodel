package kb

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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

	// ChannelVector / ChannelFullText / ChannelHybrid 是检索通道标识（§7.4），
	// 同时作为检索模式取值；混合召回为向量 + 全文的 RRF 融合。
	ChannelVector   = "vector"
	ChannelFullText = "fulltext"
	ChannelHybrid   = "hybrid"

	// 检索模式取值（与通道标识一致，供调用方语义化引用）。
	ModeVector   = ChannelVector
	ModeFullText = ChannelFullText
	ModeHybrid   = ChannelHybrid

	// RrfK 是 RRF 融合的平滑常数（IR 惯用取值 60）。
	RrfK = 60

	// channelTimeout 是混合召回中每路通道的独立超时（§7.4 第 2 步）。
	channelTimeout = 10 * time.Second
)

// 服务级错误，由 handler 层映射为稳定错误码。
var (
	ErrInvalidQuestion   = errors.New("问题不合法")
	ErrDimensionMismatch = errors.New("查询向量维度与知识库索引不一致")
	ErrInvalidMode       = errors.New("检索模式不合法")
)

// SearchRequest 是检索请求；Mode 为空默认 hybrid。
type SearchRequest struct {
	Question string
	TopK     int
	Mode     string
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
	// RrfScore 是混合召回的 RRF 融合得分；仅 hybrid 通道返回。
	RrfScore float64 `json:"rrf_score,omitempty"`
}

// ChannelStats 记录一路检索通道的候选数、耗时与降级原因（§7.4 第 7 步），
// 随运行记录写入 app_runs.retrieval_stats。
type ChannelStats struct {
	Channel       string `json:"channel"`
	Candidates    int    `json:"candidates"`
	DurationMs    int64  `json:"duration_ms"`
	DegradeReason string `json:"degrade_reason,omitempty"`
}

// normalizeMode 校验并归一化检索模式；空值默认 hybrid。
func normalizeMode(mode string) (string, error) {
	switch mode {
	case "":
		return ModeHybrid, nil
	case ModeVector, ModeFullText, ModeHybrid:
		return mode, nil
	default:
		return "", fmt.Errorf("%w: mode 必须是 vector/fulltext/hybrid", ErrInvalidMode)
	}
}

// normalizeTopK 收敛候选数为 [DefaultTopK, MaxTopK]。
func normalizeTopK(topK int) int {
	if topK <= 0 {
		return DefaultTopK
	}
	if topK > MaxTopK {
		return MaxTopK
	}
	return topK
}

// validateQuestion 校验问题非空且不超长。
func validateQuestion(question string) (string, error) {
	question = strings.TrimSpace(question)
	qRunes := []rune(question)
	if len(qRunes) == 0 {
		return "", fmt.Errorf("%w: 问题不能为空", ErrInvalidQuestion)
	}
	if len(qRunes) > MaxQuestionRunes {
		return "", fmt.Errorf("%w: 问题超过 %d 字符", ErrInvalidQuestion, MaxQuestionRunes)
	}
	return question, nil
}

// Search 面向教师/管理员的调试检索：先做知识库授权校验（§7.2）。
func (s *Service) Search(ctx context.Context, userID int64, isAdmin bool, kbID int64,
	req SearchRequest, embed model.EmbeddingClient, embedModelName string,
) ([]Candidate, error) {
	if _, err := s.checkAccess(ctx, userID, isAdmin, kbID); err != nil {
		return nil, err
	}
	mode, err := normalizeMode(req.Mode)
	if err != nil {
		return nil, err
	}
	switch mode {
	case ModeVector:
		return s.searchByVector(ctx, kbID, req, embed, embedModelName)
	case ModeFullText:
		return s.searchFullText(ctx, kbID, req)
	default:
		fused, _, err := s.searchHybrid(ctx, kbID, req, embed, embedModelName)
		return fused, err
	}
}

// SearchForApp 供已发布问答应用运行时检索，并返回每路通道统计。
// 授权由"应用已发布"建立（§4.3/§9.3）：检索以服务端应用配置为准，
// 不经客户端知识库参数，因此不做用户级知识库校验。
func (s *Service) SearchForApp(ctx context.Context, kbID int64,
	req SearchRequest, embed model.EmbeddingClient, embedModelName string,
) ([]Candidate, []ChannelStats, error) {
	mode, err := normalizeMode(req.Mode)
	if err != nil {
		return nil, nil, err
	}
	switch mode {
	case ModeVector:
		cands, err := s.searchByVector(ctx, kbID, req, embed, embedModelName)
		return cands, channelStat(ModeVector, cands, err, 0), err
	case ModeFullText:
		cands, err := s.searchFullText(ctx, kbID, req)
		return cands, channelStat(ModeFullText, cands, err, 0), err
	default:
		return s.searchHybrid(ctx, kbID, req, embed, embedModelName)
	}
}

// channelStat 组装单路通道统计；耗时未知时由调用方传 0。
func channelStat(channel string, cands []Candidate, err error, dur time.Duration) []ChannelStats {
	st := ChannelStats{Channel: channel, Candidates: len(cands), DurationMs: dur.Milliseconds()}
	if err != nil {
		st.DegradeReason = err.Error()
	}
	return []ChannelStats{st}
}

// searchByVector 是向量检索的公共实现：仅返回 ready 文档片段。
func (s *Service) searchByVector(ctx context.Context, kbID int64,
	req SearchRequest, embed model.EmbeddingClient, embedModelName string,
) ([]Candidate, error) {
	if embed == nil {
		return nil, ErrEmbeddingNotConfigured
	}

	question, err := validateQuestion(req.Question)
	if err != nil {
		return nil, err
	}
	topK := normalizeTopK(req.TopK)

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

// searchFullText 是全文检索通道：查询与索引词元均由中文分词生成，
// 按 ts_rank 词频相关性排序，索引侧标题词 A 级、正文 C 级加权
// （setweight；实测与无加权持平，保留为标题信号，见 docs/eval/RESULTS.md
// 排序变体实验）；仅返回 ready 文档片段；不依赖 Embedding 上游
// （§7.4 全文通道，非关键词 LIKE 包含查询）。
func (s *Service) searchFullText(ctx context.Context, kbID int64, req SearchRequest) ([]Candidate, error) {
	question, err := validateQuestion(req.Question)
	if err != nil {
		return nil, err
	}
	topK := normalizeTopK(req.TopK)

	tokens := QueryTokens(question)
	if len(tokens) == 0 {
		return []Candidate{}, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.document_id, d.display_name, c.content, c.heading,
		       c.start_offset, c.end_offset, c.embedding_model,
		       ts_rank(c.fts, q.query) AS rel
		FROM document_chunks c
		JOIN documents d ON d.id = c.document_id
		CROSS JOIN (SELECT to_tsquery('simple', $1) AS query) q
		WHERE d.knowledge_base_id = $2 AND d.status = 'ready' AND c.fts @@ q.query
		ORDER BY rel DESC, c.id
		LIMIT $3`,
		buildTsQuery(tokens), kbID, topK,
	)
	if err != nil {
		return nil, fmt.Errorf("全文检索: %w", err)
	}
	defer rows.Close()

	out := []Candidate{}
	for rows.Next() {
		var c Candidate
		var rel float64
		if err := rows.Scan(&c.ChunkID, &c.DocumentID, &c.DocumentTitle, &c.Content, &c.Source.Heading,
			&c.Source.StartOffset, &c.Source.EndOffset, &c.EmbeddingModel, &rel); err != nil {
			return nil, fmt.Errorf("读取候选: %w", err)
		}
		c.Channel = ChannelFullText
		c.Rank = len(out) + 1
		c.Score = rel
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历候选: %w", err)
	}
	return out, nil
}

// searchHybrid 混合召回：向量语义与全文检索两路并行、各自独立超时，
// 单路失败降级为另一路并记录原因（§7.4 第 2、7 步），RRF 融合去重。
func (s *Service) searchHybrid(ctx context.Context, kbID int64,
	req SearchRequest, embed model.EmbeddingClient, embedModelName string,
) ([]Candidate, []ChannelStats, error) {
	var (
		wg             sync.WaitGroup
		vec, fts       []Candidate
		vecErr, ftsErr error
		vecDur, ftsDur time.Duration
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		cctx, cancel := context.WithTimeout(ctx, channelTimeout)
		defer cancel()
		start := time.Now()
		cands, err := s.searchByVector(cctx, kbID, req, embed, embedModelName)
		vecDur = time.Since(start)
		vec, vecErr = cands, err
	}()
	go func() {
		defer wg.Done()
		cctx, cancel := context.WithTimeout(ctx, channelTimeout)
		defer cancel()
		start := time.Now()
		cands, err := s.searchFullText(cctx, kbID, req)
		ftsDur = time.Since(start)
		fts, ftsErr = cands, err
	}()
	wg.Wait()

	stats := append(channelStat(ChannelVector, vec, vecErr, vecDur),
		channelStat(ChannelFullText, fts, ftsErr, ftsDur)...)

	switch {
	case vecErr != nil && ftsErr != nil:
		return nil, stats, vecErr // 两路都失败：向量路错误更基础，优先返回
	case errors.Is(vecErr, ErrDimensionMismatch):
		// 维度不一致是配置错误（知识库换用不同维度模型），不降级掩盖。
		return nil, stats, vecErr
	case vecErr != nil:
		return fts, stats, nil // 上游临时故障：降级全文检索，原因记入统计
	case ftsErr != nil:
		return vec, stats, nil
	}

	fused := rrfFuse(vec, fts)
	if topK := normalizeTopK(req.TopK); len(fused) > topK {
		fused = fused[:topK]
	}
	return fused, stats, nil
}

// rrfFuse 按 Reciprocal Rank Fusion 融合多路候选：score = Σ 1/(k+rank)。
// 不直接相加量纲不同的原始分数（§7.4 第 4 步）；按 ChunkID 去重，
// 重叠候选以向量路候选为基础字段（保留距离与模型信息）。
func rrfFuse(lists ...[]Candidate) []Candidate {
	const absentRank = 1 << 30
	type slot struct {
		cand             Candidate
		score            float64
		vecRank, ftsRank int
	}
	order := map[int64]*slot{}
	merged := make([]*slot, 0, 16)
	for li, list := range lists {
		for i, c := range list {
			rank := i + 1
			sl, ok := order[c.ChunkID]
			if !ok {
				sl = &slot{cand: c}
				order[c.ChunkID] = sl
				merged = append(merged, sl)
			}
			sl.score += 1.0 / float64(RrfK+rank)
			if li == 0 && sl.vecRank == 0 {
				sl.vecRank = rank
				sl.cand = c
			}
			if li == 1 && sl.ftsRank == 0 {
				sl.ftsRank = rank
			}
		}
	}

	rankOrAbsent := func(r int) int {
		if r == 0 {
			return absentRank
		}
		return r
	}
	sort.Slice(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if ra, rb := rankOrAbsent(a.vecRank), rankOrAbsent(b.vecRank); ra != rb {
			return ra < rb
		}
		return rankOrAbsent(a.ftsRank) < rankOrAbsent(b.ftsRank)
	})

	out := make([]Candidate, 0, len(merged))
	for _, sl := range merged {
		c := sl.cand
		c.Channel = ChannelHybrid
		c.Score = sl.score
		c.RrfScore = sl.score
		c.Rank = len(out) + 1
		out = append(out, c)
	}
	return out
}

// buildTsQuery 把查询词元组合为 OR 语义的 tsquery（'a' | 'b'）。
// 词元已被 QueryTokens 过滤为字母/数字成分，单引号包裹防特殊字符。
func buildTsQuery(tokens []string) string {
	quoted := make([]string, len(tokens))
	for i, tok := range tokens {
		quoted[i] = "'" + tok + "'"
	}
	return strings.Join(quoted, " | ")
}

// ReindexFullText 按当前分词器重建知识库全部片段的全文索引：
// 不重新向量化、不调用模型上游，仅重算 fts 列。返回重建的片段数。
func (s *Service) ReindexFullText(ctx context.Context, userID int64, isAdmin bool, kbID int64) (int64, error) {
	if _, err := s.checkAccess(ctx, userID, isAdmin, kbID); err != nil {
		return 0, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.content, c.heading
		FROM document_chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE d.knowledge_base_id = $1`, kbID)
	if err != nil {
		return 0, fmt.Errorf("读取片段: %w", err)
	}
	defer rows.Close()

	var rebuilt int64
	const batch = 200
	ids := make([]int64, 0, batch)
	headSegs := make([]string, 0, batch)
	bodySegs := make([]string, 0, batch)
	flush := func() error {
		for i, id := range ids {
			ct, err := s.pool.Exec(ctx, `
				UPDATE document_chunks SET fts =
				        setweight(to_tsvector('simple', $1), 'A') || setweight(to_tsvector('simple', $2), 'C')
				WHERE id = $3`,
				headSegs[i], bodySegs[i], id)
			if err != nil {
				return fmt.Errorf("重建片段 %d: %w", id, err)
			}
			rebuilt += ct.RowsAffected()
		}
		ids = ids[:0]
		headSegs = headSegs[:0]
		bodySegs = bodySegs[:0]
		return nil
	}
	for rows.Next() {
		var id int64
		var content, heading string
		if err := rows.Scan(&id, &content, &heading); err != nil {
			return 0, fmt.Errorf("扫描片段: %w", err)
		}
		ids = append(ids, id)
		// 与 worker 写入保持一致：标题 A 级、正文 C 级。
		headSegs = append(headSegs, SegmentText(heading))
		bodySegs = append(bodySegs, SegmentText(content))
		if len(ids) >= batch {
			if err := flush(); err != nil {
				return 0, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("遍历片段: %w", err)
	}
	if err := flush(); err != nil {
		return 0, err
	}
	return rebuilt, nil
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
