package kb

import (
	"fmt"
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
	// 片段以自身标题行开头时，Heading 归属该标题（章节归属修正）。
	if c.Heading != "# 标题" {
		t.Errorf("片段应以自带标题为所属章节: %q", c.Heading)
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

func TestSplitTextTableNotSplitAcrossChunks(t *testing.T) {
	// 表格整体可容纳于一个片段时不得被拆散（对应评测发现的错误码表格拆散问题）：
	// 前置章节把表格推过常规窗口后，扩展窗口应把切点回退到表格之前的空行。
	var b strings.Builder
	for i := 0; i < 18; i++ {
		fmt.Fprintf(&b, "前置章节内容第%d行，用于把表格推入切分窗口边界。这是一段较长的铺垫文字。\n", i)
	}
	b.WriteString("\n## 数据表\n\n")
	for r := 0; r < 12; r++ {
		fmt.Fprintf(&b, "| 键%d | 值%d的说明文字，补充表格行长度 | 备注%d |\n", r, r, r)
	}
	chunks := SplitText(b.String(), 600, 100)
	if len(chunks) < 2 {
		t.Fatalf("应切出多片段, got %d", len(chunks))
	}
	for k := 1; k < len(chunks); k++ {
		prevLines := strings.Split(strings.TrimRight(chunks[k-1].Content, "\n"), "\n")
		curLines := strings.Split(strings.TrimLeft(chunks[k].Content, "\n"), "\n")
		if len(prevLines) == 0 || len(curLines) == 0 {
			continue
		}
		prevLast := strings.TrimSpace(prevLines[len(prevLines)-1])
		curFirst := strings.TrimSpace(curLines[0])
		if strings.HasPrefix(prevLast, "|") && strings.HasPrefix(curFirst, "|") {
			t.Errorf("表格行被切分拆散：片段 %d 尾行 %q 与片段 %d 首行 %q",
				k-1, prevLast, k, curFirst)
		}
	}
}

func TestSplitTextHeadingNeverTailsChunk(t *testing.T) {
	// 段落边界落在标题行之后时，标题不得成为上一片段的尾行。
	var b strings.Builder
	for i := 0; i < 14; i++ {
		fmt.Fprintf(&b, "正文段落第%d行，持续填充使窗口接近目标长度，便于触发边界切分。\n", i)
	}
	b.WriteString("\n## 新章节标题\n\n新章节的正文内容从这里开始，需要足够长以构成独立片段。\n")
	chunks := SplitText(b.String(), 600, 100)
	for k, c := range chunks {
		lines := strings.Split(strings.TrimRight(c.Content, "\n"), "\n")
		if len(lines) == 0 {
			continue
		}
		last := strings.TrimSpace(lines[len(lines)-1])
		if strings.HasPrefix(last, "#") {
			t.Errorf("片段 %d 以标题行结尾：%q", k, last)
		}
	}
}

func TestSplitTextHeadingLedChunkOwnsHeading(t *testing.T) {
	// 切点落在标题前空行时，新片段以标题开头且 Heading 归属该标题
	// （而非上一个章节），headingBefore 只能看到前一个章节。
	// 标题后接表格行（行间禁切、标题后不可切），使标题前空行成为
	// 窗口内唯一合法切点，场景确定。
	var b strings.Builder
	for i := 0; i < 9; i++ {
		fmt.Fprintf(&b, "甲章节正文第%d行，无空行的连续正文，把窗口推近目标长度。\n", i)
	}
	b.WriteString("\n## 数据表\n\n")
	for r := 0; r < 10; r++ {
		fmt.Fprintf(&b, "| 键%d | 值%d的说明文字 |\n", r, r)
	}
	// 300 字符窗口、零重叠：唯一合法切点应为数据表标题前的空行。
	chunks := SplitText(b.String(), 300, 0)
	found := false
	for _, c := range chunks {
		if strings.HasPrefix(strings.TrimSpace(c.Content), "## 数据表") {
			found = true
			if !strings.Contains(c.Heading, "数据表") {
				t.Errorf("以标题开头的片段应归属该标题, got %q", c.Heading)
			}
		}
	}
	if !found {
		t.Fatal("未找到以数据表标题开头的片段")
	}
}
