#!/usr/bin/env bash
# 清理容量压测遗留数据（loadtest_ 前缀的账号、考试、作答、审计事件）。
# 用法：scripts/clean-loadtest.sh
# 说明：按依赖顺序删除；应用数据（loadtest 考试的题目/条目）随外键级联。
set -eu

PSQL='docker compose exec -T db psql -U bigmodel -d bigmodel -v ON_ERROR_STOP=1'

echo "清理前统计:"
$PSQL -t -c "
SELECT '  用户 ' || count(*) FROM users WHERE login_name LIKE 'loadtest_%'
UNION ALL SELECT '  考试 ' || count(*) FROM exams WHERE owner_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%')
UNION ALL SELECT '  审计 ' || count(*) FROM audit_events WHERE actor_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%');"

$PSQL -q <<'SQL'
-- 依赖顺序：片段/答案 → 条目/作答 → 题目/考试/应用运行 → 用户
DELETE FROM document_chunks WHERE document_id IN (
  SELECT d.id FROM documents d JOIN knowledge_bases k ON k.id = d.knowledge_base_id
  WHERE k.owner_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%'));
DELETE FROM documents WHERE knowledge_base_id IN (
  SELECT id FROM knowledge_bases WHERE owner_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%'));
DELETE FROM knowledge_bases WHERE owner_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%');
DELETE FROM exam_answers WHERE attempt_id IN (
  SELECT id FROM exam_attempts WHERE user_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%'));
DELETE FROM exam_attempts WHERE user_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%');
DELETE FROM exam_items WHERE exam_id IN (
  SELECT id FROM exams WHERE owner_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%'));
DELETE FROM exam_questions WHERE owner_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%');
DELETE FROM exams WHERE owner_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%');
DELETE FROM audit_events WHERE actor_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%');
DELETE FROM users WHERE login_name LIKE 'loadtest_%';
SQL

echo "清理后统计:"
$PSQL -t -c "
SELECT '  用户 ' || count(*) FROM users WHERE login_name LIKE 'loadtest_%'
UNION ALL SELECT '  考试 ' || count(*) FROM exams WHERE owner_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%')
UNION ALL SELECT '  审计 ' || count(*) FROM audit_events WHERE actor_id IN (SELECT id FROM users WHERE login_name LIKE 'loadtest_%');"

echo "清理完成。压测账号/考试/作答/审计已移除；演示数据（admin/teacher/student 与演示库）不受影响。"
