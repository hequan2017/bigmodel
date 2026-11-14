package exam

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// 选项与题干的取值约束。
const (
	maxStemRunes      = 2000
	maxOptionText     = 500
	maxLevelCodeRunes = 50
	maxTags           = 10
	maxTagRunes       = 50
	optionKeys        = "ABCDEF"
)

// normalizeAndValidate 校验题目数据：
// 单选题 2-6 个选项（键 A-F 且唯一），答案必须是选项键之一；
// 判断题答案只能是 true/false 且不携带选项。
func normalizeAndValidate(in QuestionInput) (QuestionInput, error) {
	in.Type = strings.TrimSpace(strings.ToLower(in.Type))
	if in.Type != TypeSingle && in.Type != TypeJudge {
		return in, fmt.Errorf("%w: type 仅支持 single/judge", ErrInvalidData)
	}

	in.Stem = strings.TrimSpace(in.Stem)
	if in.Stem == "" {
		return in, fmt.Errorf("%w: stem 不能为空", ErrInvalidData)
	}
	if len([]rune(in.Stem)) > maxStemRunes {
		return in, fmt.Errorf("%w: stem 过长", ErrInvalidData)
	}
	if in.DefaultScore < 0 {
		return in, fmt.Errorf("%w: default_score 必须为非负整数", ErrInvalidData)
	}

	in.CorrectAnswer = strings.TrimSpace(in.CorrectAnswer)
	in.LevelCode = strings.TrimSpace(in.LevelCode)
	if len([]rune(in.LevelCode)) > maxLevelCodeRunes {
		return in, fmt.Errorf("%w: level_code 过长", ErrInvalidData)
	}
	if len(in.Tags) > maxTags {
		return in, fmt.Errorf("%w: tags 最多 %d 个", ErrInvalidData, maxTags)
	}
	for i, tag := range in.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || len([]rune(tag)) > maxTagRunes {
			return in, fmt.Errorf("%w: 第 %d 个标签非法", ErrInvalidData, i+1)
		}
		in.Tags[i] = tag
	}

	switch in.Type {
	case TypeSingle:
		if len(in.Options) < 2 || len(in.Options) > len(optionKeys) {
			return in, fmt.Errorf("%w: 单选题需要 2-%d 个选项", ErrInvalidData, len(optionKeys))
		}
		seen := map[string]bool{}
		for i := range in.Options {
			key := strings.TrimSpace(strings.ToUpper(in.Options[i].Key))
			if len(key) != 1 || !strings.Contains(optionKeys, key) {
				return in, fmt.Errorf("%w: 选项键必须为 %s", ErrInvalidData, optionKeys)
			}
			if seen[key] {
				return in, fmt.Errorf("%w: 选项键 %s 重复", ErrInvalidData, key)
			}
			seen[key] = true
			in.Options[i].Key = key
			in.Options[i].Text = strings.TrimSpace(in.Options[i].Text)
			if in.Options[i].Text == "" || utf8.RuneCountInString(in.Options[i].Text) > maxOptionText {
				return in, fmt.Errorf("%w: 选项 %s 内容非法", ErrInvalidData, key)
			}
		}
		in.CorrectAnswer = strings.ToUpper(in.CorrectAnswer)
		if !seen[in.CorrectAnswer] {
			return in, fmt.Errorf("%w: correct_answer 必须是选项键之一", ErrInvalidData)
		}
	case TypeJudge:
		in.CorrectAnswer = strings.ToLower(in.CorrectAnswer)
		if in.CorrectAnswer != "true" && in.CorrectAnswer != "false" {
			return in, fmt.Errorf("%w: 判断题 correct_answer 只能为 true/false", ErrInvalidData)
		}
		in.Options = nil
	}
	return in, nil
}
