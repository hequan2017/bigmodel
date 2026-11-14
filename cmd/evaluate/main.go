// Command evaluate 是检索质量评测运行器（§7.5）：
// 对评测集中每条问题调用知识库检索，统计文档命中与引用定位正确性，
// 输出 Top-1 / Top-K 文档命中率、Top-1 引用正确率与未命中明细。
//
// 评测集见 docs/eval/eval_set.json（20 条，基于演示资料命题）与
// docs/eval/eval_set_corpus.json（12 条，基于真实仓库文档多片段语料，
// 含期望章节标题的引用定位断言）。
// 指标有效性按通道区分（§7.4）：
//   - fulltext：真实词项相关性，任何上游配置下均为有效指标；
//   - vector / hybrid：依赖 Embedding 上游语义，mock 上游下命中率
//     无参考意义，接入真实模型后方为有效。
//
// 用法：
//
//	# 既有知识库（如演示库）
//	go run ./cmd/evaluate -kb 31 -mode fulltext
//
//	# 用真实仓库文档搭建多片段语料库并评测引用定位（幂等，可重复执行）
//	go run ./cmd/evaluate -mode fulltext \
//	  -corpus docs/Go大模型应用开发平台_两天开发计划与模块设计.md,README.md,docs/API.md,docs/DELIVERY.md \
//	  -set docs/eval/eval_set_corpus.json
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const readyTimeout = 90 * time.Second

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

