// Command seed 初始化平台基础数据（幂等，可重复执行）：
//   - 三种角色的演示账号（口令可用 APP_SEED_*_PASSWORD 覆盖）
//   - -demo=true（默认）时创建演示知识库、4 份原创教学资料与 16 道客观题
//
// 文档上传后状态为 queued，由服务端 worker 在运行时向量化。
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hequan2017/bigmodel/internal/auth"
	"github.com/hequan2017/bigmodel/internal/config"
	"github.com/hequan2017/bigmodel/internal/database"
	"github.com/hequan2017/bigmodel/internal/exam"
	"github.com/hequan2017/bigmodel/internal/kb"
)

//go:embed demo
var demoFS embed.FS

type account struct {
	LoginName string
	Password  string
	Role      string
}

func main() {
	demo := true
	for i, arg := range os.Args {
		if arg == "-demo" || arg == "--demo" {
			if i+1 < len(os.Args) {
				demo = os.Args[i+1] != "false"
			}
		}
	}
	if err := run(demo); err != nil {
		fmt.Fprintln(os.Stderr, "初始化失败:", err)
		os.Exit(1)
	}
}

func run(demo bool) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("加载配置: %w", err)
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := database.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("执行迁移: %w", err)
	}

	if err := seedAccounts(ctx, pool); err != nil {
		return err
	}
	if demo {
		if err := seedDemo(ctx, pool, cfg); err != nil {
			return fmt.Errorf("创建演示数据: %w", err)
		}
	}
	fmt.Println("提示：口令可通过 APP_SEED_*_PASSWORD 环境变量覆盖")
	return nil
}

