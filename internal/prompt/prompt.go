// Package prompt 提供应用内 Prompt 模板的校验与渲染。
// 仅允许固定白名单变量（question / context / history），
// 采用简单替换，不执行模板中的代码、表达式、文件访问或网络请求（§8.1）。
package prompt

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Whitelist 是允许在模板中使用的固定变量及其来源：
// question=当前用户问题，context=本次授权检索得到的证据，history=会话内最近成功问答。
var Whitelist = []string{"question", "context", "history"}

var varPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// Variables 返回模板中引用的变量（按出现顺序去重）。
func Variables(template string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range varPattern.FindAllStringSubmatch(template, -1) {
		name := m[1]
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// Validate 校验模板：不存在未闭合占位符，且变量全部在白名单内。
// 对未知变量报错（§8.1）；模板为空或未引用变量不在此处阻止，
// 是否必需由应用配置层决定。
func Validate(template string) error {
	for _, m := range varPattern.FindAllStringSubmatch(template, -1) {
		if !isWhitelisted(m[1]) {
			return fmt.Errorf("模板使用了未知变量 %q，仅允许: %s", m[1], strings.Join(Whitelist, ", "))
		}
	}
	remaining := varPattern.ReplaceAllString(template, "")
	if strings.Contains(remaining, "{{") {
		return errors.New("模板语法错误：存在未闭合或格式非法的 {{ 占位符")
	}
	return nil
}

// Inputs 是渲染输入；仅白名单键会被注入，额外键一律忽略。
type Inputs map[string]string

// Render 渲染模板：先校验语法与白名单，再检查缺失变量（明确报错，
// 不静默留空），最后把全部占位符替换为输入值。
func Render(template string, inputs Inputs) (string, error) {
	if err := Validate(template); err != nil {
		return "", err
	}
	missing := []string{}
	for _, name := range Variables(template) {
		if _, ok := inputs[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("缺少变量: %s", strings.Join(missing, ", "))
	}
	return varPattern.ReplaceAllStringFunc(template, func(m string) string {
		return inputs[varPattern.FindStringSubmatch(m)[1]]
	}), nil
}

func isWhitelisted(name string) bool {
	for _, v := range Whitelist {
		if v == name {
			return true
		}
	}
	return false
}
