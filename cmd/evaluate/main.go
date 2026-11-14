// Command evaluate 是检索质量评测运行器（§7.5）：
// 对评测集中每条问题调用知识库检索，统计期望文档是否出现在 Top-K 中，
// 输出 top1 / topK 命中率与未命中明细。
//
// 评测集见 docs/eval/eval_set.json（20 条，基于演示资料命题）。
// 指标有效性按通道区分（§7.4）：
//   - fulltext：真实词项相关性，任何上游配置下均为有效指标；
//   - vector / hybrid：依赖 Embedding 上游语义，mock 上游下
//     所有查询向量相同，命中率无参考意义，接入真实模型后方为有效。
//
// 用法：
//
//	go run ./cmd/evaluate -base http://127.0.0.1:18080 \
//	  -user teacher -pass teacher@123 -kb 31 -mode fulltext \
//	  -set docs/eval/eval_set.json
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func login(base, user, pass string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"login_name": user, "password": pass})
	resp, err := http.Post(base+"/api/auth/login", "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var d struct {
		Token string `json:"token"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, d.Error.Message)
	}
	return d.Token, nil
}

func httpNewRequest(url, contentType string, body io.Reader, token string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	return req, nil
}

type evalCase struct {
	Question         string `json:"question"`
	ExpectedDocument string `json:"expected_document"`
}

type evalSet struct {
	Description  string     `json:"description"`
	ExpectedTopK int        `json:"expected_top_k"`
	Cases        []evalCase `json:"cases"`
}

type candidate struct {
	DocumentTitle string  `json:"document_title"`
	Rank          int     `json:"rank"`
	Score         float64 `json:"score"`
}

func main() {
	base := flag.String("base", "http://127.0.0.1:18080", "被测服务地址")
	user := flag.String("user", "teacher", "教师账号")
	pass := flag.String("pass", "teacher@123", "教师口令")
	kbID := flag.Int("kb", 0, "被测知识库 ID（必填）")
	setPath := flag.String("set", "docs/eval/eval_set.json", "评测集文件")
	topK := flag.Int("top-k", 2, "检索返回候选数")
	mode := flag.String("mode", "fulltext", "检索通道：vector / fulltext / hybrid")
	flag.Parse()

	switch *mode {
	case "vector", "fulltext", "hybrid":
	default:
		fmt.Fprintf(os.Stderr, "mode 必须是 vector/fulltext/hybrid，得到 %q\n", *mode)
		os.Exit(1)
	}

	if *kbID == 0 {
		fmt.Fprintln(os.Stderr, "必须指定 -kb 知识库 ID")
		os.Exit(1)
	}

	raw, err := os.ReadFile(*setPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取评测集失败:", err)
		os.Exit(1)
	}
	var set evalSet
	if err := json.Unmarshal(raw, &set); err != nil {
		fmt.Fprintln(os.Stderr, "解析评测集失败:", err)
		os.Exit(1)
	}

	token, err := login(*base, *user, *pass)
	if err != nil {
		fmt.Fprintln(os.Stderr, "登录失败:", err)
		os.Exit(1)
	}

	fmt.Printf("检索评测开始：知识库 #%d，通道 %s，评测问题 %d 条，Top-K %d\n",
		*kbID, *mode, len(set.Cases), *topK)
	if *mode == "fulltext" {
		fmt.Println("说明：全文通道为真实词项相关性（ts_rank），指标有效。")
	} else {
		fmt.Println("说明：该通道依赖 Embedding 语义；mock 上游下命中率无参考意义，接入真实模型后方为有效指标。")
	}

	top1Hits, topKHits := 0, 0
	var misses []string
	for i, c := range set.Cases {
		payload, _ := json.Marshal(map[string]any{"question": c.Question, "top_k": *topK, "mode": *mode})
		req := strings.NewReader(string(payload))
		resp, err := httpPost(*base+fmt.Sprintf("/api/kb/%d/search", *kbID),
			"application/json", req, token)
		if err != nil {
			misses = append(misses, fmt.Sprintf("%02d [%s] 请求失败: %v", i+1, c.Question, err))
			continue
		}
		var d struct {
			Candidates []candidate `json:"candidates"`
		}
		if err := json.NewDecoder(bytes.NewReader(resp)).Decode(&d); err != nil {
			misses = append(misses, fmt.Sprintf("%02d [%s] 解析失败: %v", i+1, c.Question, err))
			continue
		}

		hitK := false
		hit1 := len(d.Candidates) > 0 &&
			strings.Contains(d.Candidates[0].DocumentTitle, c.ExpectedDocument)
		for _, cand := range d.Candidates {
			if strings.Contains(cand.DocumentTitle, c.ExpectedDocument) {
				hitK = true
				break
			}
		}
		if hit1 {
			top1Hits++
		}
		if hitK {
			topKHits++
		} else {
			misses = append(misses, fmt.Sprintf("%02d [%s] 期望 %s 未出现在 Top-%d",
				i+1, c.Question, c.ExpectedDocument, *topK))
		}

		ranks := make([]string, 0, len(d.Candidates))
		for _, cand := range d.Candidates {
			ranks = append(ranks, fmt.Sprintf("#%d %s", cand.Rank, cand.DocumentTitle))
		}
		fmt.Printf("%02d %-4s %s\n", i+1, map[bool]string{true: "命中", false: "未中"}[hitK],
			strings.Join(ranks, " / "))
	}

	total := len(set.Cases)
	fmt.Println(strings.Repeat("-", 50))
	fmt.Printf("评测完成：%d 条 | Top-1 命中 %d（%.0f%%）| Top-%d 命中 %d（%.0f%%）\n",
		total, top1Hits, float64(top1Hits)/float64(total)*100,
		*topK, topKHits, float64(topKHits)/float64(total)*100)
	if len(misses) > 0 {
		fmt.Println("未命中明细:")
		for _, m := range misses {
			fmt.Println("  " + m)
		}
	}
}

func httpPost(url, contentType string, body io.Reader, token string) ([]byte, error) {
	req, err := httpNewRequest(url, contentType, body, token)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}
