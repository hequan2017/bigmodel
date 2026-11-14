package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/config"
	"github.com/hequan2017/bigmodel/internal/database"
	"github.com/hequan2017/bigmodel/internal/kb"
	"github.com/hequan2017/bigmodel/internal/model"
)

type fakeEmbedder struct {
	dim  int
	fail bool
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.fail {
		return nil, &model.UpstreamError{Code: "upstream_status", Status: 500, Message: "模拟故障"}
	}
	out := make([][]float32, len(texts))
	for i := range out {
		v := make([]float32, f.dim)
		for j := range v {
			v[j] = float32(j+1) / 10
		}
		out[i] = v
	}
	return out, nil
}

type fakeChat struct {
	lastPrompt string
	fail       bool
}

func (f *fakeChat) Stream(_ context.Context, req model.ChatRequest, emit func(model.ChatEvent) error) error {
	if f.fail {
		return &model.UpstreamError{Code: "upstream_status", Status: 500, Message: "模拟故障"}
	}
	if len(req.Messages) > 0 {
		f.lastPrompt = req.Messages[len(req.Messages)-1].Content
	}
	for _, d := range []string{"你好", "，", "这是RAG回答"} {
		if err := emit(model.ChatEvent{Delta: d}); err != nil {
			return err
		}
	}
	return emit(model.ChatEvent{Done: true, Usage: &model.Usage{PromptTokens: 7, CompletionTokens: 3}})
}

// blockingChat 阻塞到 ctx 取消，用于测试客户端断开/取消路径。
type blockingChat struct {
	started chan struct{}
}

func (b *blockingChat) Stream(ctx context.Context, _ model.ChatRequest, _ func(model.ChatEvent) error) error {
	close(b.started)
	<-ctx.Done()
	return ctx.Err()
}

func newTestEnv(t *testing.T) (*Service, *kb.Service, *pgxpool.Pool) {
	t.Helper()
	cfg, err := config.Load(nil)
	if err != nil {
		t.Skipf("配置不可用，跳过集成测试: %v", err)
	}
	ctx := context.Background()
	schema := "itest_app_" + fmt.Sprintf("%d", time.Now().UnixNano())
	if err := database.EnsureSchema(ctx, cfg, schema); err != nil {
		t.Skipf("创建测试 schema 失败: %v", err)
	}
	pool, err := database.ConnectWithSchema(ctx, cfg, schema)
	if err != nil {
		_ = database.DropSchema(ctx, cfg, schema)
		t.Skipf("连接测试 schema 失败: %v", err)
	}
	if err := database.Migrate(ctx, pool); err != nil {
		pool.Close()
		_ = database.DropSchema(ctx, cfg, schema)
		t.Skipf("测试 schema 迁移失败: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_ = database.DropSchema(ctx, cfg, schema)
	})
	kbSvc := kb.NewService(pool, t.TempDir())
	return NewService(pool, kbSvc), kbSvc, pool
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func createAppTestUser(t *testing.T, pool *pgxpool.Pool, login string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (login_name, password_hash, role) VALUES ($1, 'x', 'teacher') RETURNING id`,
		login,
	).Scan(&id); err != nil {
		t.Fatalf("创建测试用户 %s: %v", login, err)
	}
	return id
}

func createStudent(t *testing.T, pool *pgxpool.Pool, login string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (login_name, password_hash, role) VALUES ($1, 'x', 'student') RETURNING id`,
		login,
	).Scan(&id); err != nil {
		t.Fatalf("创建测试学生 %s: %v", login, err)
	}
	return id
}

func seedReadyDoc(t *testing.T, kbSvc *kb.Service, kbID, userID int64, embed *fakeEmbedder) {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, "## 第%d节\n\n", i+1)
		b.WriteString(strings.Repeat(fmt.Sprintf("第%d节讲解检索增强生成的核心原理。", i+1), 60))
		b.WriteString("\n\n")
	}
	if _, _, err := kbSvc.UploadDocument(ctx, userID, false, kbID,
		"doc.md", "text/markdown", []byte(b.String())); err != nil {
		t.Fatalf("上传文档: %v", err)
	}
	worker := kbSvc.NewWorker(embed, "fake-embed", time.Second, testLogger())
	more, err := worker.ProcessOnce(ctx)
	if err != nil || !more {
		t.Fatalf("处理文档: more=%v err=%v", more, err)
	}
}

func collectEvents(events *[]Event) func(Event) error {
	return func(ev Event) error {
		*events = append(*events, ev)
		return nil
	}
}

