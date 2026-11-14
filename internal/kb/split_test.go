package kb

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitTextShortSingleChunk(t *testing.T) {
	text := "# 标题\n短内容。"
	chunks := SplitText(text, 600, 100)
	if len(chunks) != 1 {
		t.Fatalf("短文本应只有 1 片段, got %d", len(chunks))
	}
	c := chunks[0]
	if c.Index != 0 || c.Content != text {
		t.Errorf("片段内容/序号错误: %+v", c)
	}
	if c.StartOffset != 0 || c.EndOffset != len([]rune(text)) {
		t.Errorf("offset 错误: [%d,%d)", c.StartOffset, c.EndOffset)
	}
	if c.Heading != "" {
		t.Errorf("首片段不应有前置标题: %q", c.Heading)
	}
}

func TestSplitTextParagraphBoundariesWithOverlap(t *testing.T) {
	para := strings.Repeat("这是正文段落内容，用于撑长文本。\n\n", 90) // ~16 字符 × 90 ≈ 1440 runes
	chunks := SplitText(para, 600, 100)
	if len(chunks) < 2 {
		t.Fatalf("长文本应产生多个片段, got %d", len(chunks))
	}
	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("片段序号不连续: %d", c.Index)
		}
		if !utf8.ValidString(c.Content) {
			t.Errorf("片段内容不是合法 UTF-8")
		}
		if len([]rune(c.Content)) > 600 {
			t.Errorf("片段超长: %d", len([]rune(c.Content)))
		}
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i].StartOffset >= chunks[i-1].EndOffset {
			t.Errorf("相邻片段无重叠: chunk%d start=%d chunk%d end=%d",
				i, chunks[i].StartOffset, i-1, chunks[i-1].EndOffset)
		}
		if chunks[i].StartOffset <= chunks[i-1].StartOffset {
			t.Errorf("片段起点未前进")
		}
	}
}

func TestSplitTextHardCutWithoutParagraphs(t *testing.T) {
	text := strings.Repeat("字", 1500) // 无任何换行的中文长文本
	chunks := SplitText(text, 600, 100)
	if len(chunks) != 3 {
		t.Fatalf("应硬切为 3 片段, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c.Content)) > 600 {
			t.Errorf("片段 %d 超长", i)
		}
		if !utf8.ValidString(c.Content) {
			t.Errorf("中文按字节截断产生非法 UTF-8")
		}
	}
}

func TestSplitTextHeadingTracked(t *testing.T) {
	var b strings.Builder
	b.WriteString("# 第一章\n\n")
	b.WriteString(strings.Repeat("第一章内容。", 120)) // ~720 runes，跨切分点
	b.WriteString("\n\n# 第二章\n\n")
	b.WriteString(strings.Repeat("第二章内容。", 120))
	chunks := SplitText(b.String(), 600, 100)
	if len(chunks) < 3 {
		t.Fatalf("应产生多片段, got %d", len(chunks))
	}
	foundFirst, foundSecond := false, false
	for _, c := range chunks {
		switch c.Heading {
		case "# 第一章":
			foundFirst = true
		case "# 第二章":
			foundSecond = true
		}
	}
	if !foundFirst || !foundSecond {
		t.Errorf("标题追踪缺失: first=%v second=%v", foundFirst, foundSecond)
	}
}

func TestSplitTextGuardsInvalidParams(t *testing.T) {
	text := strings.Repeat("内容", 500)
	c1 := SplitText(text, 0, 100)
	c2 := SplitText(text, 600, 600) // overlap >= size
	if len(c1) == 0 || len(c2) == 0 {
		t.Fatal("非法参数应回退默认值并正常切分")
	}
	if len(SplitText("", 600, 100)) != 0 {
		t.Error("空文本应返回空片段")
	}
	if len(SplitText("   \n\n  ", 600, 100)) != 0 {
		t.Error("全空白文本应返回空片段")
	}
}

func TestSplitTextOffsetsMapToOriginal(t *testing.T) {
	text := "第一段内容。\n\n第二段内容继续，中文字符较多一些。\n\n第三段收尾。"
	chunks := SplitText(text, 10, 3)
	for _, c := range chunks {
		rs := []rune(text)
		if string(rs[c.StartOffset:c.EndOffset]) != c.Content {
			t.Errorf("offset 无法还原片段: [%d,%d) %q vs %q",
				c.StartOffset, c.EndOffset, string(rs[c.StartOffset:c.EndOffset]), c.Content)
		}
	}
}
