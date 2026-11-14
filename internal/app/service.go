package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/api"
	"github.com/hequan2017/bigmodel/internal/kb"
	"github.com/hequan2017/bigmodel/internal/model"
	"github.com/hequan2017/bigmodel/internal/prompt"
)

const (
	// MaxHistoryTurns 是会话历史上限（§9.3：最近最多 3 轮）。
	MaxHistoryTurns = 3
	// MaxHistoryRunes 是注入上下文的历史总字符上限。
	MaxHistoryRunes = 4000
	// MaxQuestionRunes 是用户问题长度上限。
	MaxQuestionRunes = 1000
	// runTimeout 限制单次运行总时长。
	runTimeout = 180 * time.Second
)

// Service 聚合应用配置与运行逻辑；kb 用于运行期检索。
type Service struct {
	pool       *pgxpool.Pool
	kb         *kb.Service
	runTimeout time.Duration
}

// NewService 构造应用服务。
func NewService(pool *pgxpool.Pool, kbSvc *kb.Service) *Service {
	return &Service{pool: pool, kb: kbSvc, runTimeout: 180 * time.Second}
}

// SetRunTimeout 设置单次运行总时长上限（部署可按模型延迟调整）。
func (s *Service) SetRunTimeout(d time.Duration) {
	if d > 0 {
		s.runTimeout = d
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func invalidConfig(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
}

// validateConfig 校验配置取值范围与 Prompt 模板（§8.1/§9.1）。
func validateConfig(cfg AppConfig) error {
	if cfg.KnowledgeBaseID <= 0 {
		return invalidConfig("knowledge_base_id 必填")
	}
	if strings.TrimSpace(cfg.Prompt) == "" {
		return invalidConfig("prompt 不能为空")
	}
	if err := prompt.Validate(cfg.Prompt); err != nil {
		return invalidConfig("prompt: %s", err.Error())
	}
	if cfg.TopK < 1 || cfg.TopK > 20 {
		return invalidConfig("top_k 必须在 1-20")
	}
	if cfg.MaxTokens < 0 || cfg.MaxTokens > 4096 {
		return invalidConfig("max_tokens 必须在 0-4096")
	}
	if cfg.HistoryTurns < 0 || cfg.HistoryTurns > MaxHistoryTurns {
		return invalidConfig("history_turns 必须在 0-%d", MaxHistoryTurns)
	}
	return nil
}

// checkKB 校验绑定的知识库存在且属于应用 owner（教师仅可绑定本人知识库）。
func (s *Service) checkKB(ctx context.Context, cfg AppConfig, ownerID int64) error {
	var owner int64
	err := s.pool.QueryRow(ctx,
		`SELECT owner_id FROM knowledge_bases WHERE id = $1`, cfg.KnowledgeBaseID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalidConfig("绑定的知识库不存在")
	}
	if err != nil {
		return fmt.Errorf("查询知识库: %w", err)
	}
	if owner != ownerID {
		return invalidConfig("只能绑定本人创建的知识库")
	}
	return nil
}

func normalizeConfig(cfg AppConfig) AppConfig {
	if cfg.TopK <= 0 {
		cfg.TopK = 5
	}
	if cfg.HistoryTurns <= 0 {
		cfg.HistoryTurns = MaxHistoryTurns
	}
	return cfg
}

// appRow 是数据库行与配置的解析结果。
type appRow struct {
	ID                int64
	OwnerID           int64
	Name              string
	Status            string
	DraftConfig       *AppConfig
	PublishedConfig   *AppConfig
	PublishedRevision int
	PublishedAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

const appColumns = `id, owner_id, name, status, draft_config, published_config, published_revision, published_at, created_at, updated_at`

func scanAppRow(row pgx.Row) (*appRow, error) {
	var a appRow
	var draft, published []byte
	err := row.Scan(&a.ID, &a.OwnerID, &a.Name, &a.Status, &draft, &published,
		&a.PublishedRevision, &a.PublishedAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if len(draft) > 0 {
		a.DraftConfig = &AppConfig{}
		if err := json.Unmarshal(draft, a.DraftConfig); err != nil {
			return nil, fmt.Errorf("解析草稿配置: %w", err)
		}
	}
	if len(published) > 0 {
		a.PublishedConfig = &AppConfig{}
		if err := json.Unmarshal(published, a.PublishedConfig); err != nil {
			return nil, fmt.Errorf("解析发布配置: %w", err)
		}
	}
	return &a, nil
}

func (s *Service) getAppRow(ctx context.Context, appID int64) (*appRow, error) {
	a, err := scanAppRow(s.pool.QueryRow(ctx,
		`SELECT `+appColumns+` FROM apps WHERE id = $1`, appID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// canManage：管理员或本人。
func canManage(role string, isAdmin bool) bool {
	return isAdmin || role != "student"
}

// CreateDraft 创建应用草稿（教师/管理员）。
func (s *Service) CreateDraft(ctx context.Context, ownerID int64, name string, cfg AppConfig) (*App, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name 不能为空", ErrInvalidName)
	}
	if len([]rune(name)) > 200 {
		return nil, fmt.Errorf("%w: name 过长", ErrInvalidName)
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if err := s.checkKB(ctx, cfg, ownerID); err != nil {
		return nil, err
	}
	cfg = normalizeConfig(cfg)

	payload, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("序列化配置: %w", err)
	}

	a := &App{OwnerID: ownerID, Name: name, Status: StatusDraft, DraftConfig: &cfg}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO apps (owner_id, name, draft_config)
		VALUES ($1, $2, $3)
		RETURNING id, created_at, updated_at`,
		ownerID, name, payload,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	if isUniqueViolation(err) {
		return nil, ErrDuplicateName
	}
	if err != nil {
		return nil, fmt.Errorf("创建应用: %w", err)
	}
	return a, nil
}

// UpdateDraft 更新草稿配置；已发布快照不受影响（§9.2）。
func (s *Service) UpdateDraft(ctx context.Context, userID int64, role string, isAdmin bool, appID int64, cfg AppConfig) (*App, error) {
	a, err := s.getAppRow(ctx, appID)
	if err != nil {
		return nil, err
	}
	if !canManage(role, isAdmin) || (!isAdmin && a.OwnerID != userID) {
		return nil, ErrNotFound
	}
	if a.Status == StatusDisabled {
		return nil, ErrInvalidState
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if err := s.checkKB(ctx, cfg, a.OwnerID); err != nil {
		return nil, err
	}
	cfg = normalizeConfig(cfg)

	payload, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("序列化配置: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE apps SET draft_config = $2, updated_at = now() WHERE id = $1`,
		appID, payload); err != nil {
		return nil, fmt.Errorf("更新草稿: %w", err)
	}
	return s.GetForOwner(ctx, appID)
}

// Publish 发布快照：冻结当前草稿，revision 递增（§9.2）。
func (s *Service) Publish(ctx context.Context, userID int64, role string, isAdmin bool, appID int64) (*App, error) {
	a, err := s.getAppRow(ctx, appID)
	if err != nil {
		return nil, err
	}
	if !canManage(role, isAdmin) || (!isAdmin && a.OwnerID != userID) {
		return nil, ErrNotFound
	}
	if a.Status == StatusDisabled {
		return nil, ErrInvalidState
	}
	if a.DraftConfig == nil {
		return nil, invalidConfig("草稿配置缺失")
	}
	if err := validateConfig(*a.DraftConfig); err != nil {
		return nil, err
	}
	if err := s.checkKB(ctx, *a.DraftConfig, a.OwnerID); err != nil {
		return nil, err
	}

	if _, err := s.pool.Exec(ctx, `
		UPDATE apps SET published_config = draft_config,
		       published_revision = published_revision + 1,
		       published_at = now(),
		       status = 'published',
		       updated_at = now()
		WHERE id = $1`, appID); err != nil {
		return nil, fmt.Errorf("发布应用: %w", err)
	}
	return s.GetForOwner(ctx, appID)
}

// Disable 停用应用：拒绝新运行（幂等）。
func (s *Service) Disable(ctx context.Context, userID int64, role string, isAdmin bool, appID int64) error {
	a, err := s.getAppRow(ctx, appID)
	if err != nil {
		return err
	}
	if !canManage(role, isAdmin) || (!isAdmin && a.OwnerID != userID) {
		return ErrNotFound
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE apps SET status = 'disabled', updated_at = now() WHERE id = $1 AND status <> 'disabled'`, appID); err != nil {
		return fmt.Errorf("停用应用: %w", err)
	}
	return nil
}

// List 列出应用：学生仅见已发布；教师本人；管理员全部。
func (s *Service) List(ctx context.Context, userID int64, role string, isAdmin bool) ([]App, error) {
	where := "WHERE owner_id = $1"
	args := []any{userID}
	if role == "student" {
		where = "WHERE status = 'published'"
		args = nil
	} else if isAdmin {
		where = ""
	}

	query := `SELECT ` + appColumns + ` FROM apps ` + where + ` ORDER BY id DESC`
	var rows pgx.Rows
	var err error
	if args == nil {
		rows, err = s.pool.Query(ctx, query)
	} else {
		rows, err = s.pool.Query(ctx, query, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("查询应用: %w", err)
	}
	defer rows.Close()

	out := []App{}
	for rows.Next() {
		a, err := scanAppRow(rows)
		if err != nil {
			return nil, fmt.Errorf("读取应用: %w", err)
		}
		item := toApp(a, role)
		out = append(out, *item)
	}
	return out, rows.Err()
}

// Get 查看应用；学生不可见草稿应用与非本人应用（统一 404，§4.3）。
func (s *Service) Get(ctx context.Context, userID int64, role string, isAdmin bool, appID int64) (*App, error) {
	a, err := s.getAppRow(ctx, appID)
	if err != nil {
		return nil, err
	}
	if role == "student" {
		if a.Status != StatusPublished {
			return nil, ErrNotFound
		}
		return toApp(a, role), nil
	}
	if !isAdmin && a.OwnerID != userID {
		return nil, ErrNotFound
	}
	return toApp(a, role), nil
}

// GetForOwner 返回完整视图（owner/admin）。
func (s *Service) GetForOwner(ctx context.Context, appID int64) (*App, error) {
	a, err := s.getAppRow(ctx, appID)
	if err != nil {
		return nil, err
	}
	return toApp(a, "teacher"), nil
}

// toApp 按角色裁剪字段：学生不返回草稿配置。
func toApp(a *appRow, role string) *App {
	item := &App{
		ID:                a.ID,
		OwnerID:           a.OwnerID,
		Name:              a.Name,
		Status:            a.Status,
		PublishedRevision: a.PublishedRevision,
		PublishedAt:       a.PublishedAt,
		CreatedAt:         a.CreatedAt,
		UpdatedAt:         a.UpdatedAt,
	}
	if role != "student" {
		item.DraftConfig = a.DraftConfig
	}
	item.PublishedConfig = a.PublishedConfig
	return item
}

// getRunnable 返回可运行的应用与配置：
// 学生只能运行已发布应用（使用发布快照）；教师/管理员运行本人应用按草稿调试（§8.4）。
func (s *Service) getRunnable(ctx context.Context, userID int64, role string, isAdmin bool, appID int64, forcePublished bool) (*appRow, AppConfig, int, string, error) {
	a, err := s.getAppRow(ctx, appID)
	if err != nil {
		return nil, AppConfig{}, 0, "", err
	}
	if role == "student" {
		if a.Status != StatusPublished || a.PublishedConfig == nil {
			return nil, AppConfig{}, 0, "", ErrNotFound
		}
		return a, *a.PublishedConfig, a.PublishedRevision, ModePublished, nil
	}
	if !isAdmin && a.OwnerID != userID {
		return nil, AppConfig{}, 0, "", ErrNotFound
	}
	if a.Status == StatusDisabled {
		return nil, AppConfig{}, 0, "", ErrNotRunnable
	}
	// 教师/管理员：默认按草稿调试；勾选"按发布配置"时验证学生实际行为。
	// 从未发布过（无快照）→ 404：不存在可验证的发布行为。
	if forcePublished {
		if a.Status != StatusPublished || a.PublishedConfig == nil {
			return nil, AppConfig{}, 0, "", ErrNotFound
		}
		return a, *a.PublishedConfig, a.PublishedRevision, ModePublished, nil
	}
	if a.DraftConfig == nil {
		return nil, AppConfig{}, 0, "", invalidConfig("草稿配置缺失")
	}
	return a, *a.DraftConfig, 0, ModeDebug, nil
}

// RunApp 执行一次问答运行并通过 emit 发送事件（§9.3/§9.4）。
// 上游错误映射为稳定错误码；客户端断开或取消标记 canceled，
// 部分输出不写入会话历史。
func (s *Service) RunApp(ctx context.Context, p RunParams, emit func(Event) error) error {
	_, cfg, revision, mode, err := s.getRunnable(ctx, p.UserID, p.Role, p.IsAdmin, p.AppID, p.UsePublished)
	if err != nil {
		return err
	}

	question := strings.TrimSpace(p.Question)
	qRunes := []rune(question)
	if len(qRunes) == 0 {
		return fmt.Errorf("%w: 问题不能为空", ErrInvalidQuestion)
	}
	if len(qRunes) > MaxQuestionRunes {
		return fmt.Errorf("%w: 问题超过 %d 字符", ErrInvalidQuestion, MaxQuestionRunes)
	}
	if p.Chat == nil {
		return ErrChatNotConfigured
	}
	if p.Embed == nil {
		return ErrEmbeddingNotConfigured
	}

	conv := p.ConversationID
	if conv == "" {
		conv = api.NewRequestID()
	} else {
		conv = strings.TrimSpace(conv)
		if len(conv) > 64 || !isSafeConversationID(conv) {
			return fmt.Errorf("%w: conversation_id 非法", ErrInvalidConfig)
		}
	}

	var runID int64
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO app_runs (app_id, revision, user_id, conversation_id, mode, status, question)
		VALUES ($1, $2, $3, $4, $5, 'running', $6)
		RETURNING id`,
		p.AppID, revision, p.UserID, conv, mode, question,
	).Scan(&runID); err != nil {
		return fmt.Errorf("创建运行记录: %w", err)
	}

	// 运行状态落库不依赖请求 ctx：客户端取消后仍需正确写入终态。
	markCtx := context.WithoutCancel(ctx)

	if err := emit(Event{Type: "start", Payload: map[string]any{
		"run_id": runID, "conversation_id": conv, "revision": revision, "mode": mode,
	}}); err != nil {
		_ = s.markRun(markCtx, runID, RunCanceled, "", 0)
		return nil
	}

	// 历史上下文：仅成功运行进入历史，且不同用户/应用不交叉（§9.3）。
	history := s.loadHistory(ctx, p.AppID, p.UserID, conv, cfg.HistoryTurns)

	cands, stats, serr := s.kb.SearchForApp(ctx, cfg.KnowledgeBaseID,
		kb.SearchRequest{Question: question, TopK: cfg.TopK}, p.Embed, p.EmbedModel)
	if serr != nil {
		code, msg := upstreamCode(serr)
		_ = s.markRun(markCtx, runID, RunFailed, code, 0)
		_ = emit(Event{Type: "error", Payload: map[string]any{"code": code, "message": msg}})
		return nil
	}
	// 每路检索通道的候选数/耗时/降级原因随运行记录保存（§7.4 第 7 步）。
	if len(stats) > 0 {
		if b, merr := json.Marshal(stats); merr == nil {
			_ = s.saveRetrievalStats(markCtx, runID, b)
		}
	}
	if err := emit(Event{Type: "references", Payload: map[string]any{"references": buildRefs(cands)}}); err != nil {
		_ = s.markRun(markCtx, runID, RunCanceled, "", 0)
		return nil
	}

	rendered, rerr := prompt.Render(cfg.Prompt, prompt.Inputs{
		"question": question,
		"context":  buildContext(cands),
		"history":  renderHistory(history),
	})
	if rerr != nil {
		_ = s.markRun(markCtx, runID, RunFailed, "render_failed", 0)
		_ = emit(Event{Type: "error", Payload: map[string]any{"code": "render_failed", "message": rerr.Error()}})
		return nil
	}

	start := time.Now()
	var answer strings.Builder
	var usage *model.Usage
	streamCtx, cancel := context.WithTimeout(ctx, s.runTimeout)
	defer cancel()

	err = p.Chat.Stream(streamCtx, model.ChatRequest{
		Model:     p.ChatModel,
		Messages:  []model.ChatMessage{{Role: "user", Content: rendered}},
		MaxTokens: cfg.MaxTokens,
	}, func(ev model.ChatEvent) error {
		if ev.Delta != "" {
			answer.WriteString(ev.Delta)
			if derr := emit(Event{Type: "delta", Payload: map[string]any{"text": ev.Delta}}); derr != nil {
				return errClientGone
			}
		}
		if ev.Usage != nil {
			usage = ev.Usage
		}
		return nil
	})
	duration := time.Since(start).Milliseconds()

	switch {
	case err == nil:
		_ = s.markRun(markCtx, runID, RunSucceeded, "", duration)
		_ = s.saveMessages(ctx, runID, p.AppID, p.UserID, conv, question, answer.String())
		done := map[string]any{"status": RunSucceeded, "duration_ms": duration}
		if usage != nil {
			done["usage"] = usage
		}
		_ = emit(Event{Type: "done", Payload: done})
		return nil
	case errors.Is(err, errClientGone), errors.Is(err, context.Canceled):
		_ = s.markRun(markCtx, runID, RunCanceled, "", duration)
		_ = emit(Event{Type: "done", Payload: map[string]any{"status": RunCanceled, "duration_ms": duration}})
		return nil
	default:
		code, msg := upstreamCode(err)
		if errors.Is(err, context.DeadlineExceeded) {
			code, msg = "upstream_timeout", "生成超时"
		}
		_ = s.markRun(markCtx, runID, RunFailed, code, duration)
		_ = emit(Event{Type: "error", Payload: map[string]any{"code": code, "message": msg}})
		return nil
	}
}

var errClientGone = errors.New("客户端断开连接")

func isSafeConversationID(s string) bool {
	for _, r := range s {
		ok := r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

func buildRefs(cands []kb.Candidate) []Reference {
	refs := make([]Reference, 0, len(cands))
	for _, c := range cands {
		refs = append(refs, Reference{
			ChunkID:     c.ChunkID,
			DocumentID:  c.DocumentID,
			Title:       c.DocumentTitle,
			StartOffset: c.Source.StartOffset,
			EndOffset:   c.Source.EndOffset,
		})
	}
	return refs
}

func buildContext(cands []kb.Candidate) string {
	if len(cands) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cands))
	for i, c := range cands {
		parts = append(parts, fmt.Sprintf("[%d] 《%s》\n%s", i+1, c.DocumentTitle, c.Content))
	}
	return strings.Join(parts, "\n\n")
}

func renderHistory(history []HistoryMessage) string {
	if len(history) == 0 {
		return ""
	}
	lines := make([]string, 0, len(history))
	for _, m := range history {
		switch m.Role {
		case "user":
			lines = append(lines, "用户: "+m.Content)
		case "assistant":
			lines = append(lines, "助手: "+m.Content)
		}
	}
	return strings.Join(lines, "\n")
}

func upstreamCode(err error) (string, string) {
	var ue *model.UpstreamError
	if errors.As(err, &ue) {
		return ue.Code, ue.Message
	}
	return "internal_error", err.Error()
}

// markRun 结束运行记录；仅在 running 状态下生效。
func (s *Service) markRun(ctx context.Context, runID int64, status, errorCode string, durationMs int64) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE app_runs SET status = $2, error_code = $3,
		       duration_ms = $4, finished_at = now()
		WHERE id = $1 AND status = 'running'`,
		runID, status, nullableString(errorCode), nullableInt(durationMs),
	); err != nil {
		return fmt.Errorf("更新运行记录: %w", err)
	}
	return nil
}

// saveRetrievalStats 把每路检索通道的统计写入运行记录（§7.4 第 7 步）。
// 写入失败不影响运行主流程（审计性元数据，尽力而为）。
func (s *Service) saveRetrievalStats(ctx context.Context, runID int64, stats []byte) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE app_runs SET retrieval_stats = $2 WHERE id = $1`,
		runID, stats,
	); err != nil {
		return fmt.Errorf("写入检索统计: %w", err)
	}
	return nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func (s *Service) saveMessages(ctx context.Context, runID, appID, userID int64, conv, question, answer string) error {
	for _, m := range []struct{ role, content string }{
		{"user", question},
		{"assistant", answer},
	} {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO app_messages (run_id, app_id, user_id, conversation_id, role, content)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			runID, appID, userID, conv, m.role, m.content,
		); err != nil {
			return fmt.Errorf("保存会话消息: %w", err)
		}
	}
	return nil
}

// loadHistory 加载最近成功问答（最多 turns 轮），新消息在前，总量受限。
func (s *Service) loadHistory(ctx context.Context, appID, userID int64, conv string, turns int) []HistoryMessage {
	if turns <= 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.role, m.content
		FROM app_messages m
		JOIN app_runs r ON r.id = m.run_id
		WHERE m.app_id = $1 AND m.user_id = $2 AND m.conversation_id = $3 AND r.status = 'succeeded'
		ORDER BY m.id DESC
		LIMIT $4`,
		appID, userID, conv, turns*2,
	)
	if err != nil {
		return nil // 历史加载失败不阻断本次运行
	}
	defer rows.Close()

	var newestFirst []HistoryMessage
	total := 0
	for rows.Next() {
		var m HistoryMessage
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return nil
		}
		total += len([]rune(m.Content))
		if total > MaxHistoryRunes {
			break
		}
		newestFirst = append(newestFirst, m)
	}
	// 反转为时间正序
	for i, j := 0, len(newestFirst)-1; i < j; i, j = i+1, j-1 {
		newestFirst[i], newestFirst[j] = newestFirst[j], newestFirst[i]
	}
	return newestFirst
}

// MessageView 是学生可见的会话消息（仅本人、仅本应用）。
type MessageView struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ListMessages 返回本人某应用、指定会话的历史消息（按时间正序）。
// 未提供会话 ID 或会话不属于本人时返回空列表，不泄露他人内容。
func (s *Service) ListMessages(ctx context.Context, userID, appID int64, conversationID string) ([]MessageView, error) {
	if strings.TrimSpace(conversationID) == "" {
		return []MessageView{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT role, content FROM app_messages
		WHERE app_id = $1 AND user_id = $2 AND conversation_id = $3
		ORDER BY id`, appID, userID, conversationID)
	if err != nil {
		return nil, fmt.Errorf("查询会话消息: %w", err)
	}
	defer rows.Close()

	out := []MessageView{}
	for rows.Next() {
		var m MessageView
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return nil, fmt.Errorf("读取会话消息: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReferenceChunks 供学生/教师查看问答引用的片段原文：
// 仅已发布应用（学生）或本人/管理员应用可查，且文档必须属于应用绑定的知识库。
func (s *Service) ReferenceChunks(ctx context.Context, userID int64, role string, isAdmin bool,
	appID, docID int64) ([]kb.ChunkView, error) {
	a, err := s.getAppRow(ctx, appID)
	if err != nil {
		return nil, err
	}
	if role == "student" {
		if a.Status != StatusPublished || a.PublishedConfig == nil {
			return nil, ErrNotFound
		}
	} else if !isAdmin && a.OwnerID != userID {
		return nil, ErrNotFound
	}
	var kbID int64
	if a.PublishedConfig != nil && role == "student" {
		kbID = a.PublishedConfig.KnowledgeBaseID
	} else if a.DraftConfig != nil {
		kbID = a.DraftConfig.KnowledgeBaseID
	} else {
		return nil, ErrNotFound
	}
	chunks, err := s.kb.ChunksForDoc(ctx, kbID, docID)
	if err != nil {
		if errors.Is(err, kb.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return chunks, nil
}

// RunRow 是教师侧运行记录（§9.3 运行记录查看；不含学生问题全文）。
type RunRow struct {
	ID         int64      `json:"id"`
	LoginName  string     `json:"login_name"`
	Mode       string     `json:"mode"`
	Status     string     `json:"status"`
	Revision   int        `json:"revision"`
	DurationMs *int       `json:"duration_ms,omitempty"`
	ErrorCode  string     `json:"error_code,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// RetrievalStats 是检索通道统计原文（§7.4：每路候选数/耗时/降级原因）。
	RetrievalStats json.RawMessage `json:"retrieval_stats,omitempty"`
}

// ListRuns 返回本人应用（管理员全量）的最近运行记录。
func (s *Service) ListRuns(ctx context.Context, userID int64, isAdmin bool, appID int64, limit int) ([]RunRow, error) {
	a, err := s.getAppRow(ctx, appID)
	if err != nil {
		return nil, err
	}
	if !isAdmin && a.OwnerID != userID {
		return nil, ErrNotFound
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, u.login_name, r.mode, r.status, r.revision,
		       r.duration_ms, COALESCE(r.error_code, ''), r.started_at, r.finished_at,
		       r.retrieval_stats
		FROM app_runs r JOIN users u ON u.id = r.user_id
		WHERE r.app_id = $1
		ORDER BY r.id DESC LIMIT $2`, appID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询运行记录: %w", err)
	}
	defer rows.Close()

	out := []RunRow{}
	for rows.Next() {
		var r RunRow
		var finishedAt *time.Time
		if err := rows.Scan(&r.ID, &r.LoginName, &r.Mode, &r.Status, &r.Revision,
			&r.DurationMs, &r.ErrorCode, &r.StartedAt, &finishedAt,
			&r.RetrievalStats); err != nil {
			return nil, fmt.Errorf("读取运行记录: %w", err)
		}
		r.FinishedAt = finishedAt
		out = append(out, r)
	}
	return out, rows.Err()
}

// ConversationSummary 是学生本人会话列表项（最近优先）。
type ConversationSummary struct {
	ConversationID string    `json:"conversation_id"`
	MessageCount   int       `json:"message_count"`
	LastAt         time.Time `json:"last_at"`
}

// ListConversations 返回本人某应用的会话列表（按最近消息时间倒序）。
// 未提供或无会话返回空列表，不泄露他人会话。
func (s *Service) ListConversations(ctx context.Context, userID, appID int64) ([]ConversationSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT conversation_id, count(*) AS messages, max(created_at) AS last_at
		FROM app_messages
		WHERE app_id = $1 AND user_id = $2
		GROUP BY conversation_id
		ORDER BY last_at DESC`, appID, userID)
	if err != nil {
		return nil, fmt.Errorf("查询会话列表: %w", err)
	}
	defer rows.Close()

	out := []ConversationSummary{}
	for rows.Next() {
		var c ConversationSummary
		if err := rows.Scan(&c.ConversationID, &c.MessageCount, &c.LastAt); err != nil {
			return nil, fmt.Errorf("读取会话: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteConversation 删除本人某应用的指定会话消息（多会话管理）。
// 仅删除消息记录；app_runs 运行记录保留（审计口径）。幂等。
func (s *Service) DeleteConversation(ctx context.Context, userID, appID int64, conversationID string) error {
	if strings.TrimSpace(conversationID) == "" {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM app_messages
		WHERE app_id = $1 AND user_id = $2 AND conversation_id = $3`,
		appID, userID, conversationID)
	if err != nil {
		return fmt.Errorf("删除会话消息: %w", err)
	}
	_ = tag
	return nil
}