func TestAppLifecycleAndRunIntegration(t *testing.T) {
	svc, kbSvc, pool := newTestEnv(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	teacherID := createAppTestUser(t, pool, "itest_a_"+suffix)
	studentID := createStudent(t, pool, "itest_a_stu_"+suffix)

	embed := &fakeEmbedder{dim: 4}
	kbRow, err := kbSvc.CreateKB(ctx, teacherID, "应用测试库", "")
	if err != nil {
		t.Fatalf("创建知识库: %v", err)
	}
	seedReadyDoc(t, kbSvc, kbRow.ID, teacherID, embed)

	goodCfg := AppConfig{
		KnowledgeBaseID: kbRow.ID,
		Prompt:          "资料: {{context}}\n问题: {{question}}\n历史: {{history}}",
		TopK:            5,
		HistoryTurns:    1,
	}

	// 配置校验。
	bad := goodCfg
	bad.Prompt = "{{foo}}"
	if _, err := svc.CreateDraft(ctx, teacherID, "A", bad); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("未知变量应 ErrInvalidConfig, got %v", err)
	}
	badKB := goodCfg
	badKB.KnowledgeBaseID = 999999
	if _, err := svc.CreateDraft(ctx, teacherID, "A", badKB); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("绑定不存在知识库应 ErrInvalidConfig, got %v", err)
	}
	badTopK := goodCfg
	badTopK.TopK = 99
	if _, err := svc.CreateDraft(ctx, teacherID, "A", badTopK); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("topK 超限应 ErrInvalidConfig, got %v", err)
	}

	a, err := svc.CreateDraft(ctx, teacherID, "教育问答助手", goodCfg)
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}

	// 学生运行未发布应用 → 404 防枚举。
	chat := &fakeChat{}
	events := []Event{}
	err = svc.RunApp(ctx, RunParams{
		UserID: studentID, Role: "student", AppID: a.ID, Question: "第一个问题",
		Chat: chat, ChatModel: "mock-chat", Embed: embed, EmbedModel: "fake-embed",
	}, collectEvents(&events))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("学生运行草稿应用应 ErrNotFound, got %v", err)
	}

	// 发布 → revision 1。
	published, err := svc.Publish(ctx, teacherID, "teacher", false, a.ID)
	if err != nil || published.PublishedRevision != 1 || published.Status != StatusPublished {
		t.Fatalf("发布: %+v err=%v", published, err)
	}

	// 学生运行（发布快照）。
	events = nil
	if err := svc.RunApp(ctx, RunParams{
		UserID: studentID, Role: "student", AppID: a.ID, Question: "第一个问题",
		Chat: chat, ChatModel: "mock-chat", Embed: embed, EmbedModel: "fake-embed",
	}, collectEvents(&events)); err != nil {
		t.Fatalf("学生运行: %v", err)
	}
	types := make([]string, 0, len(events))
	for _, ev := range events {
		types = append(types, ev.Type)
	}
	wantTypes := []string{"start", "references", "delta", "delta", "delta", "done"}
	if len(types) != len(wantTypes) {
		t.Fatalf("事件序列 %v 与期望 %v 不符", types, wantTypes)
	}
	for i := range wantTypes {
		if types[i] != wantTypes[i] {
			t.Errorf("事件 %d = %s, 期望 %s", i, types[i], wantTypes[i])
		}
	}
	startPayload := events[0].Payload.(map[string]any)
	convID := startPayload["conversation_id"].(string)
	if startPayload["mode"] != ModePublished || startPayload["revision"].(int) != 1 {
		t.Errorf("start 事件错误: %+v", startPayload)
	}
	donePayload := events[len(events)-1].Payload.(map[string]any)
	if donePayload["status"] != RunSucceeded {
		t.Errorf("done 状态 = %v", donePayload["status"])
	}
	if _, ok := donePayload["usage"]; !ok {
		t.Error("供应商返回 usage 时应包含在 done 事件")
	}

	// 运行记录与会话消息。
	var runStatus, runMode string
	var revision int
	if err := pool.QueryRow(ctx,
		`SELECT status, mode, revision FROM app_runs WHERE conversation_id = $1`, convID,
	).Scan(&runStatus, &runMode, &revision); err != nil {
		t.Fatalf("查询运行记录: %v", err)
	}
	if runStatus != RunSucceeded || runMode != ModePublished || revision != 1 {
		t.Errorf("运行记录错误: %s %s rev=%d", runStatus, runMode, revision)
	}
	var msgCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM app_messages WHERE conversation_id = $1`, convID).Scan(&msgCount); err != nil {
		t.Fatalf("查询消息: %v", err)
	}
	if msgCount != 2 {
		t.Errorf("成功运行应保存 2 条消息, got %d", msgCount)
	}

	// 会话历史：第二轮运行注入第一轮问答。
	events = nil
	if err := svc.RunApp(ctx, RunParams{
		UserID: studentID, Role: "student", AppID: a.ID, Question: "第二个问题",
		ConversationID: convID,
		Chat:           chat, ChatModel: "mock-chat", Embed: embed, EmbedModel: "fake-embed",
	}, collectEvents(&events)); err != nil {
		t.Fatalf("第二轮运行: %v", err)
	}
	if !strings.Contains(chat.lastPrompt, "第一个问题") || !strings.Contains(chat.lastPrompt, "你好，这是RAG回答") {
		t.Errorf("第二轮 Prompt 未注入历史: %q", chat.lastPrompt)
	}

	// 教师调试草稿：发布快照不受草稿修改影响。
	draftCfg := goodCfg
	draftCfg.Prompt = "调试版 {{question}}"
	if _, err := svc.UpdateDraft(ctx, teacherID, "teacher", false, a.ID, draftCfg); err != nil {
		t.Fatalf("更新草稿: %v", err)
	}
	full, err := svc.GetForOwner(ctx, a.ID)
	if err != nil || full.PublishedConfig == nil || full.PublishedConfig.Prompt == draftCfg.Prompt {
		t.Errorf("发布快照被草稿修改影响: %+v err=%v", full.PublishedConfig, err)
	}
	events = nil
	if err := svc.RunApp(ctx, RunParams{
		UserID: teacherID, Role: "teacher", AppID: a.ID, Question: "调试问题",
		Chat: chat, ChatModel: "mock-chat", Embed: embed, EmbedModel: "fake-embed",
	}, collectEvents(&events)); err != nil {
		t.Fatalf("教师调试运行: %v", err)
	}
	if events[0].Payload.(map[string]any)["mode"] != ModeDebug {
		t.Errorf("教师运行应为 debug 模式: %+v", events[0].Payload)
	}
	if !strings.Contains(chat.lastPrompt, "调试版") {
		t.Errorf("调试运行未使用草稿 Prompt: %q", chat.lastPrompt)
	}

	// 停用：学生不可见（404），教师不可运行（409）。
	if err := svc.Disable(ctx, teacherID, "teacher", false, a.ID); err != nil {
		t.Fatalf("停用: %v", err)
	}
	if _, err := svc.Get(ctx, studentID, "student", false, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("停用后学生访问应 404, got %v", err)
	}
	err = svc.RunApp(ctx, RunParams{
		UserID: teacherID, Role: "teacher", AppID: a.ID, Question: "q",
		Chat: chat, ChatModel: "mock-chat", Embed: embed, EmbedModel: "fake-embed",
	}, collectEvents(&events))
	if !errors.Is(err, ErrNotRunnable) {
		t.Errorf("停用后教师运行应 ErrNotRunnable, got %v", err)
	}

	// 再次发布（基于新草稿）→ revision 2。
	published, err = svc.Publish(ctx, teacherID, "teacher", false, a.ID)
	if err != nil || published.PublishedRevision != 2 {
		t.Errorf("再次发布: rev=%d err=%v", published.PublishedRevision, err)
	}
}

func TestAppRunFailureAndCancelIntegration(t *testing.T) {
	svc, kbSvc, pool := newTestEnv(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	teacherID := createAppTestUser(t, pool, "itest_b_"+suffix)
	embed := &fakeEmbedder{dim: 4}

	kbRow, err := kbSvc.CreateKB(ctx, teacherID, "失败测试库", "")
	if err != nil {
		t.Fatalf("创建知识库: %v", err)
	}
	seedReadyDoc(t, kbSvc, kbRow.ID, teacherID, embed)

	a, err := svc.CreateDraft(ctx, teacherID, "失败应用", AppConfig{
		KnowledgeBaseID: kbRow.ID,
		Prompt:          "{{question}}",
		TopK:            5,
	})
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	if _, err := svc.Publish(ctx, teacherID, "teacher", false, a.ID); err != nil {
		t.Fatalf("发布: %v", err)
	}

	// 上游故障 → error 事件 + 运行 failed。
	events := []Event{}
	if err := svc.RunApp(ctx, RunParams{
		UserID: teacherID, Role: "teacher", AppID: a.ID, Question: "q",
		Chat: &fakeChat{fail: true}, ChatModel: "mock-chat", Embed: embed, EmbedModel: "fake-embed",
	}, collectEvents(&events)); err != nil {
		t.Fatalf("运行: %v", err)
	}
	last := events[len(events)-1]
	if last.Type != "error" || last.Payload.(map[string]any)["code"] != "upstream_status" {
		t.Errorf("上游故障应返回 error 事件: %+v", last)
	}

	// 取消 → canceled，且不写会话历史。
	blocking := &blockingChat{started: make(chan struct{})}
	done := make(chan error, 1)
	ctx2, cancel := context.WithCancel(context.Background())
	go func() {
		done <- svc.RunApp(ctx2, RunParams{
			UserID: teacherID, Role: "teacher", AppID: a.ID, Question: "会被取消的问题",
			Chat: blocking, ChatModel: "mock-chat", Embed: embed, EmbedModel: "fake-embed",
		}, collectEvents(&events))
	}()
	<-blocking.started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消运行返回错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消运行超时")
	}

	var runStatus string
	var conv string
	if err := pool.QueryRow(ctx,
		`SELECT status, conversation_id FROM app_runs WHERE question = '会被取消的问题' ORDER BY id DESC LIMIT 1`,
	).Scan(&runStatus, &conv); err != nil {
		t.Fatalf("查询运行记录: %v", err)
	}
	if runStatus != RunCanceled {
		t.Errorf("被取消运行应为 canceled, got %s", runStatus)
	}
	var msgCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM app_messages WHERE conversation_id = $1`, conv).Scan(&msgCount); err != nil {
		t.Fatalf("查询消息: %v", err)
	}
	if msgCount != 0 {
		t.Errorf("取消运行不应写入会话历史, got %d 条", msgCount)
	}
}
