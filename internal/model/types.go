// Package model 提供模型接入：Chat 与 Embedding 的客户端适配器、
// 配置档案（model_profiles）与连通性验证。
package model

import "context"

// ChatMessage 是平台内部使用的消息结构。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest 是发给 Chat 服务的请求。
// 平台内部字段与外部协议解耦，由适配器负责转换。
type ChatRequest struct {
	Model       string
	Messages    []ChatMessage
	MaxTokens   int
	Temperature float64
}

// ChatEvent 是流式输出事件：文本增量，或结束时的可选使用量。
type ChatEvent struct {
	Delta string
	Done  bool
	Usage *Usage
}

// Usage 是供应商提供的 Token 使用量；未提供时为 nil，不伪造精确用量。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// ChatClient 流式调用 Chat 服务。
type ChatClient interface {
	Stream(ctx context.Context, req ChatRequest, emit func(ChatEvent) error) error
}

// EmbeddingClient 批量生成文本向量。
type EmbeddingClient interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}
