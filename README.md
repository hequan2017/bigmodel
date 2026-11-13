# bigmodel

Go 大模型应用开发平台（MVP）：知识问答链路 + 在线考试链路。

开发计划与模块设计见 [docs/Go大模型应用开发平台_两天开发计划与模块设计.md](docs/Go大模型应用开发平台_两天开发计划与模块设计.md)。

## 快速开始

```bash
# 1. 启动 PostgreSQL（含 pgvector）
docker compose up -d db

# 2. 编译并运行（启动时自动执行数据库迁移）
go run ./cmd/server

# 3. 健康检查
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

## 目录结构

```
cmd/server          入口进程
internal/config     部署配置加载（环境变量）
internal/database   连接池与迁移执行
internal/api        统一响应/错误结构与 request_id
internal/httpserver 路由、中间件与健康检查
migrations          内嵌 SQL 迁移（启动时自动按序执行）
```

## 当前进度（P0）

- [x] ① 项目骨架 / 配置加载 / 数据库迁移 / 健康检查
- [ ] ② 登录认证与 admin/teacher/student 三角色权限
- [ ] ③ 模型接入（ChatClient / EmbeddingClient 与连通性验证）
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
