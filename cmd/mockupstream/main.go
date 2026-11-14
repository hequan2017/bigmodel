// Command mockupstream 是开发/联调用的 OpenAI 兼容协议模拟上游，
// 仅用于本地验证适配器与全链路连通性，不参与生产部署。
// 生产环境应通过 APP_CHAT_* / APP_EMBEDDING_* 指向真实模型服务。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	addr := firstNonEmpty(os.Getenv("MOCK_ADDR"), ":18081")
	http.HandleFunc("/chat/completions", handleChat)
	http.HandleFunc("/embeddings", handleEmbeddings)
	fmt.Println("模拟上游已启动:", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintln(os.Stderr, "模拟上游退出:", err)
		os.Exit(1)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// checkAuth 模拟真实上游：缺少 Bearer 令牌返回 401。
func checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "missing bearer token"}})
		return false
	}
	return true
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !checkAuth(w, r) {
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	for _, piece := range []string{"OK", "，", "来自", "模拟", "上游"} {
		chunk, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]string{"content": piece}}},
		})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		if fl != nil {
			fl.Flush()
		}
		time.Sleep(20 * time.Millisecond)
	}
	usage, _ := json.Marshal(map[string]any{
		"choices": []any{},
		"usage":   map[string]int{"prompt_tokens": 12, "completion_tokens": 5},
	})
	fmt.Fprintf(w, "data: %s\n\n", usage)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

func handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !checkAuth(w, r) {
		return
	}
	var req struct {
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Input) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	data := make([]map[string]any, len(req.Input))
	for i := range req.Input {
		data[i] = map[string]any{
			"index":     i,
			"embedding": []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8},
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":  data,
		"usage": map[string]int{"prompt_tokens": 8},
	})
}
