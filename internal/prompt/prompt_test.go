package prompt

import (
	"strings"
	"testing"
)

func TestValidateAcceptsWhitelistedTemplate(t *testing.T) {
	tpl := "你是教学助手。\n问题: {{question}}\n资料: {{context}}\n历史: {{history}}"
	if err := Validate(tpl); err != nil {
		t.Fatalf("白名单模板应通过: %v", err)
	}
	vars := Variables(tpl)
	if len(vars) != 3 || vars[0] != "question" || vars[1] != "context" || vars[2] != "history" {
		t.Errorf("变量提取错误: %v", vars)
	}
}

func TestValidateRejectsUnknownVariable(t *testing.T) {
	err := Validate("回答 {{foo}} 的问题")
	if err == nil || !strings.Contains(err.Error(), "未知变量") || !strings.Contains(err.Error(), "foo") {
		t.Errorf("未知变量应报错并指出名称: %v", err)
	}
	// 大小写不同也算未知变量
	if err := Validate("{{Question}}"); err == nil {
		t.Error("大小写敏感，{{Question}} 应报错")
	}
}

func TestValidateRejectsUnclosedPlaceholder(t *testing.T) {
	cases := []string{
		"问题 {{question",
		"{{ }} 空占位符",
		"{{1abc}} 非法变量名",
	}
	for _, tpl := range cases {
		if err := Validate(tpl); err == nil {
			t.Errorf("应拒绝 %q", tpl)
		}
	}
}

func TestValidateAllowsPlainTemplate(t *testing.T) {
	// 无变量的纯文本模板合法；大括号单字符不受影响。
	if err := Validate("你是老师，请回答学生的数学问题。JSON 示例: {\"a\": 1}"); err != nil {
		t.Fatalf("纯文本模板应通过: %v", err)
	}
}

func TestVariablesDeduplicatesInOrder(t *testing.T) {
	vars := Variables("{{question}} 开头 {{context}} 中间 {{question}} 结尾 {{history}}")
	if len(vars) != 3 || vars[0] != "question" || vars[1] != "context" || vars[2] != "history" {
		t.Errorf("去重/顺序错误: %v", vars)
	}
}

func TestRenderReplacesAllOccurrences(t *testing.T) {
	tpl := "Q: {{question}}\n再次引用: {{question}}\n资料: {{context}}"
	out, err := Render(tpl, Inputs{
		"question": "什么是RAG",
		"context":  "检索到的证据",
	})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Count(out, "什么是RAG") != 2 || !strings.Contains(out, "检索到的证据") {
		t.Errorf("渲染结果错误: %q", out)
	}
	if strings.Contains(out, "{{") {
		t.Errorf("渲染后不应残留占位符: %q", out)
	}
}

func TestRenderReportsMissingVariables(t *testing.T) {
	_, err := Render("{{question}} {{context}} {{history}}", Inputs{"question": "q"})
	if err == nil || !strings.Contains(err.Error(), "缺少变量") ||
		!strings.Contains(err.Error(), "context") || !strings.Contains(err.Error(), "history") {
		t.Errorf("缺失变量应明确报错: %v", err)
	}
}

func TestRenderIgnoresExtraInputs(t *testing.T) {
	out, err := Render("问题: {{question}}", Inputs{
		"question": "q",
		"foo":      "不该被注入",
	})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if strings.Contains(out, "不该被注入") {
		t.Errorf("额外输入不应被注入: %q", out)
	}
}

func TestRenderPreservesChineseAndNewlines(t *testing.T) {
	tpl := "你是老师。\n问题：{{question}}\n证据：{{context}}"
	out, err := Render(tpl, Inputs{"question": "1+1等于几", "context": "教材第一页"})
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.Contains(out, "你是老师。\n问题：1+1等于几") {
		t.Errorf("中文与换行被破坏: %q", out)
	}
}