func seedAccounts(ctx context.Context, pool *pgxpool.Pool) error {
	accounts := []account{
		{LoginName: "admin", Password: firstNonEmpty(os.Getenv("APP_SEED_ADMIN_PASSWORD"), "admin@123"), Role: auth.RoleAdmin},
		{LoginName: "teacher", Password: firstNonEmpty(os.Getenv("APP_SEED_TEACHER_PASSWORD"), "teacher@123"), Role: auth.RoleTeacher},
		{LoginName: "student", Password: firstNonEmpty(os.Getenv("APP_SEED_STUDENT_PASSWORD"), "student@123"), Role: auth.RoleStudent},
	}
	for _, a := range accounts {
		hash, err := auth.HashPassword(a.Password)
		if err != nil {
			return fmt.Errorf("生成口令哈希 %s: %w", a.LoginName, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO users (login_name, password_hash, role)
			VALUES ($1, $2, $3)
			ON CONFLICT (login_name) DO UPDATE
			SET password_hash = EXCLUDED.password_hash, enabled = TRUE`,
			a.LoginName, hash, a.Role,
		); err != nil {
			return fmt.Errorf("创建账号 %s: %w", a.LoginName, err)
		}
		fmt.Printf("已就绪账号: %s (%s)\n", a.LoginName, a.Role)
	}
	return nil
}

// demoDoc 是一份演示教学资料。
type demoDoc struct {
	DisplayName string
	Content     string
}

// demoQuestions 是与演示资料配套的客观题（全部依据文档内容命题）。
func demoQuestions() []exam.QuestionInput {
	single := []struct {
		stem    string
		opts    []string
		correct string
	}{
		{"大语言模型训练的三个阶段中，成本最高的是哪一个？", []string{"预训练", "监督微调", "对齐训练", "指令调优"}, "A"},
		{"RAG 的中文全称是什么？", []string{"检索增强生成", "生成式对抗网络", "强化学习", "多模态融合"}, "A"},
		{"在 RAG 流程中，把文档片段转换成向量的组件是？", []string{"嵌入模型", "重排序模型", "判别模型", "词法分析器"}, "A"},
		{"温度（temperature）参数越低，模型输出越？", []string{"确定", "发散", "冗长", "简短"}, "A"},
		{"下列哪项是提示词平台允许使用的变量？", []string{"{{question}}", "{{user_input}}", "{{sql}}", "{{file}}"}, "A"},
		{"检索不到相关资料时，理想的系统行为是？", []string{"明确说明无法从资料确认", "编造一个答案", "忽略问题", "重复问题"}, "A"},
		{"教师试图访问其他教师的资源，系统应返回？", []string{"不存在", "该资源详情", "部分内容", "资源路径"}, "A"},
		{"考试截止时间应由谁计算？", []string{"服务端数据库时间", "学生本机时钟", "教师浏览器", "任意客户端"}, "A"},
		{"属于教师权限范围的操作是？", []string{"管理本人创建的题库与考试", "查看全部用户口令", "修改任意学生的作答", "关闭平台服务"}, "A"},
		{"向量检索时进行相似度比较的对象是？", []string{"问题的向量与片段向量", "问题的原文与答案", "问题的哈希与文件哈希", "问题的字数与片段字数"}, "A"},
	}
	questions := []exam.QuestionInput{}
	for i, q := range single {
		keys := []string{"A", "B", "C", "D"}
		opts := make([]exam.Option, 0, len(q.opts))
		for j, text := range q.opts {
			opts = append(opts, exam.Option{Key: keys[j], Text: text})
		}
		questions = append(questions, exam.QuestionInput{
			Type: exam.TypeSingle, Stem: q.stem, Options: opts,
			CorrectAnswer: q.correct, DefaultScore: 5,
			LevelCode: fmt.Sprintf("L%d", i%2+1), Tags: []string{"演示", "AI 基础"},
		})
	}
	judges := []struct {
		stem    string
		correct string
	}{
		{"大语言模型可能生成看似合理但错误的内容。", "true"},
		{"微调效果可以只凭一两个示例判断。", "false"},
		{"RAG 可以降低私有知识问答中的幻觉。", "true"},
		{"提示词校验可以替代服务端权限控制。", "false"},
		{"学生接口应当返回试题的正确答案。", "false"},
		{"页面刷新后，剩余考试时间应从服务端恢复。", "true"},
	}
	for _, q := range judges {
		questions = append(questions, exam.QuestionInput{
			Type: exam.TypeJudge, Stem: q.stem, CorrectAnswer: q.correct,
			DefaultScore: 3, LevelCode: "L1", Tags: []string{"演示", "AI 基础"},
		})
	}
	return questions
}

// seedDemo 创建演示知识库、上传教学资料并导入配套客观题（幂等）。
func seedDemo(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) error {
	var teacherID int64
	if err := pool.QueryRow(ctx,
		`SELECT id FROM users WHERE login_name = 'teacher'`).Scan(&teacherID); err != nil {
		return fmt.Errorf("查询教师账号: %w", err)
	}

	kbSvc := kb.NewService(pool, cfg.DataDir)
	kbRow, err := kbSvc.CreateKB(ctx, teacherID, "AI 教学演示库", "内置演示教学资料，供问答与检索演示")
	if errors.Is(err, kb.ErrDuplicateName) {
		// 已存在：查回既有知识库 ID。
		kbRow = &kb.KB{}
		if err := pool.QueryRow(ctx,
			`SELECT id FROM knowledge_bases WHERE owner_id = $1 AND name = $2`,
			teacherID, "AI 教学演示库").Scan(&kbRow.ID); err != nil {
			return fmt.Errorf("查询演示知识库: %w", err)
		}
	} else if err != nil {
		return err
	}

	// 上传演示文档；重复上传由服务端按内容哈希去重。
	entries, err := demoFS.ReadDir("demo")
	if err != nil {
		return err
	}
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		content, err := demoFS.ReadFile("demo/" + name)
		if err != nil {
			return err
		}
		doc, existing, err := kbSvc.UploadDocument(ctx, teacherID, false, kbRow.ID,
			name, "text/markdown", content)
		if err != nil {
			return fmt.Errorf("上传 %s: %w", name, err)
		}
		state := "新上传（queued）"
		if existing {
			state = "已存在（" + doc.Status + "）"
		}
		fmt.Printf("  演示资料: %s → %s\n", doc.DisplayName, state)
	}
	fmt.Println("  说明：文档由服务端 worker 在运行时向量化（需配置 Embedding 上游）")

	// 导入配套客观题（按题干去重）。
	qs := demoQuestions()
	created, skipped := 0, 0
	for _, in := range qs {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM exam_questions WHERE owner_id = $1 AND stem = $2)`,
			teacherID, in.Stem).Scan(&exists); err != nil {
			return err
		}
		if exists {
			skipped++
			continue
		}
		if _, err := svcCreateQuestion(ctx, pool, teacherID, in); err != nil {
			return err
		}
		created++
	}
	fmt.Printf("  客观题: 新增 %d 道，跳过已存在 %d 道\n", created, skipped)
	return nil
}

// svcCreateQuestion 借用题库服务的校验逻辑创建题目。
func svcCreateQuestion(ctx context.Context, pool *pgxpool.Pool, ownerID int64, in exam.QuestionInput) (*exam.Question, error) {
	svc := exam.NewService(pool)
	return svc.CreateQuestion(ctx, ownerID, in)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
