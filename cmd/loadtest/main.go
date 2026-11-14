// Command loadtest 是考试容量验证工具（§16.3）：
// 阶段一集中开始、阶段二稳定保存、阶段三同时交卷，输出真实延迟分位数与错误数。
//
// 用法示例：
//
//	go run ./cmd/loadtest -base http://127.0.0.1:18080 -students 20 \
//	  -db postgres://bigmodel:bigmodel@localhost:5432/bigmodel?sslmode=disable
//
// 脚本自建带 loadtest_ 前缀的压测账号与考试，可在库中按前缀清理。
// 报告即实测数据，不伪造结果。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/auth"
)

type latencies struct {
	latencies []time.Duration
	errors    int
	lastErr   error
}

func (l *latencies) add(d time.Duration, err error) {
	if err != nil {
		l.errors++
		l.lastErr = err
		return
	}
	l.latencies = append(l.latencies, d)
}

func (l *latencies) report(name string) {
	if len(l.latencies) == 0 {
		fmt.Printf("  %-8s 全部失败（%d 个错误）最后错误: %v\n", name, l.errors, l.lastErr)
		return
	}
	sorted := append([]time.Duration(nil), l.latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	pct := func(p float64) time.Duration {
		return sorted[int(float64(len(sorted)-1)*p)]
	}
	fmt.Printf("  %-8s 成功 %d / 失败 %d | p50 %v  p95 %v  p99 %v  max %v\n",
		name, len(sorted), l.errors, pct(0.50), pct(0.95), pct(0.99), sorted[len(sorted)-1])
}

type client struct {
	base   string
	token  string
	client *http.Client
}

func (c *client) do(method, path string, body any) (time.Duration, map[string]any, error) {
	start := time.Now()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return time.Since(start), nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return time.Since(start), nil, fmt.Errorf("HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &parsed)
	}
	return time.Since(start), parsed, nil
}

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

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// prepFixtures 创建压测学生账号（幂等，口令均为 loadtest@123）。
func prepFixtures(ctx context.Context, pool *pgxpool.Pool, n int) error {
	hash, err := auth.HashPassword("loadtest@123")
	if err != nil {
		return err
	}
	for i := 1; i <= n; i++ {
		login := fmt.Sprintf("loadtest_s%d", i)
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM users WHERE login_name = $1)`, login).Scan(&exists); err != nil {
			return err
		}
		if exists {
			if _, err := pool.Exec(ctx,
				`UPDATE users SET password_hash = $2, enabled = TRUE WHERE login_name = $1`,
				login, hash); err != nil {
				return err
			}
			continue
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO users (login_name, password_hash, role) VALUES ($1, $2, 'student')`,
			login, hash); err != nil {
			return err
		}
	}
	return nil
}

