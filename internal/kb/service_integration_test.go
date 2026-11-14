package kb

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/config"
	"github.com/hequan2017/bigmodel/internal/database"
)

// newTestService 在独立 schema 中构建测试环境；数据库不可用时跳过集成测试。
// 独立 schema 保证测试不触碰共享开发数据（如演示知识库与全局处理队列）。
func newTestService(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()
	cfg, err := config.Load(nil)
	if err != nil {
		t.Skipf("配置不可用，跳过集成测试: %v", err)
	}
	ctx := context.Background()
	schema := testSchemaName(t)
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
	// 测试 schema 是全新库：预置一个可用的 Embedding 档案供建库绑定。
	if _, err := pool.Exec(ctx, `
		INSERT INTO model_profiles (name, kind, model_name, base_url, credential_ref)
		VALUES ($1, 'embedding', 'itest-model', 'http://127.0.0.1:1', 'ITEST')`,
		"itest-default-emb-"+schema); err != nil {
		t.Fatalf("创建默认测试档案: %v", err)
	}
	return NewService(pool, t.TempDir()), pool
}

func testSchemaName(t *testing.T) string {
	t.Helper()
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.Name()))
	return fmt.Sprintf("itest_%x_%d", h.Sum32(), time.Now().UnixNano())
}

func TestKBFlowIntegration(t *testing.T) {
	svc, pool := newTestService(t)
	ctx := context.Background()

	// 准备：两个教师用户 + 一个测试 Embedding 档案；结束时按依赖顺序清理。
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userA, userB := "itest_kb_a_"+suffix, "itest_kb_b_"+suffix
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (login_name, password_hash, role) VALUES ($1, 'x', 'teacher'), ($2, 'x', 'teacher')`,
		userA, userB); err != nil {
		t.Fatalf("创建测试用户: %v", err)
	}
	var modelID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO model_profiles (name, kind, model_name, base_url, credential_ref)
		VALUES ($1, 'embedding', 'itest-model', 'http://127.0.0.1:1', 'ITEST')
		RETURNING id`, "itest-embedding-"+suffix).Scan(&modelID); err != nil {
		t.Fatalf("创建测试档案: %v", err)
	}
	var userAID, userBID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE login_name = $1`, userA).Scan(&userAID); err != nil {
		t.Fatalf("查询用户A: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE login_name = $1`, userB).Scan(&userBID); err != nil {
		t.Fatalf("查询用户B: %v", err)
	}

	// 创建知识库：按服务规则绑定"首个可用"的 Embedding 档案。
	kbRow, err := svc.CreateKB(ctx, userAID, "测试知识库A", "集成测试用")
	if err != nil {
		t.Fatalf("创建知识库: %v", err)
	}
	var firstEnabledModelID int64
	if err := pool.QueryRow(ctx, `
		SELECT id FROM model_profiles WHERE kind = 'embedding' AND enabled
		ORDER BY id LIMIT 1`).Scan(&firstEnabledModelID); err != nil {
		t.Fatalf("查询首个可用档案: %v", err)
	}
	if kbRow.EmbeddingModelID == nil || *kbRow.EmbeddingModelID != firstEnabledModelID {
		t.Errorf("Embedding 模型未按规则绑定: %+v, 期望 %d", kbRow.EmbeddingModelID, firstEnabledModelID)
	}
	if kbRow.ChunkSize != 600 || kbRow.ChunkOverlap != 100 {
		t.Errorf("切分参数默认值错误: %d/%d", kbRow.ChunkSize, kbRow.ChunkOverlap)
	}
	if _, err := svc.CreateKB(ctx, userAID, "测试知识库A", ""); !isDupName(err) {
		t.Errorf("同名应返回 ErrDuplicateName, got %v", err)
	}

	// 权限：教师 B 不可见教师 A 的库；管理员可见。
	if _, _, err := svc.GetKB(ctx, userBID, false, kbRow.ID); !isNotFound(err) {
		t.Errorf("教师 B 访问他人知识库应 404, got %v", err)
	}
	if _, _, err := svc.GetKB(ctx, userBID, true, kbRow.ID); err != nil {
		t.Errorf("管理员应可访问: %v", err)
	}

	// 上传与去重。
	content := []byte("# 课程\n这是第一段。")
	doc, existing, err := svc.UploadDocument(ctx, userAID, false, kbRow.ID, "lesson.md", "text/markdown", content)
	if err != nil {
		t.Fatalf("上传文档: %v", err)
	}
	if existing {
		t.Error("首次上传不应标记为 existing")
	}
	if doc.Status != StatusQueued || doc.CharCount != utf8.RuneCountInString(string(content)) {
		t.Errorf("文档状态/字符数错误: %+v", doc)
	}
	saved, err := os.ReadFile(filepath.Join(svc.dataDir, "kb",
		fmt.Sprintf("%d", kbRow.ID), fmt.Sprintf("%d", doc.ID)+".md"))
	if err != nil || string(saved) != string(content) {
		t.Errorf("文件内容不一致: err=%v content=%q", err, saved)
	}

	again, existing, err := svc.UploadDocument(ctx, userAID, false, kbRow.ID, "lesson-copy.md", "text/markdown", content)
	if err != nil || !existing || again.ID != doc.ID {
		t.Errorf("同内容上传应复用已有文档: existing=%v id=%v err=%v", existing, again.ID, err)
	}

	// 非法上传。
	if _, _, err := svc.UploadDocument(ctx, userAID, false, kbRow.ID, "bad.png", "image/png", []byte("x")); !errors.Is(err, ErrInvalidFile) {
		t.Errorf("png 应拒绝: %v", err)
	}
	if _, _, err := svc.UploadDocument(ctx, userAID, false, kbRow.ID, "bad.txt", "text/plain", []byte{0xFF}); !errors.Is(err, ErrInvalidFile) {
		t.Errorf("非 UTF-8 应拒绝: %v", err)
	}
	big := make([]byte, MaxUploadBytes+1)
	if _, _, err := svc.UploadDocument(ctx, userAID, false, kbRow.ID, "big.txt", "text/plain", big); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("超大小应拒绝: %v", err)
	}

	// 停用与重新上传。
	if err := svc.DisableDocument(ctx, userAID, false, kbRow.ID, doc.ID); err != nil {
		t.Fatalf("停用文档: %v", err)
	}
	if err := svc.DisableDocument(ctx, userAID, false, kbRow.ID, doc.ID); err != nil {
		t.Errorf("重复停用应幂等: %v", err)
	}
	_, docs, err := svc.GetKB(ctx, userAID, false, kbRow.ID)
	if err != nil {
		t.Fatalf("查询详情: %v", err)
	}
	if len(docs) != 1 || docs[0].Status != StatusDisabled {
		t.Errorf("文档应停用: %+v", docs)
	}
	reup, existing, err := svc.UploadDocument(ctx, userAID, false, kbRow.ID, "lesson.md", "text/markdown", content)
	if err != nil || !existing || reup.Status != StatusQueued {
		t.Errorf("重新上传应重新入队: existing=%v status=%s err=%v", existing, reup.Status, err)
	}

	// 重试：仅 failed 状态允许。
	if _, err := svc.RetryDocument(ctx, userAID, false, kbRow.ID, doc.ID); !isInvalidState(err) {
		t.Errorf("非 failed 状态重试应 ErrInvalidState, got %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE documents SET status = 'failed', failure_reason = '模拟失败' WHERE id = $1`, doc.ID); err != nil {
		t.Fatalf("置为 failed: %v", err)
	}
	retried, err := svc.RetryDocument(ctx, userAID, false, kbRow.ID, doc.ID)
	if err != nil || retried.Status != StatusQueued || retried.RetryCount != 1 || retried.FailureReason != nil {
		t.Errorf("重试结果异常: %+v err=%v", retried, err)
	}

	// 列表：教师只看本人。
	list, err := svc.ListKBs(ctx, userAID, false)
	if err != nil || len(list) == 0 {
		t.Fatalf("教师列表: %v", err)
	}
	listB, err := svc.ListKBs(ctx, userBID, false)
	if err != nil || len(listB) != 0 {
		t.Errorf("教师 B 不应看到教师 A 的知识库: %v %+v", err, listB)
	}
}

func isDupName(err error) bool      { return errors.Is(err, ErrDuplicateName) }
func isNotFound(err error) bool     { return errors.Is(err, ErrNotFound) }
func isInvalidState(err error) bool { return errors.Is(err, ErrInvalidState) }
