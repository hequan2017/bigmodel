// Package kb 提供知识库与文档管理：创建、上传校验、去重、停用与重试。
package kb

import (
	"errors"
	"time"
)

// 文档处理状态机：queued → parsing → embedding → ready；任何状态可停用。
const (
	StatusQueued    = "queued"
	StatusParsing   = "parsing"
	StatusEmbedding = "embedding"
	StatusReady     = "ready"
	StatusFailed    = "failed"
	StatusDisabled  = "disabled"
)

// 服务级错误，由 handler 层映射为稳定错误码。
var (
	ErrNotFound               = errors.New("资源不存在")
	ErrDuplicateName          = errors.New("同名知识库已存在")
	ErrEmbeddingNotConfigured = errors.New("未配置可用的 Embedding 模型")
	ErrInvalidName            = errors.New("名称不合法")
	ErrInvalidFile            = errors.New("文件不合法")
	ErrFileTooLarge           = errors.New("文件超过大小上限")
	ErrInvalidState           = errors.New("当前状态不允许该操作")
)

// KB 是知识库。
type KB struct {
	ID                 int64     `json:"id"`
	OwnerID            int64     `json:"owner_id"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	EmbeddingModelID   *int64    `json:"embedding_model_id"`
	EmbeddingModelName *string   `json:"embedding_model_name"`
	EmbeddingDimension *int      `json:"embedding_dimension"`
	ChunkSize          int       `json:"chunk_size"`
	ChunkOverlap       int       `json:"chunk_overlap"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// KBWithCount 附带可用文档数量。
type KBWithCount struct {
	KB
	DocCount int `json:"doc_count"`
}

// Document 是文档的展示结构；file_path 不对外暴露。
type Document struct {
	ID              int64     `json:"id"`
	KnowledgeBaseID int64     `json:"knowledge_base_id"`
	DisplayName     string    `json:"display_name"`
	ContentHash     string    `json:"content_hash"`
	MediaType       string    `json:"media_type"`
	CharCount       int       `json:"char_count"`
	ChunkCount      int       `json:"chunk_count"`
	Status          string    `json:"status"`
	FailureReason   *string   `json:"failure_reason"`
	RetryCount      int       `json:"retry_count"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
