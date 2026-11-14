package model

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatStreamCollectsDeltasAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, d := range []string{"你", "好"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", d)
			fl.Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	c := NewOpenAIChatClient(srv.URL, "test-key")
	var text strings.Builder
	var usage *Usage
	err := c.Stream(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}, func(ev ChatEvent) error {
		text.WriteString(ev.Delta)
		if ev.Usage != nil {
			usage = ev.Usage
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	if text.String() != "你好" {
		t.Errorf("文本增量汇总 = %q", text.String())
	}
	if usage == nil || usage.PromptTokens != 3 || usage.CompletionTokens != 2 {
		t.Errorf("Usage = %+v", usage)
	}
}

func TestChatStreamUpstreamStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewOpenAIChatClient(srv.URL, "test-key")
	err := c.Stream(context.Background(), ChatRequest{Model: "m"}, func(ChatEvent) error { return nil })
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("应为 UpstreamError, got %v", err)
	}
	if ue.Code != "upstream_status" || ue.Status != http.StatusUnauthorized {
		t.Errorf("Code=%s Status=%d", ue.Code, ue.Status)
	}
}

func TestChatStreamContextCanceled(t *testing.T) {
	c := NewOpenAIChatClient("http://127.0.0.1:1", "test-key")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Stream(ctx, ChatRequest{Model: "m"}, func(ChatEvent) error { return nil })
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Code != "upstream_canceled" {
		t.Fatalf("应返回 upstream_canceled, got %v", err)
	}
}

func TestEmbeddingOrderedByIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = jsonDecode(r.Body, &req)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[`+
			`{"index":1,"embedding":[0.3,0.4]},`+
			`{"index":0,"embedding":[0.1,0.2]}]}`)
	}))
	defer srv.Close()

	c := NewOpenAIEmbeddingClient(srv.URL, "test-key", "e")
	got, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed 失败: %v", err)
	}
	if len(got) != 2 || got[0][0] != 0.1 || got[1][0] != 0.3 {
		t.Errorf("向量顺序错误: %+v", got)
	}
}

func TestEmbeddingRetriesOnceOnServerError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,2]}]}`)
	}))
	defer srv.Close()

	c := NewOpenAIEmbeddingClient(srv.URL, "test-key", "e")
	got, err := c.Embed(context.Background(), []string{"a"})
	if err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if calls != 2 {
		t.Errorf("调用次数 = %d", calls)
	}
	if len(got[0]) != 2 {
		t.Errorf("向量维度 = %d", len(got[0]))
	}
}

func TestEmbeddingNoRetryOnClientError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := NewOpenAIEmbeddingClient(srv.URL, "test-key", "e")
	_, err := c.Embed(context.Background(), []string{"a"})
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Code != "upstream_status" {
		t.Fatalf("应返回 upstream_status, got %v", err)
	}
	if calls != 1 {
		t.Errorf("4xx 不应重试, 调用次数 = %d", calls)
	}
}

func TestEmbeddingRejectsInvalidVectors(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"数量不符", `{"data":[{"index":0,"embedding":[1,2]}]}`},
		{"维度不一致", `{"data":[{"index":0,"embedding":[1,2]},{"index":1,"embedding":[1,2,3]}]}`},
		{"序号重复", `{"data":[{"index":0,"embedding":[1,2]},{"index":0,"embedding":[1,2]}]}`},
		{"维度非法", `{"data":[{"index":0,"embedding":[]}]}`},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, tc.payload)
		}))
		c := NewOpenAIEmbeddingClient(srv.URL, "test-key", "e")
		_, err := c.Embed(context.Background(), []string{"a", "b"})
		srv.Close()
		var ue *UpstreamError
		if !errors.As(err, &ue) {
			t.Errorf("%s: 应返回 UpstreamError, got %v", tc.name, err)
		}
	}
}

func TestEmbeddingEmptyInput(t *testing.T) {
	c := NewOpenAIEmbeddingClient("http://127.0.0.1:1", "test-key", "e")
	if _, err := c.Embed(context.Background(), nil); err == nil {
		t.Error("空输入应返回错误")
	}
}
