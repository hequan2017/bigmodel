# bigmodel

Go 大模型应用开发平台（MVP）：知识问答链路 + 在线考试链路。

开发计划与模块设计见 [docs/Go大模型应用开发平台_两天开发计划与模块设计.md](docs/Go大模型应用开发平台_两天开发计划与模块设计.md)。

## 快速开始

```bash
# 1. 启动 PostgreSQL（含 pgvector）
docker compose up -d db

# 2. 初始化演示账号（admin / teacher / student，幂等可重复执行）
go run ./cmd/seed

# 3. 编译并运行（启动时自动执行数据库迁移）
go run ./cmd/server

# 4. 健康检查
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
```

## 配置（环境变量）

| 变量 | 默认值 | 说明 |
|---|---|---|
| APP_HTTP_ADDR | `:8080` | HTTP 监听地址 |
| DATABASE_URL | `postgres://bigmodel:bigmodel@localhost:5432/bigmodel?sslmode=disable` | PostgreSQL 连接串（含口令，勿写入日志） |
| APP_DATA_DIR | `./data` | 原始资料等本地持久化目录 |
| APP_LOG_LEVEL | `info` | debug / info / warn / error |
| APP_SESSION_TTL | `12h` | 会话有效期（Go duration） |
| APP_CHAT_BASE_URL / APP_CHAT_API_KEY / APP_CHAT_MODEL | 未配置 | Chat 上游（OpenAI 兼容协议） |
| APP_EMBEDDING_BASE_URL / APP_EMBEDDING_API_KEY / APP_EMBEDDING_MODEL | 未配置 | Embedding 上游 |
| APP_SEED_*_PASSWORD | 见下方演示账号表 | 初始化账号口令（仅 seed 使用） |

## 目录结构

```
cmd/server          入口进程
cmd/seed            预置演示账号初始化
cmd/mockupstream    开发用 OpenAI 协议模拟上游（仅联调，不用于生产）
internal/config     部署配置加载（环境变量）
internal/database   连接池与迁移执行
internal/api        统一响应/错误结构与 request_id
internal/auth       登录认证、会话与角色权限
internal/audit      关键操作留痕
internal/model      Chat/Embedding 适配器与模型档案
internal/httpserver 路由、中间件与健康检查
migrations          内嵌 SQL 迁移（启动时自动按序执行）
```

## 演示账号

| 登录名 | 默认口令（环境变量覆盖） | 角色 |
|---|---|---|
| admin | `admin@123`（APP_SEED_ADMIN_PASSWORD） | 管理员 |
| teacher | `teacher@123`（APP_SEED_TEACHER_PASSWORD） | 教师 |
| student | `student@123`（APP_SEED_STUDENT_PASSWORD） | 学生 |

登录后携带 `Authorization: Bearer <token>` 访问业务接口：

```bash
TOKEN=$(curl -s -X POST http://127.0.0.1:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"login_name":"teacher","password":"teacher@123"}' | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')
curl -s http://127.0.0.1:8080/api/auth/me -H "Authorization: Bearer $TOKEN"
```

## 模型联调

生产环境通过 `APP_CHAT_*` / `APP_EMBEDDING_*` 指向 OpenAI 兼容的真实服务；本地没有上游时，可用开发用模拟器验证协议链路（明确标注为测试工具，不伪造生产结果）：

```bash
go run ./cmd/mockupstream   # 默认 :18081

APP_CHAT_BASE_URL=http://127.0.0.1:18081 APP_CHAT_API_KEY=dev APP_CHAT_MODEL=mock-chat \
APP_EMBEDDING_BASE_URL=http://127.0.0.1:18081 APP_EMBEDDING_API_KEY=dev APP_EMBEDDING_MODEL=mock-embedding \
go run ./cmd/server
```

管理员登录后调用 `POST /api/admin/models/chat/test` 与 `POST /api/admin/models/embedding/test` 实测连通性（真实发起一次上游调用；错误密钥、超时、取消均返回可辨识错误码）。

## 当前进度（P0）

- [x] ① 项目骨架 / 配置加载 / 数据库迁移 / 健康检查
- [x] ② 登录认证与 admin/teacher/student 三角色权限
- [x] ③ 模型接入（ChatClient / EmbeddingClient 与连通性验证）
- [ ] ④ 知识库创建与 TXT/Markdown 文档上传
- [ ] ⑤ 文档解析/切分/向量化 worker 与状态机
- [ ] ⑥ pgvector 向量检索与引用返回
- [ ] ⑦ Prompt 编辑与变量白名单校验
- [ ] ⑧ 问答应用草稿/发布与 SSE 流式问答
- [ ] ⑨ 考试题库（单选/判断）
- [ ] ⑩ 组卷与发布快照
- [ ] ⑪ 开始考试、自动保存与版本冲突控制
- [ ] ⑫ 交卷判分与到期自动结算
- [ ] ⑬ 学生端考试页面