// apiCall 是带认证的 JSON 请求助手。
func apiCall(base, token, method, path, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
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

type evalCase struct {
	Question    string `json:"question"`
	ExpectedDoc string `json:"expected_document"`
	// ExpectedHeading 期望 Top-1 候选引用片段的章节标题包含该子串；
	// 空值不做引用定位断言（仅文档命中）。
	ExpectedHeading string `json:"expected_heading,omitempty"`
}

type evalSet struct {
	Description string     `json:"description"`
	Cases       []evalCase `json:"cases"`
}

type candidate struct {
	DocumentTitle string  `json:"document_title"`
	Heading       string  `json:"-"`
	Rank          int     `json:"rank"`
	Score         float64 `json:"score"`
	StartOffset   int     `json:"-"`
}

// parseCandidates 从检索响应提取候选（含引用定位）。
func parseCandidates(data []byte) ([]candidate, error) {
	var d struct {
		Candidates []struct {
			DocumentTitle string  `json:"document_title"`
			Rank          int     `json:"rank"`
			Score         float64 `json:"score"`
			Source        struct {
				StartOffset int    `json:"start_offset"`
				Heading     string `json:"heading"`
			} `json:"source_locator"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	out := make([]candidate, 0, len(d.Candidates))
	for _, c := range d.Candidates {
		out = append(out, candidate{
			DocumentTitle: c.DocumentTitle, Heading: c.Source.Heading,
			Rank: c.Rank, Score: c.Score, StartOffset: c.Source.StartOffset,
		})
	}
	return out, nil
}

// setupCorpus 用本地真实文档搭建/复用评测语料库，返回知识库 ID。
// 幂等：同名知识库复用；display_name 已存在的文档跳过（哈希去重）。
func setupCorpus(base, token string, files []string, kbName string) (int64, error) {
	// 复用或创建知识库。
	list, err := apiCall(base, token, http.MethodGet, "/api/kb", "", nil)
	if err != nil {
		return 0, fmt.Errorf("列出知识库: %w", err)
	}
	var kbID int64
	var kbd struct {
		KnowledgeBases []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"knowledge_bases"`
	}
	if err := json.Unmarshal(list, &kbd); err != nil {
		return 0, err
	}
	for _, k := range kbd.KnowledgeBases {
		if k.Name == kbName {
			kbID = k.ID
			break
		}
	}
	if kbID == 0 {
		payload, _ := json.Marshal(map[string]string{"name": kbName,
			"description": "检索评测语料库（真实仓库文档，多片段）"})
		created, err := apiCall(base, token, http.MethodPost, "/api/kb",
			"application/json", strings.NewReader(string(payload)))
		if err != nil {
			return 0, fmt.Errorf("创建语料库: %w", err)
		}
		var cd struct {
			KnowledgeBase struct {
				ID int64 `json:"id"`
			} `json:"knowledge_base"`
		}
		if err := json.Unmarshal(created, &cd); err != nil {
			return 0, err
		}
		kbID = cd.KnowledgeBase.ID
		fmt.Printf("语料库已创建：#%d %s\n", kbID, kbName)
	} else {
		fmt.Printf("复用语料库：#%d %s\n", kbID, kbName)
	}

	// 逐个上传缺失文档（按 display_name 幂等）。
	for _, f := range files {
		name := filepath.Base(f)
		content, err := os.ReadFile(f)
		if err != nil {
			return 0, fmt.Errorf("读取语料 %s: %w", f, err)
		}
		detail, err := apiCall(base, token, http.MethodGet,
			fmt.Sprintf("/api/kb/%d", kbID), "", nil)
		if err != nil {
			return 0, err
		}
		var dd struct {
			Documents []struct {
				DisplayName string `json:"display_name"`
			} `json:"documents"`
		}
		if err := json.Unmarshal(detail, &dd); err != nil {
			return 0, err
		}
		exists := false
		for _, doc := range dd.Documents {
			if doc.DisplayName == name {
				exists = true
				break
			}
		}
		if exists {
			fmt.Printf("  跳过（已存在）：%s\n", name)
			continue
		}
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			return 0, err
		}
		if _, err := fw.Write(content); err != nil {
			return 0, err
		}
		mw.Close()
		if _, err := apiCall(base, token, http.MethodPost,
			fmt.Sprintf("/api/kb/%d/documents", kbID),
			mw.FormDataContentType(), &buf); err != nil {
			return 0, fmt.Errorf("上传语料 %s: %w", name, err)
		}
		fmt.Printf("  已上传：%s\n", name)
	}

	// 轮询等待全部 ready。
	deadline := time.Now().Add(readyTimeout)
	for {
		detail, err := apiCall(base, token, http.MethodGet,
			fmt.Sprintf("/api/kb/%d", kbID), "", nil)
		if err != nil {
			return 0, err
		}
		var dd struct {
			Documents []struct {
				DisplayName string `json:"display_name"`
				Status      string `json:"status"`
			} `json:"documents"`
		}
		if err := json.Unmarshal(detail, &dd); err != nil {
			return 0, err
		}
		allReady, n := true, 0
		for _, doc := range dd.Documents {
			n++
			if doc.Status != "ready" {
				allReady = false
			}
		}
		if n == len(files) && allReady {
			fmt.Printf("语料就绪：%d 篇文档全部 ready\n", n)
			return kbID, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("语料 %d 秒内未全部 ready（%d/%d 篇）",
				int(readyTimeout.Seconds()), n, len(files))
		}
		time.Sleep(2 * time.Second)
	}
}

func main() {
	base := flag.String("base", "http://127.0.0.1:18080", "被测服务地址")
	user := flag.String("user", "teacher", "教师账号")
	pass := flag.String("pass", "teacher@123", "教师口令")
	kbID := flag.Int64("kb", 0, "被测知识库 ID（未指定 -corpus 时必填）")
	setPath := flag.String("set", "docs/eval/eval_set.json", "评测集文件")
	topK := flag.Int("top-k", 2, "检索返回候选数")
	mode := flag.String("mode", "fulltext", "检索通道：vector / fulltext / hybrid")
	corpus := flag.String("corpus", "", "逗号分隔的本地文档路径：搭建/复用语料库并评测")
	kbName := flag.String("kb-name", "检索评测语料库", "语料库知识库名称")
	flag.Parse()

	switch *mode {
	case "vector", "fulltext", "hybrid":
	default:
		fmt.Fprintf(os.Stderr, "mode 必须是 vector/fulltext/hybrid，得到 %q\n", *mode)
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

	if *corpus != "" {
		files := strings.Split(*corpus, ",")
		for i := range files {
			files[i] = strings.TrimSpace(files[i])
		}
		id, err := setupCorpus(*base, token, files, *kbName)
		if err != nil {
			fmt.Fprintln(os.Stderr, "搭建语料库失败:", err)
			os.Exit(1)
		}
		*kbID = id
	}
	if *kbID == 0 {
		fmt.Fprintln(os.Stderr, "必须指定 -kb 知识库 ID 或 -corpus 语料文件")
		os.Exit(1)
	}

	fmt.Printf("检索评测开始：知识库 #%d，通道 %s，评测问题 %d 条，Top-K %d\n",
		*kbID, *mode, len(set.Cases), *topK)
	if *mode == "fulltext" {
		fmt.Println("说明：全文通道为真实词项相关性（ts_rank），指标有效。")
	} else {
		fmt.Println("说明：该通道依赖 Embedding 语义；mock 上游下命中率无参考意义，接入真实模型后方为有效指标。")
	}

	top1DocHits, topKDocHits, citeHits := 0, 0, 0
	var misses []string
	for i, c := range set.Cases {
		payload, _ := json.Marshal(map[string]any{"question": c.Question, "top_k": *topK, "mode": *mode})
		resp, err := apiCall(*base, token, http.MethodPost,
			fmt.Sprintf("/api/kb/%d/search", *kbID), "application/json",
			strings.NewReader(string(payload)))
		if err != nil {
			misses = append(misses, fmt.Sprintf("%02d [%s] 请求失败: %v", i+1, c.Question, err))
			continue
		}
		cands, err := parseCandidates(resp)
		if err != nil {
			misses = append(misses, fmt.Sprintf("%02d [%s] 解析失败: %v", i+1, c.Question, err))
			continue
		}

		hitK := false
		for _, cand := range cands {
			if strings.Contains(cand.DocumentTitle, c.ExpectedDoc) {
				hitK = true
				break
			}
		}
		hit1 := len(cands) > 0 && strings.Contains(cands[0].DocumentTitle, c.ExpectedDoc)
		// 引用定位正确性：Top-1 为期望文档，且引用片段标题含期望章节子串。
		citeOK := hit1 && c.ExpectedHeading != "" &&
			strings.Contains(cands[0].Heading, c.ExpectedHeading)
		if c.ExpectedHeading == "" {
			citeOK = hit1 // 无定位断言时以文档命中为准
		}
		if hit1 {
			top1DocHits++
		}
		if hitK {
			topKDocHits++
		} else {
			misses = append(misses, fmt.Sprintf("%02d [%s] 期望 %s 未出现在 Top-%d",
				i+1, c.Question, c.ExpectedDoc, *topK))
		}
		if citeOK {
			citeHits++
		} else if hitK && c.ExpectedHeading != "" {
			head := "-"
			if len(cands) > 0 {
				head = cands[0].Heading
			}
			misses = append(misses, fmt.Sprintf("%02d [%s] 引用定位不符：期望章节含 %q，实际 %q",
				i+1, c.Question, c.ExpectedHeading, head))
		}

		ranks := make([]string, 0, len(cands))
		for _, cand := range cands {
			heading := cand.Heading
			if heading != "" {
				heading = "§" + heading
			}
			ranks = append(ranks, fmt.Sprintf("#%d %s%s", cand.Rank, cand.DocumentTitle, heading))
		}
		fmt.Printf("%02d %-4s %s\n", i+1, map[bool]string{true: "命中", false: "未中"}[hitK],
			strings.Join(ranks, " / "))
	}

	total := len(set.Cases)
	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("评测完成：%d 条 | Top-1 文档命中 %d（%.0f%%）| Top-%d 文档命中 %d（%.0f%%）| Top-1 引用定位正确 %d（%.0f%%）\n",
		total, top1DocHits, float64(top1DocHits)/float64(total)*100,
		*topK, topKDocHits, float64(topKDocHits)/float64(total)*100,
		citeHits, float64(citeHits)/float64(total)*100)
	if len(misses) > 0 {
		fmt.Println("未命中/定位不符明细:")
		for _, m := range misses {
			fmt.Println("  " + m)
		}
	}
}
