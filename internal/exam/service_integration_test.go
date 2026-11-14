package exam

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/config"
	"github.com/hequan2017/bigmodel/internal/database"
)

// newTestService 在独立 schema 中构建测试环境；数据库不可用时跳过。
func newTestService(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()
	cfg, err := config.Load(nil)
	if err != nil {
		t.Skipf("配置不可用，跳过集成测试: %v", err)
	}
	ctx := context.Background()
	h := fnv32(t.Name())
	schema := fmt.Sprintf("itest_exam_%x_%d", h, time.Now().UnixNano())
	if err := database.EnsureSchema(ctx, cfg, schema); err != nil {
		t.Skipf("创建测试 schema 失败: %v", err)
	}
	migPool, err := database.ConnectWithSchema(ctx, cfg, schema, false)
	if err != nil {
		_ = database.DropSchema(ctx, cfg, schema)
		t.Skipf("连接迁移 schema 失败: %v", err)
	}
	if err := database.Migrate(ctx, migPool); err != nil {
		migPool.Close()
		_ = database.DropSchema(ctx, cfg, schema)
		t.Skipf("测试 schema 迁移失败: %v", err)
	}
	migPool.Close()
	pool, err := database.ConnectWithSchema(ctx, cfg, schema, true)
	if err != nil {
		_ = database.DropSchema(ctx, cfg, schema)
		t.Skipf("连接测试 schema 失败: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_ = database.DropSchema(ctx, cfg, schema)
	})
	return NewService(pool), pool
}

func fnv32(s string) uint32 {
	var h uint32 = 2166136261
	for _, c := range []byte(s) {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

func createExamUser(t *testing.T, pool *pgxpool.Pool, login, role string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (login_name, password_hash, role) VALUES ($1, 'x', $2) RETURNING id`,
		login, role,
	).Scan(&id); err != nil {
		t.Fatalf("创建测试用户 %s: %v", login, err)
	}
	return id
}

func TestQuestionFlowIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	teacherA := createExamUser(t, pool, "itest_e_a_"+suffix, "teacher")
	teacherB := createExamUser(t, pool, "itest_e_b_"+suffix, "teacher")

	// 单选题。
	single := QuestionInput{
		Type:          "single",
		Stem:          "RAG 的中文含义是什么？",
		Options:       []Option{{Key: "A", Text: "检索增强生成"}, {Key: "B", Text: "生成式对抗网络"}, {Key: "C", Text: "强化学习"}},
		CorrectAnswer: "a", // 小写应归一化
		DefaultScore:  5,
		LevelCode:     "L1",
		Tags:          []string{"RAG", "基础"},
	}
	q1, err := svc.CreateQuestion(ctx, teacherA, single)
	if err != nil {
		t.Fatalf("创建单选题: %v", err)
	}
	if q1.CorrectAnswer != "A" || len(q1.Options) != 3 || q1.Status != StatusActive {
		t.Errorf("单选题数据错误: %+v", q1)
	}

	// 判断题（携带的 options 应被忽略）。
	judge := QuestionInput{
		Type:          "judge",
		Stem:          "RAG 可以完全替代模型微调。",
		Options:       []Option{{Key: "A", Text: "多余选项"}},
		CorrectAnswer: "False",
		DefaultScore:  2,
	}
	q2, err := svc.CreateQuestion(ctx, teacherA, judge)
	if err != nil {
		t.Fatalf("创建判断题: %v", err)
	}
	if q2.CorrectAnswer != "false" || q2.Options != nil {
		t.Errorf("判断题数据错误: %+v", q2)
	}

	// 非法数据用例。
	invalid := []struct {
		name string
		in   QuestionInput
	}{
		{"未知题型", QuestionInput{Type: "essay", Stem: "x", CorrectAnswer: "A", DefaultScore: 1}},
		{"答案不在选项中", QuestionInput{Type: "single", Stem: "x", Options: []Option{{Key: "A", Text: "a"}, {Key: "B", Text: "b"}}, CorrectAnswer: "D", DefaultScore: 1}},
		{"选项键重复", QuestionInput{Type: "single", Stem: "x", Options: []Option{{Key: "A", Text: "a"}, {Key: "A", Text: "b"}}, CorrectAnswer: "A", DefaultScore: 1}},
		{"选项过少", QuestionInput{Type: "single", Stem: "x", Options: []Option{{Key: "A", Text: "a"}}, CorrectAnswer: "A", DefaultScore: 1}},
		{"判断题答案非法", QuestionInput{Type: "judge", Stem: "x", CorrectAnswer: "maybe", DefaultScore: 1}},
		{"负分", QuestionInput{Type: "judge", Stem: "x", CorrectAnswer: "true", DefaultScore: -1}},
		{"空题干", QuestionInput{Type: "judge", Stem: "  ", CorrectAnswer: "true", DefaultScore: 1}},
	}
	for _, tc := range invalid {
		if _, err := svc.CreateQuestion(ctx, teacherA, tc.in); !errors.Is(err, ErrInvalidData) {
			t.Errorf("%s: 应 ErrInvalidData, got %v", tc.name, err)
		}
	}

	// 更新。
	single.CorrectAnswer = "B"
	single.DefaultScore = 10
	updated, err := svc.UpdateQuestion(ctx, teacherA, false, q1.ID, single)
	if err != nil || updated.CorrectAnswer != "B" || updated.DefaultScore != 10 {
		t.Errorf("更新题目: %+v err=%v", updated, err)
	}

	// 越权：教师 B 不可见/不可改教师 A 的题目。
	if _, err := svc.GetQuestion(ctx, teacherB, false, q1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("越权访问应 404, got %v", err)
	}
	if _, err := svc.UpdateQuestion(ctx, teacherB, false, q1.ID, single); !errors.Is(err, ErrNotFound) {
		t.Errorf("越权更新应 404, got %v", err)
	}

	// 停用与列表过滤。
	if err := svc.DisableQuestion(ctx, teacherA, false, q2.ID); err != nil {
		t.Fatalf("停用: %v", err)
	}
	if err := svc.DisableQuestion(ctx, teacherA, false, q2.ID); err != nil {
		t.Errorf("重复停用应幂等: %v", err)
	}
	if _, err := svc.UpdateQuestion(ctx, teacherA, false, q2.ID, judge); !errors.Is(err, ErrInvalidState) {
		t.Errorf("已停用题目更新应 ErrInvalidState, got %v", err)
	}
	listA, err := svc.ListQuestions(ctx, teacherA, false, "")
	if err != nil || len(listA) != 1 || listA[0].ID != q1.ID {
		t.Errorf("教师 A 列表应只有 q1: %v err=%v", listA, err)
	}
	listJudge, err := svc.ListQuestions(ctx, teacherA, false, "judge")
	if err != nil || len(listJudge) != 0 {
		t.Errorf("judge 过滤应为空: %v err=%v", listJudge, err)
	}
	listB, err := svc.ListQuestions(ctx, teacherB, false, "")
	if err != nil || len(listB) != 0 {
		t.Errorf("教师 B 不应看到他人题目: %v err=%v", listB, err)
	}
	listAdmin, err := svc.ListQuestions(ctx, teacherB, true, "single")
	if err != nil || len(listAdmin) != 1 {
		t.Errorf("管理员 single 过滤应看到 1 题: %v err=%v", listAdmin, err)
	}
}
