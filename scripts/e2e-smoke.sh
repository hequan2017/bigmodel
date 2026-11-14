#!/usr/bin/env bash
# 双链路冒烟回归脚本：对运行中的服务验证关键路径。
# 前提：服务已启动（默认 http://127.0.0.1:18080），数据库与 seed 已执行，
#       mock 或真实模型上游可用（含 Embedding，否则知识库阶段跳过）。
# 用法：scripts/e2e-smoke.sh [BASE]
# 输出 PASS/FAIL 汇总；有 FAIL 时退出码为 1。
set -u
BASE="${1:-http://127.0.0.1:18080}"
DEMO_DOC="cmd/seed/demo/02-检索增强生成RAG入门.md"

PASS=0; FAIL=0
pass() { PASS=$((PASS+1)); echo "  PASS  $1"; }
fail() { FAIL=$((FAIL+1)); echo "  FAIL  $1"; }
check() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1（期望 $3，实际 $2）"; fi }

jqpy() { python3 -c "
import json,sys
d=json.load(sys.stdin)
print($1)
" 2>/dev/null; }

echo "=== 双链路冒烟回归（${BASE}）==="

# ---------- 认证 ----------
TOK_T=$(curl -s --noproxy '*' -X POST "$BASE/api/auth/login" -H 'Content-Type: application/json' \
  -d '{"login_name":"teacher","password":"teacher@123"}' | jqpy 'd["token"]')
TOK_S=$(curl -s --noproxy '*' -X POST "$BASE/api/auth/login" -H 'Content-Type: application/json' \
  -d '{"login_name":"student","password":"student@123"}' | jqpy 'd["token"]')
[ -n "$TOK_T" ] && pass "教师登录" || fail "教师登录"
[ -n "$TOK_S" ] && pass "学生登录" || fail "学生登录"
AH_T="Authorization: Bearer $TOK_T"
AH_S="Authorization: Bearer $TOK_S"

# ---------- 知识问答链路 ----------
TS=$(date +%s)
KB=$(curl -s --noproxy '*' -X POST "$BASE/api/kb" -H "$AH_T" -H 'Content-Type: application/json' \
  -d "{\"name\":\"冒烟测试库 $TS\",\"description\":\"e2e\"}" | jqpy 'd["knowledge_base"]["id"]')
[ -n "$KB" ] && pass "创建知识库 #$KB" || fail "创建知识库"

CODE=$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' -X POST "$BASE/api/kb/$KB/documents" \
  -H "$AH_T" -F "file=@$DEMO_DOC;type=text/markdown")
check "上传演示文档" "$CODE" "201"

sleep 5
STATUS=$(curl -s --noproxy '*' "$BASE/api/kb/$KB" -H "$AH_T" \
  | jqpy '[d["documents"][0]["status"] for d in [d]][0] if d["documents"] else "none"')
check "文档向量化 ready（worker）" "$STATUS" "ready"

SEARCH=$(curl -s --noproxy '*' -X POST "$BASE/api/kb/$KB/search" -H "$AH_T" -H 'Content-Type: application/json' \
  -d '{"question":"RAG 的基本流程","top_k":2}')
REFS=$(echo "$SEARCH" | jqpy 'len(d["candidates"])')
[ "$REFS" -ge 1 ] 2>/dev/null && pass "向量检索命中" || fail "向量检索命中（${REFS}）"

# ---------- 问答应用 ----------
APP=$(curl -s --noproxy '*' -X POST "$BASE/api/apps" -H "$AH_T" -H 'Content-Type: application/json' \
  -d "{\"name\":\"冒烟测试应用 $TS\",\"config\":{\"knowledge_base_id\":$KB,\"prompt\":\"资料:\\n{{context}}\\n问题: {{question}}\",\"top_k\":3,\"history_turns\":1}}" \
  | jqpy 'd["app"]["id"]')
[ -n "$APP" ] && pass "创建问答应用 #$APP" || fail "创建问答应用"

CODE=$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' -X POST "$BASE/api/apps/$APP/publish" -H "$AH_T")
check "发布应用" "$CODE" "200"

SSE=$(curl -s --noproxy '*' -X POST "$BASE/api/apps/$APP/run" -H "$AH_S" -H 'Content-Type: application/json' \
  -d '{"question":"冒烟：RAG 是什么"}')
ANSWER=$(echo "$SSE" | python3 -c '
import sys
text = ""
for line in sys.stdin:
    line = line.strip()
    if line.startswith("data: ") and line != "data: [DONE]":
        import json as j
        try:
            d = j.loads(line[6:])
            if "text" in d: text += d["text"]
        except Exception: pass
print(text[:40] if text else "(空)")')
[ -n "$ANSWER" ] && [ "$ANSWER" != "(空)" ] && pass "SSE 问答有回答（${ANSWER}）" || fail "SSE 问答无回答"

# ---------- 考试链路 ----------
OPEN=$(python3 -c "import datetime;print((datetime.datetime.now(datetime.timezone.utc)-datetime.timedelta(minutes=1)).isoformat())")
CLOSE=$(python3 -c "import datetime;print((datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(hours=2)).isoformat())")
EXAM=$(curl -s --noproxy '*' -X POST "$BASE/api/exam/exams" -H "$AH_T" -H 'Content-Type: application/json' \
  -d "{\"title\":\"冒烟测试考试 $TS\",\"level_code\":\"L1\",\"open_at\":\"$OPEN\",\"close_at\":\"$CLOSE\",\"duration_seconds\":7200}" \
  | jqpy 'd["exam"]["id"]')
[ -n "$EXAM" ] && pass "创建考试 #$EXAM" || fail "创建考试"

Q1=$(curl -s --noproxy '*' -X POST "$BASE/api/exam/questions" -H "$AH_T" -H 'Content-Type: application/json' \
  -d '{"type":"single","stem":"冒烟单选题","options":[{"key":"A","text":"甲"},{"key":"B","text":"乙"}],"correct_answer":"A","default_score":7}' \
  | jqpy 'd["question"]["id"]')
Q2=$(curl -s --noproxy '*' -X POST "$BASE/api/exam/questions" -H "$AH_T" -H 'Content-Type: application/json' \
  -d '{"type":"judge","stem":"冒烟判断题","correct_answer":"true","default_score":3}' \
  | jqpy 'd["question"]["id"]')
[ -n "$Q1" ] && [ -n "$Q2" ] && pass "创建两道题目" || fail "创建题目"

CODE=$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' -X PUT "$BASE/api/exam/exams/$EXAM/items" \
  -H "$AH_T" -H 'Content-Type: application/json' \
  -d "{\"items\":[{\"source_question_id\":$Q1,\"order_no\":1,\"score\":7},{\"source_question_id\":$Q2,\"order_no\":2,\"score\":3}]}")
check "设置试卷条目" "$CODE" "200"
CODE=$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' -X POST "$BASE/api/exam/exams/$EXAM/publish" -H "$AH_T")
check "发布考试" "$CODE" "200"

START=$(curl -s --noproxy '*' -X POST "$BASE/api/exam/exams/$EXAM/attempts" -H "$AH_S")
REV=$(echo "$START" | jqpy 'd["attempt"]["answer_revision"]')
ITEM1=$(echo "$START" | jqpy 'd["attempt"]["items"][0]["item_id"]')
ITEM2=$(echo "$START" | jqpy 'd["attempt"]["items"][1]["item_id"]')
LEAK=$(echo "$START" | grep -c correct_answer || true)
check "开始考试且答案不泄漏" "$LEAK" "0"

CODE=$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' -X PUT "$BASE/api/exam/exams/$EXAM/attempts/answers" \
  -H "$AH_S" -H 'Content-Type: application/json' \
  -d "{\"expected_revision\":$REV,\"answers\":[{\"item_id\":$ITEM1,\"answer\":\"A\"},{\"item_id\":$ITEM2,\"answer\":\"true\"}]}")
check "保存答案" "$CODE" "200"

CODE=$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' -X POST "$BASE/api/exam/exams/$EXAM/attempts/submit" \
  -H "$AH_S" -H 'Content-Type: application/json' -d '{"expected_revision":2}')
check "交卷" "$CODE" "200"

SCORE=$(curl -s --noproxy '*' "$BASE/api/exam/exams/$EXAM/attempts/result" -H "$AH_S" \
  | jqpy 'd["result"]["total_score"]')
check "判分正确（全对 10 分）" "$SCORE" "10"

# ---------- 汇总 ----------
echo "================================"
echo "冒烟回归完成：PASS ${PASS} / FAIL ${FAIL}"
[ "$FAIL" -eq 0 ] || exit 1
