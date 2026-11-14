package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"time"
)

// UpstreamError 统一描述外部模型服务错误；Code 是稳定错误码。
type UpstreamError struct {
	Code      string // upstream_status / upstream_timeout / upstream_unavailable / upstream_canceled / upstream_response
	Status    int    // 上游 HTTP 状态码；非 HTTP 错误为 0
	Message   string
	Retryable bool
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("上游错误 [%s]: %s", e.Code, e.Message)
}

// limiter 限制对上游的全局并发，避免拖垮共享服务。
type limiter struct{ sem chan struct{} }

func newLimiter(n int) *limiter { return &limiter{sem: make(chan struct{}, n)} }

func (l *limiter) acquire(ctx context.Context) error {
	select {
	case l.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *limiter) release() { <-l.sem }

// openaiTransport 是上游 HTTP 传输的公共配置。
func openaiTransport() *http.Transport {
	return &http.Transport{
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
}

func mapTransportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return &UpstreamError{Code: "upstream_canceled", Message: "请求已取消"}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &UpstreamError{Code: "upstream_timeout", Message: "上游响应超时", Retryable: true}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &UpstreamError{Code: "upstream_timeout", Message: "上游响应超时", Retryable: true}
	}
	return &UpstreamError{Code: "upstream_unavailable", Message: err.Error(), Retryable: true}
}

// statusError 把非 2xx 响应转换为 UpstreamError；是否可重试取决于状态码。
func statusError(status int, body io.Reader) error {
	snippet, _ := io.ReadAll(io.LimitReader(body, 2048))
	retryable := status == http.StatusTooManyRequests || status >= 500
	return &UpstreamError{
		Code:      "upstream_status",
		Status:    status,
		Message:   fmt.Sprintf("上游返回 %d: %s", status, truncateText(string(snippet), 300)),
		Retryable: retryable,
	}
}

func truncateText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// OpenAIChatClient 是 OpenAI 兼容协议的流式 Chat 适配器。
// 流式请求不做自动重试：已输出文本后重放会造成重复内容。
type OpenAIChatClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
	limits  *limiter
}

// NewOpenAIChatClient 构造 Chat 适配器。
func NewOpenAIChatClient(baseURL, apiKey string) *OpenAIChatClient {
	return &OpenAIChatClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Transport: openaiTransport()},
		limits:  newLimiter(4),
	}
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func messagesPayload(messages []ChatMessage) []map[string]string {
	out := make([]map[string]string, 0, len(messages))
	for _, m := range messages {
		out = append(out, map[string]string{"role": m.Role, "content": m.Content})
	}
	return out
}

// Stream 调用上游 chat/completions 流式接口，把文本增量逐段交给 emit。
// emit 返回错误时立即终止（例如调用方取消），不吞错。
func (c *OpenAIChatClient) Stream(ctx context.Context, req ChatRequest, emit func(ChatEvent) error) error {
	if err := c.limits.acquire(ctx); err != nil {
		return &UpstreamError{Code: "upstream_canceled", Message: "请求已取消"}
	}
	defer c.limits.release()

	body := map[string]any{
		"model":    req.Model,
		"messages": messagesPayload(req.Messages),
		"stream":   true,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return &UpstreamError{Code: "upstream_response", Message: err.Error()}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return &UpstreamError{Code: "upstream_response", Message: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return mapTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp.StatusCode, resp.Body)
	}

	var usage *Usage
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return &UpstreamError{Code: "upstream_response", Message: "无法解析上游流式响应"}
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			if err := emit(ChatEvent{Delta: chunk.Choices[0].Delta.Content}); err != nil {
				return err
			}
		}
		if chunk.Usage != nil {
			usage = &Usage{PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens}
		}
	}
	if err := scanner.Err(); err != nil {
		return mapTransportError(err)
	}
	if usage != nil {
		if err := emit(ChatEvent{Done: true, Usage: usage}); err != nil {
			return err
		}
	}
	return nil
}

// OpenAIEmbeddingClient 是 OpenAI 兼容协议的 Embedding 适配器。
type OpenAIEmbeddingClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
	limits  *limiter
}

// NewOpenAIEmbeddingClient 构造 Embedding 适配器；model 为提供商模型标识。
func NewOpenAIEmbeddingClient(baseURL, apiKey, model string) *OpenAIEmbeddingClient {
	return &OpenAIEmbeddingClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Transport: openaiTransport(), Timeout: 30 * time.Second},
		limits:  newLimiter(4),
	}
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed 批量生成向量；对明确可重试的错误做一次受限重试。
func (c *OpenAIEmbeddingClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, &UpstreamError{Code: "upstream_request", Message: "embedding 输入不能为空"}
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(300 * time.Millisecond):
			case <-ctx.Done():
				return nil, &UpstreamError{Code: "upstream_canceled", Message: "请求已取消"}
			}
		}
		vecs, err := c.tryEmbed(ctx, texts)
		if err == nil {
			return vecs, nil
		}
		var ue *UpstreamError
		if errors.As(err, &ue) && ue.Retryable {
			lastErr = err
			continue
		}
		return nil, err
	}
	return nil, lastErr
}

func (c *OpenAIEmbeddingClient) tryEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := c.limits.acquire(ctx); err != nil {
		return nil, &UpstreamError{Code: "upstream_canceled", Message: "请求已取消"}
	}
	defer c.limits.release()

	payload, err := json.Marshal(map[string]any{"model": c.model, "input": texts})
	if err != nil {
		return nil, &UpstreamError{Code: "upstream_request", Message: err.Error()}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, &UpstreamError{Code: "upstream_request", Message: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, mapTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode, resp.Body)
	}

	var parsed embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, &UpstreamError{Code: "upstream_response", Message: "无法解析上游向量响应"}
	}
	if len(parsed.Data) != len(texts) {
		return nil, &UpstreamError{Code: "upstream_response", Message: fmt.Sprintf("向量数量不符：期望 %d，返回 %d", len(texts), len(parsed.Data))}
	}

	out := make([][]float32, len(texts))
	dimension := -1
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(texts) || out[item.Index] != nil {
			return nil, &UpstreamError{Code: "upstream_response", Message: "向量序号非法或重复"}
		}
		if dimension == -1 {
			dimension = len(item.Embedding)
		}
		if len(item.Embedding) != dimension {
			return nil, &UpstreamError{Code: "upstream_response", Message: "向量维度不一致"}
		}
		for _, v := range item.Embedding {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, &UpstreamError{Code: "upstream_response", Message: "向量包含无效数值"}
			}
		}
		out[item.Index] = item.Embedding
	}
	if dimension <= 0 {
		return nil, &UpstreamError{Code: "upstream_response", Message: "向量维度非法"}
	}
	return out, nil
}