// prepExam 幂等创建一场已发布压测考试（两道题、即刻开放、时长 2 小时），
// 并清理压测学生的遗留作答以便重新开始。
func prepExam(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var teacherID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO users (login_name, password_hash, role)
		VALUES ('loadtest_teacher', 'loadtest', 'teacher')
		ON CONFLICT (login_name) DO UPDATE SET enabled = TRUE
		RETURNING id`).Scan(&teacherID)
	if err != nil {
		return 0, err
	}

	var q1, q2 int64
	if err := pool.QueryRow(ctx, `
		SELECT id FROM exam_questions
		WHERE owner_id = $1 AND stem = 'loadtest 单选题' LIMIT 1`, teacherID).Scan(&q1); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := pool.QueryRow(ctx, `
				INSERT INTO exam_questions (owner_id, type, stem, options, correct_answer, default_score)
				VALUES ($1, 'single', 'loadtest 单选题',
				        '[{"key":"A","text":"甲"},{"key":"B","text":"乙"}]', 'A', 5)
				RETURNING id`, teacherID).Scan(&q1); err != nil {
				return 0, err
			}
		} else {
			return 0, err
		}
	}
	if err := pool.QueryRow(ctx, `
		SELECT id FROM exam_questions
		WHERE owner_id = $1 AND stem = 'loadtest 判断题' LIMIT 1`, teacherID).Scan(&q2); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := pool.QueryRow(ctx, `
				INSERT INTO exam_questions (owner_id, type, stem, options, correct_answer, default_score)
				VALUES ($1, 'judge', 'loadtest 判断题', NULL, 'true', 5)
				RETURNING id`, teacherID).Scan(&q2); err != nil {
				return 0, err
			}
		} else {
			return 0, err
		}
	}

	var examID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO exams (owner_id, title, level_code, open_at, close_at, duration_seconds)
		VALUES ($1, 'loadtest 容量验证考试', 'LT', now() - interval '1 minute',
		        now() + interval '3 hours', 7200)
		ON CONFLICT (owner_id, title) DO UPDATE SET updated_at = now()
		RETURNING id`, teacherID).Scan(&examID)
	if err != nil && !isUniqueViolation(err) {
		return 0, err
	}
	if isUniqueViolation(err) {
		if err := pool.QueryRow(ctx,
			`SELECT id FROM exams WHERE owner_id = $1 AND title = 'loadtest 容量验证考试'`,
			teacherID).Scan(&examID); err != nil {
			return 0, err
		}
	}

	var itemCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM exam_items WHERE exam_id = $1`, examID).Scan(&itemCount); err != nil {
		return 0, err
	}
	if itemCount == 0 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO exam_items (exam_id, source_question_id, order_no, type, stem, options, correct_answer, score)
			VALUES ($1, $2, 1, 'single', 'loadtest 单选题',
			        '[{"key":"A","text":"甲"},{"key":"B","text":"乙"}]', 'A', 5),
			       ($1, $3, 2, 'judge', 'loadtest 判断题', NULL, 'true', 5)`,
			examID, q1, q2); err != nil {
			return 0, err
		}
	}

	if _, err := pool.Exec(ctx, `
		UPDATE exams SET status = 'published', published_at = now(), updated_at = now()
		WHERE id = $1 AND status <> 'published'`, examID); err != nil {
		return 0, err
	}

	// 清理压测学生上一轮遗留作答，保证"集中开始"阶段可重新开始。
	if _, err := pool.Exec(ctx, `
		DELETE FROM exam_attempts WHERE exam_id = $1 AND user_id IN (
			SELECT id FROM users WHERE login_name LIKE 'loadtest_s%')`, examID); err != nil {
		return 0, err
	}
	return examID, nil
}

func examItemIDs(ctx context.Context, pool *pgxpool.Pool, examID int64) []int64 {
	rows, err := pool.Query(ctx,
		`SELECT id FROM exam_items WHERE exam_id = $1 ORDER BY order_no`, examID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil
		}
		ids = append(ids, id)
	}
	return ids
}

