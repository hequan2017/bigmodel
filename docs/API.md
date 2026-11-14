# API 接口文档

全部接口为同源 HTTP JSON（`Content-Type: application/json`），除健康检查与登录外
均需携带 `Authorization: Bearer <token>`（登录获得）。错误统一为
`{"error":{"code","message"},"request_id"}` 结构；事件流为 SSE（§9.4）。

角色标注：A=admin，T=teacher，S=student。

## 健康检查（无认证）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /healthz | 存活探针：进程可响应即 200 |
| GET | /readyz | 就绪探针：数据库可达且数据目录可写 |
| GET | /api/version | 构建版本与启动时间（无认证，排障用） |

## 认证

| 方法 | 路径 | 角色 | 说明 |
|---|---|---|---|
| POST | /api/auth/login | 无 | `{login_name,password}` → `{token,user}` |
| POST | /api/auth/logout | 已登录 | 使服务端会话失效（幂等） |
| GET | /api/auth/me | 已登录 | 当前用户 `{user:{id,login_name,role}}` |

## 管理员

| 方法 | 路径 | 角色 | 说明 |
|---|---|---|---|
| GET | /api/admin/models | A | 模型档案列表（credential_ref 仅环境变量名） |
| POST | /api/admin/models/chat/test | A | 真实调用 Chat 上游，返回样例与耗时 |
| POST | /api/admin/models/embedding/test | A | 真实调用 Embedding 上游，实测维度并回写档案 |
| GET | /api/admin/audit?limit=&action= | A | 最近审计事件（倒序，可按动作过滤） |

## 知识库与文档

| 方法 | 路径 | 角色 | 说明 |
|---|---|---|---|
| POST | /api/kb | A,T | 创建知识库（绑定首个可用 Embedding 档案） |
| GET | /api/kb | A,T | 列表（教师本人；管理员全部；含文档计数） |
| GET | /api/kb/{id} | A,T | 详情 + 文档列表（不含 file_path） |
| POST | /api/kb/{id}/documents | A,T | 上传 TXT/Markdown（≤5MiB、UTF-8、内容哈希去重） |
| DELETE | /api/kb/{id}/documents/{docID} | A,T | 停用文档（立即不可检索，幂等） |
| POST | /api/kb/{id}/documents/{docID}/retry | A,T | 失败文档重新入队（仅 failed） |
| GET | /api/kb/{id}/documents/{docID}/chunks | A,T | 文档片段（文本与 rune 偏移，不含向量） |
| POST | /api/kb/{id}/search | A,T | 向量检索调试：问题 → 候选（含引用定位） |

## 问答应用

| 方法 | 路径 | 角色 | 说明 |
|---|---|---|---|
| POST | /api/apps | A,T | 创建草稿（配置后端 Schema 校验） |
| GET | /api/apps | 全部 | 学生仅见已发布；教师本人；管理员全部 |
| GET | /api/apps/{id} | 全部 | 详情（学生不含草稿配置） |
| PUT | /api/apps/{id} | A,T | 更新草稿（发布快照不受影响） |
| POST | /api/apps/{id}/publish | A,T | 发布（revision+1，冻结快照） |
| DELETE | /api/apps/{id} | A,T | 停用（拒绝新运行） |
| POST | /api/apps/{id}/run | 全部 | SSE 运行：start/references/delta/done/error |
| GET | /api/apps/{id}/messages?conversation_id= | 全部 | 本人该会话的历史消息（正序） |

## Prompt

| 方法 | 路径 | 角色 | 说明 |
|---|---|---|---|
| POST | /api/prompt/validate | A,T | `{template}` → `{valid,variables,error}` |
| POST | /api/prompt/render | A,T | 调试渲染（缺变量明确报错） |

## 考试后台

| 方法 | 路径 | 角色 | 说明 |
|---|---|---|---|
| POST | /api/exam/questions | A,T | 创建题目（单选/判断，答案服务端校验） |
| GET | /api/exam/questions?type= | A,T | 题目列表（教师侧含答案） |
| GET | /api/exam/questions/{id} | A,T | 题目详情 |
| PUT | /api/exam/questions/{id} | A,T | 更新题目（停用后不可改） |
| DELETE | /api/exam/questions/{id} | A,T | 停用题目（幂等软删除） |
| POST | /api/exam/exams | A,T | 创建考试草稿（时间窗口/时长可后补） |
| GET | /api/exam/exams?status= | A,T | 考试列表 |
| GET | /api/exam/exams/{id} | A,T | 考试详情（含条目快照与答案） |
| PUT | /api/exam/exams/{id} | A,T | 更新元信息（仅草稿） |
| PUT | /api/exam/exams/{id}/items | A,T | 整体替换试卷条目（生成快照） |
| POST | /api/exam/exams/{id}/publish | A,T | 发布（锁行校验总分/时间窗口，冻结） |
| DELETE | /api/exam/exams/{id} | A,T | 删除草稿（已发布不可删） |
| GET | /api/exam/exams/{id}/results | A,T | 成绩列表（登录名/总分/提交原因） |
| GET | /api/exam/exams/{id}/results/export | A,T | 成绩 CSV（UTF-8 BOM，attachment） |
| GET | /api/exam/exams/{id}/stats | A,T | 统计：提交/平均/逐题答对率 |
| GET | /api/exam/exams/{id}/attempts/{attemptID} | A,T | 单次作答逐题明细 |

## 学生作答

| 方法 | 路径 | 角色 | 说明 |
|---|---|---|---|
| GET | /api/exam/available | S | 可参加考试列表（窗口状态+作答状态+分数） |
| POST | /api/exam/exams/{id}/attempts | S | 开始考试（幂等，不重置计时） |
| GET | /api/exam/exams/{id}/attempts | S | 恢复作答（已保存答案+截止时间） |
| PUT | /api/exam/exams/{id}/attempts/answers | S | 自动保存（expected_revision 冲突 409） |
| POST | /api/exam/exams/{id}/attempts/submit | S | 交卷（幂等；过期做 timeout 结算） |
| GET | /api/exam/exams/{id}/attempts/result | S | 本人成绩（总分与提交状态） |

## 学生问答使用

学生不直接访问知识库/检索接口——问答通过已发布应用（授权由发布建立）：

| 方法 | 路径 | 角色 | 说明 |
|---|---|---|---|
| POST | /api/apps/{id}/run | 全部 | 运行问答（学生=发布快照；教师=草稿调试） |
| GET | /api/apps/{id}/messages?conversation_id= | 全部 | 本人该会话历史消息 |

## 页面（同源托管）

| 路径 | 说明 |
|---|---|
| / | 登录页（按角色跳转） |
| /exam.html | 学生考试（列表/作答/倒计时/结果） |
| /chat.html | 学生问答（SSE 对话/引用/历史还原） |
| /teacher.html | 教师与管理员工作台 |

## 主要错误码

| code | HTTP | 含义 |
|---|---|---|
| unauthorized / invalid_credentials | 401 | 未登录 / 凭据错误 |
| forbidden | 403 | 角色无权 |
| not_found | 404 | 资源不存在（越权统一 404 防枚举） |
| duplicate_name / duplicate_exam | 409 | 同名冲突 |
| revision_conflict | 409 | 答案版本冲突 |
| attempt_expired / exam_closed | 409 | 已过截止 / 已截止 |
| embedding_not_configured / chat_not_configured | 409 | 模型上游未配置 |
| invalid_file / file_too_large | 400/413 | 上传校验失败 |
| upstream_status / upstream_timeout / upstream_unavailable / upstream_canceled | 502 | 模型上游错误（§5.3） |
