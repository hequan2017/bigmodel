// Package app 提供教育问答应用：草稿/发布快照、SSE 流式运行与短期会话历史。
package app

import (
	"errors"
	"time"

	"github.com/hequan2017/bigmodel/internal/model"
)

// 应用与运行状态。
const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusDisabled  = "disabled"

	ModeDebug     = "debug"
	ModePublished = "published"

	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCanceled  = "canceled"
)

// 服务级错误，由 handler 层映射为稳定错误码。
var (
	ErrNotFound               = errors.New("资源不存在")
	ErrDuplicateName          = errors.New("同名应用已存在")
	ErrNotRunnable            = errors.New("应用当前不可运行")
	ErrInvalidState           = errors.New("当前状态不允许该操作")
	ErrInvalidConfig          = errors.New("应用配置不合法")
	ErrInvalidName            = errors.New("名称不合法")
	ErrInvalidQuestion        = errors.New("问题不合法")
	ErrChatNotConfigured      = errors.New("未配置 Chat 上游")
	ErrEmbeddingNotConfigured = errors.New("未配置 Embedding 上游")
)

// AppConfig 是 draft_config / published_config 的结构。
// 字段必须经后端 Schema 校验（§9.1），禁止客户端透传任意字段。
type AppConfig struct {
	KnowledgeBaseID int64  `json:"knowledge_base_id"`
	ChatModelID     *int64 `json:"chat_model_id"`
	Prompt          string `json:"prompt"`
	TopK            int    `json:"top_k"`
	MaxTokens       int    `json:"max_tokens"`
	HistoryTurns    int    `json:"history_turns"`
}

// App 是应用的展示结构。
type App struct {
	ID                int64      `json:"id"`
	OwnerID           int64      `json:"owner_id"`
	Name              string     `json:"name"`
	Status            string     `json:"status"`
	DraftConfig       *AppConfig `json:"draft_config,omitempty"`
	PublishedConfig   *AppConfig `json:"published_config,omitempty"`
	PublishedRevision int        `json:"published_revision"`
	PublishedAt       *time.Time `json:"published_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// Reference 是本次可引用片段（§9.4 references 事件）。
type Reference struct {
	ChunkID     int64  `json:"chunk_id"`
	DocumentID  int64  `json:"document_id"`
	Title       string `json:"title"`
	StartOffset int    `json:"start_offset"`
	EndOffset   int    `json:"end_offset"`
}

// Event 是运行过程事件（§9.4）。
type Event struct {
	Type    string // start / references / delta / done / error
	Payload any
}

// HistoryMessage 是会话历史消息。
type HistoryMessage struct {
	Role    string
	Content string
}

// RunParams 是一次运行的输入。
type RunParams struct {
	UserID  int64
	Role    string
	IsAdmin bool
	AppID   int64
	// UsePublished: 教师/管理员调试时按发布快照运行（默认按草稿）。
	UsePublished   bool
	Question       string
	ConversationID string
	Chat           model.ChatClient
	ChatModel      string
	Embed          model.EmbeddingClient
	EmbedModel     string
}