func main() {
	base := flag.String("base", "http://127.0.0.1:18080", "被测服务地址")
	students := flag.Int("students", 20, "并发压测学生数量")
	dbURL := flag.String("db", "postgres://bigmodel:bigmodel@localhost:5432/bigmodel?sslmode=disable",
		"PostgreSQL 连接串（用于准备压测账号与考试）")
	savePhaseSeconds := flag.Int("save-seconds", 20, "稳定保存阶段持续秒数")
	flag.Parse()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "连接数据库失败:", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := prepFixtures(ctx, pool, *students); err != nil {
		fmt.Fprintln(os.Stderr, "准备压测账号失败:", err)
		os.Exit(1)
	}
	examID, err := prepExam(ctx, pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "准备压测考试失败:", err)
		os.Exit(1)
	}
	itemIDs := examItemIDs(ctx, pool, examID)

	adminToken, err := login(*base, "admin", "admin@123")
	if err != nil {
		fmt.Fprintln(os.Stderr, "管理员登录失败:", err)
		os.Exit(1)
	}
	adminC := &client{base: *base, token: adminToken, client: &http.Client{Timeout: 30 * time.Second}}

	fmt.Printf("容量验证开始：服务 %s，学生 %d 人，考试 #%d（条目 %d）\n",
		*base, *students, examID, len(itemIDs))

	tokens := make([]string, *students)
	for i := 0; i < *students; i++ {
		tok, err := login(*base, fmt.Sprintf("loadtest_s%d", i+1), "loadtest@123")
		if err != nil {
			fmt.Fprintln(os.Stderr, "压测学生登录失败:", err)
			os.Exit(1)
		}
		tokens[i] = tok
	}

	var wg sync.WaitGroup
	revs := make([]int, len(tokens)) // 每个学生保存后的最新版本号，供交卷使用

	// 阶段一：集中开始（§16.3）。
	startPhase := &latencies{}
	for _, tok := range tokens {
		wg.Add(1)
		go func(tok string) {
			defer wg.Done()
			c := &client{base: *base, token: tok, client: &http.Client{Timeout: 30 * time.Second}}
			d, _, err := c.do("POST", fmt.Sprintf("/api/exam/exams/%d/attempts", examID), nil)
			startPhase.add(d, err)
		}(tok)
	}
	wg.Wait()
	startPhase.report("集中开始")

	// 阶段二：稳定保存（每人每 2 秒保存一次全量答案快照）。
	savePhase := &latencies{}
	phaseDeadline := time.Now().Add(time.Duration(*savePhaseSeconds) * time.Second)
	for i, tok := range tokens {
		wg.Add(1)
		go func(i int, tok string) {
			defer wg.Done()
			c := &client{base: *base, token: tok, client: &http.Client{Timeout: 30 * time.Second}}
			// 按题型作答：单选 A、判断 true；未拿到题目信息前不保存。
			d, parsed, err := c.do("POST", fmt.Sprintf("/api/exam/exams/%d/attempts", examID), nil)
			startPhase.add(d, err)
			if err != nil {
				return
			}
			attempt, _ := parsed["attempt"].(map[string]any)
			revF, _ := attempt["answer_revision"].(float64)
			rev := int(revF)
			answersOf := func() []map[string]any {
				out := []map[string]any{}
				for _, it := range attempt["items"].([]any) {
					m, _ := it.(map[string]any)
					idF, _ := m["item_id"].(float64)
					typ, _ := m["type"].(string)
					ans := "A"
					if typ == "judge" {
						ans = "true"
					}
					out = append(out, map[string]any{"item_id": int(idF), "answer": ans})
				}
				return out
			}
			answers := answersOf()
			for time.Now().Before(phaseDeadline) {
				d, parsed, err := c.do("PUT", fmt.Sprintf("/api/exam/exams/%d/attempts/answers", examID),
					map[string]any{"expected_revision": rev, "answers": answers})
				if err == nil {
					if newRev, ok := parsed["answer_revision"].(float64); ok {
						rev = int(newRev)
					}
				}
				savePhase.add(d, err)
				time.Sleep(2 * time.Second)
			}
			revs[i] = rev
		}(i, tok)
	}
	wg.Wait()
	savePhase.report("稳定保存")

	// 阶段三：同时交卷。
	submitPhase := &latencies{}
	for i, tok := range tokens {
		wg.Add(1)
		go func(i int, tok string) {
			defer wg.Done()
			c := &client{base: *base, token: tok, client: &http.Client{Timeout: 30 * time.Second}}
			d, _, err := c.do("POST", fmt.Sprintf("/api/exam/exams/%d/attempts/submit", examID),
				map[string]any{"expected_revision": revs[i]})
			submitPhase.add(d, err)
		}(i, tok)
	}
	wg.Wait()
	submitPhase.report("同时交卷")

	// 教师成绩查询。
	resultsPhase := &latencies{}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, _, err := adminC.do("GET", fmt.Sprintf("/api/exam/exams/%d/results", examID), nil)
			resultsPhase.add(d, err)
		}()
	}
	wg.Wait()
	resultsPhase.report("成绩查询")

	fmt.Println("容量验证完成。以上为实测数据；压测数据带 loadtest_ 前缀，可按前缀清理。")
}
