package kb

import (
	"strings"
	"testing"
)

func TestSegmentTextChinese(t *testing.T) {
	got := SegmentText("什么是检索增强生成？RAG 的基本流程是什么。")
	if got == "" {
		t.Fatal("分词结果为空")
	}
	tokens := strings.Fields(got)
	if len(tokens) < 3 {
		t.Fatalf("分词过少: %q", got)
	}
	joined := " " + got + " "
	// 词典分词应把常见词切出（而非整句粘连）
	if !strings.Contains(joined, " 什么 ") && !strings.Contains(joined, " 检索 ") {
		t.Errorf("常见词未被切出: %q", got)
	}
	// 英文词元保留
	if !strings.Contains(joined, "rag") && !strings.Contains(joined, "RAG") {
		t.Errorf("英文词元丢失: %q", got)
	}
}

func TestSegmentTextFiltersPunctuation(t *testing.T) {
	got := SegmentText("，。！？、；：")
	if got != "" {
		t.Errorf("纯符号应过滤为空, got %q", got)
	}
}

func TestQueryTokensDedupAndCap(t *testing.T) {
	toks := QueryTokens("检索 检索 检索增强 生成。")
	seen := map[string]int{}
	for _, tok := range toks {
		seen[tok]++
	}
	for tok, n := range seen {
		if n > 1 {
			t.Errorf("查询词元应去重, %q 出现 %d 次", tok, n)
		}
	}
	long := strings.Repeat("词语重复", 100)
	if got := len(QueryTokens(long)); got > MaxQueryTokens {
		t.Errorf("查询词元应上限 %d, got %d", MaxQueryTokens, got)
	}
}
