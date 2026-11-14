package model

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/config"
)

// Profile 是 model_profiles 的展示结构。
// credential_ref 只是凭据的环境变量名，密钥正文不出现在任何接口响应中。
type Profile struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Kind               string `json:"kind"`
	ProviderType       string `json:"provider_type"`
	ModelName          string `json:"model_name"`
	BaseURL            string `json:"base_url"`
	CredentialRef      string `json:"credential_ref"`
	EmbeddingDimension *int   `json:"embedding_dimension"`
	Enabled            bool   `json:"enabled"`
	ConfigVersion      int    `json:"config_version"`
}

// Service 聚合模型客户端与配置档案。
type Service struct {
	pool *pgxpool.Pool

	chatClient       ChatClient
	chatModel        string
	chatBaseURL      string
	embeddingClient  EmbeddingClient
	embeddingModel   string
	embeddingBaseURL string
}

// NewService 依据部署配置构建客户端；未配置的维度保持 nil，
// 相关功能明确报告"未配置"，不伪造结果。
func NewService(pool *pgxpool.Pool, cfg config.Config) *Service {
	s := &Service{pool: pool}
	if cfg.ChatBaseURL != "" && cfg.ChatAPIKey != "" && cfg.ChatModel != "" {
		s.chatClient = NewOpenAIChatClient(cfg.ChatBaseURL, cfg.ChatAPIKey)
		s.chatModel = cfg.ChatModel
		s.chatBaseURL = cfg.ChatBaseURL
	}
	if cfg.EmbeddingBaseURL != "" && cfg.EmbeddingAPIKey != "" && cfg.EmbeddingModel != "" {
		s.embeddingClient = NewOpenAIEmbeddingClient(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel)
		s.embeddingModel = cfg.EmbeddingModel
		s.embeddingBaseURL = cfg.EmbeddingBaseURL
	}
	return s
}

// ChatConfigured 返回 Chat 上游是否已配置。
func (s *Service) ChatConfigured() bool { return s.chatClient != nil }

// EmbeddingConfigured 返回 Embedding 上游是否已配置。
func (s *Service) EmbeddingConfigured() bool { return s.embeddingClient != nil }

// Embedding 返回已配置的 Embedding 客户端；未配置返回 nil。
func (s *Service) Embedding() EmbeddingClient { return s.embeddingClient }

// EmbeddingModelName 返回已配置的 Embedding 模型标识。
func (s *Service) EmbeddingModelName() string { return s.embeddingModel }

// SeedProfiles 将部署配置镜像到 model_profiles（幂等）。
// 配置值变化时递增 config_version，便于定位历史运行使用的配置。
func (s *Service) SeedProfiles(ctx context.Context) error {
	if s.chatClient != nil {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO model_profiles (name, kind, provider_type, model_name, base_url, credential_ref)
			VALUES ('chat-default', 'chat', 'openai_compatible', $1, $2, 'APP_CHAT_API_KEY')
			ON CONFLICT (name) DO UPDATE SET
				model_name = EXCLUDED.model_name,
				base_url = EXCLUDED.base_url,
				credential_ref = EXCLUDED.credential_ref,
				enabled = TRUE,
				updated_at = now(),
				config_version = model_profiles.config_version + 1
			WHERE model_profiles.model_name IS DISTINCT FROM EXCLUDED.model_name
			   OR model_profiles.base_url IS DISTINCT FROM EXCLUDED.base_url`,
			s.chatModel, s.chatBaseURL,
		); err != nil {
			return fmt.Errorf("写入 chat 配置: %w", err)
		}
	}
	if s.embeddingClient != nil {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO model_profiles (name, kind, provider_type, model_name, base_url, credential_ref)
			VALUES ('embedding-default', 'embedding', 'openai_compatible', $1, $2, 'APP_EMBEDDING_API_KEY')
			ON CONFLICT (name) DO UPDATE SET
				model_name = EXCLUDED.model_name,
				base_url = EXCLUDED.base_url,
				credential_ref = EXCLUDED.credential_ref,
				enabled = TRUE,
				updated_at = now(),
				config_version = model_profiles.config_version + 1
			WHERE model_profiles.model_name IS DISTINCT FROM EXCLUDED.model_name
			   OR model_profiles.base_url IS DISTINCT FROM EXCLUDED.base_url`,
			s.embeddingModel, s.embeddingBaseURL,
		); err != nil {
			return fmt.Errorf("写入 embedding 配置: %w", err)
		}
	}
	return nil
}

// ListProfiles 列出模型档案。
func (s *Service) ListProfiles(ctx context.Context) ([]Profile, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, kind, provider_type, model_name, base_url, credential_ref,
		       embedding_dimension, enabled, config_version
		FROM model_profiles
		ORDER BY kind, name`)
	if err != nil {
		return nil, fmt.Errorf("查询模型档案: %w", err)
	}
	defer rows.Close()

	var out []Profile
	for rows.Next() {
		var p Profile
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.ProviderType, &p.ModelName,
			&p.BaseURL, &p.CredentialRef, &p.EmbeddingDimension, &p.Enabled, &p.ConfigVersion); err != nil {
			return nil, fmt.Errorf("读取模型档案: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// TestChat 真实调用一次 Chat 上游，验证连通性。
func (s *Service) TestChat(ctx context.Context) (map[string]any, error) {
	start := time.Now()
	var out strings.Builder
	err := s.chatClient.Stream(ctx, ChatRequest{
		Model:     s.chatModel,
		Messages:  []ChatMessage{{Role: "user", Content: "连通性测试：请只回复 OK"}},
		MaxTokens: 16,
	}, func(ev ChatEvent) error {
		out.WriteString(ev.Delta)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":         true,
		"model":      s.chatModel,
		"latency_ms": time.Since(start).Milliseconds(),
		"sample":     truncateText(strings.TrimSpace(out.String()), 120),
	}, nil
}

// TestEmbedding 真实调用一次 Embedding 上游，并把实测维度回写档案。
func (s *Service) TestEmbedding(ctx context.Context) (map[string]any, error) {
	start := time.Now()
	vecs, err := s.embeddingClient.Embed(ctx, []string{"连通性测试"})
	if err != nil {
		return nil, err
	}
	dimension := len(vecs[0])
	if _, err := s.pool.Exec(ctx, `
		UPDATE model_profiles SET embedding_dimension = $1, updated_at = now()
		WHERE kind = 'embedding' AND (embedding_dimension IS NULL OR embedding_dimension <> $1)`,
		dimension,
	); err != nil {
		return nil, fmt.Errorf("回写向量维度: %w", err)
	}
	return map[string]any{
		"ok":         true,
		"model":      s.embeddingModel,
		"dimension":  dimension,
		"latency_ms": time.Since(start).Milliseconds(),
	}, nil
}
