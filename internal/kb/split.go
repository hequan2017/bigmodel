package kb

import "unicode"

// Chunk 是切分后的文本片段；offset 为原文中的 rune 下标，
// 避免按字节截断 UTF-8，供引用回到对应文本。
type Chunk struct {
	Index       int
	Content     string
	StartOffset int
	EndOffset   int
	Heading     string
}

const (
	// DefaultChunkSize / DefaultChunkOverlap 是便于实现的初始值，
	// 不是效果最优保证；实际值在部署前按评测结果调整。
	DefaultChunkSize    = 600
	DefaultChunkOverlap = 100

	// MaxChunksPerDoc 是单文档片段数上限；超限明确拒绝，不静默截断。
	MaxChunksPerDoc = 500

	maxHeadingScanBack = 5000
	maxHeadingRunes    = 200
)

// SplitText 按固定规则切分文本：目标片段 targetSize 个 Unicode 字符、
// 相邻片段重叠 overlap，优先在段落（空行）边界切分，其次行边界，最后硬切。
// 切点受结构规则约束：标题行不得留在片段尾部、Markdown 表格行之间不切
// （表格行与所属章节标题保持同片）。全空白片段被跳过；
// 内容为去除首尾空白后的原文切片。
func SplitText(text string, targetSize, overlap int) []Chunk {
	if targetSize <= 0 {
		targetSize = DefaultChunkSize
	}
	if overlap < 0 || overlap >= targetSize {
		overlap = targetSize / 6
	}

	runes := []rune(text)
	n := len(runes)
	chunks := []Chunk{}

	appendChunk := func(start, end int) {
		for start < end && isSpace(runes[start]) {
			start++
		}
		for end > start && isSpace(runes[end-1]) {
			end--
		}
		if start >= end {
			return
		}
		heading := headingBefore(runes, start)
		// 片段以标题行开头时，该标题即本片段所属章节，优于此前的章节标题。
		if ls, le, ok := lineAt(runes, start); ok && isHeadingLine(runes[ls:le]) {
			heading = string(trimRunes(runes[ls:le]))
		}
		chunks = append(chunks, Chunk{
			Index:       len(chunks),
			Content:     string(runes[start:end]),
			StartOffset: start,
			EndOffset:   end,
			Heading:     heading,
		})
	}

	pos := 0
	for pos < n {
		cut := n
		if n-pos > targetSize {
			cut = findCut(runes, pos, targetSize)
			if cut <= pos {
				cut = pos + 1 // 安全兜底，保证窗口前进
			}
		}
		appendChunk(pos, cut)
		if cut >= n {
			break
		}
		next := cut - overlap
		if next <= pos {
			next = cut // 无法保持重叠时直接从切点继续，保证前进且不重复
		}
		pos = next
	}
	return chunks
}

// findCut 在 (pos+targetSize*2/3, pos+targetSize] 内从后向前寻找切割点：
// 优先空行（段落）边界，其次行边界；窗口内无可满足结构规则的切点时
// （如表格内部），向前扩展扫描窗口（下限 targetSize/3），优先把表格等
// 结构块整体保留到下一片段；仍无果则硬切行边界（不截断行内）。
func findCut(runes []rune, pos, targetSize int) int {
	end := pos + targetSize
	if end > len(runes) {
		end = len(runes)
	}
	minPos := pos + targetSize*2/3
	if minPos < 1 {
		minPos = 1
	}
	minPos2 := pos + targetSize/3
	if minPos2 < 1 {
		minPos2 = 1
	}

	// 常规窗口：空行优先，其次行边界。
	for i := end; i > minPos; i-- {
		if i < len(runes) && runes[i-1] == '\n' && runes[i] == '\n' && canCutAt(runes, i) {
			return i
		}
	}
	for i := end; i > minPos; i-- {
		if runes[i-1] == '\n' && canCutAt(runes, i) {
			return i
		}
	}
	// 扩展窗口：同序，接受更短的片段以避免拆散结构块。
	for i := end; i > minPos2; i-- {
		if i < len(runes) && runes[i-1] == '\n' && runes[i] == '\n' && canCutAt(runes, i) {
			return i
		}
	}
	for i := end; i > minPos2; i-- {
		if runes[i-1] == '\n' && canCutAt(runes, i) {
			return i
		}
	}
	// 硬切兜底：行边界优先（不截断行内），最后原样 end。
	for i := end; i > minPos2; i-- {
		if runes[i-1] == '\n' {
			return i
		}
	}
	return end
}

// canCutAt 判断位置 i（紧跟 '\n' 之后）是否为合法切点：
// 标题行不得留在上一片段尾部；连续表格行之间不切。
// 极长表格超过窗口且无可满足规则的切点时，由硬切兜底。
func canCutAt(runes []rune, i int) bool {
	pls, ple, prevOK := lineBefore(runes, i)
	if prevOK && isHeadingLine(runes[pls:ple]) {
		return false
	}
	nls, nle, nextOK := lineAt(runes, i)
	if !prevOK || !nextOK {
		return true
	}
	if isTableRow(runes[pls:ple]) && isTableRow(runes[nls:nle]) {
		return false
	}
	return true
}

// lineAt 返回以 i 开头的行边界（i 应紧跟换行或为 0）；内容为 [i, le)。
func lineAt(runes []rune, i int) (int, int, bool) {
	if i >= len(runes) {
		return 0, 0, false
	}
	le := i
	for le < len(runes) && runes[le] != '\n' {
		le++
	}
	return i, le, true
}

// lineBefore 返回以 i-1 的 '\n' 结尾的行边界；行内容为 [ls, i-1)。
func lineBefore(runes []rune, i int) (int, int, bool) {
	if i < 1 || runes[i-1] != '\n' {
		return 0, 0, false
	}
	ls := i - 2
	for ls >= 0 && runes[ls] != '\n' {
		ls--
	}
	ls++
	return ls, i - 1, true
}

// isHeadingLine 判断一行是否为 Markdown 标题（去除前导空白后以 # 开头）。
func isHeadingLine(line []rune) bool {
	t := trimRunes(line)
	return len(t) > 0 && t[0] == '#'
}

// isTableRow 判断一行是否为 Markdown 表格行（以 | 开头或为分隔行）。
func isTableRow(line []rune) bool {
	t := trimRunes(line)
	if len(t) == 0 {
		return false
	}
	if t[0] == '|' {
		return true
	}
	// 表头分隔行 |---|---| 的变体（首列为空）：含 --- 且含 |。
	if len(t) >= 3 && string(t[:3]) == "---" {
		for _, r := range t {
			if r == '|' {
				return true
			}
		}
	}
	return false
}

// headingBefore 返回 start 之前最近的 Markdown 标题行文本；没有则返回空串。
func headingBefore(runes []rune, start int) string {
	floor := start - maxHeadingScanBack
	if floor < 0 {
		floor = 0
	}
	for k := start - 1; k >= floor; k-- {
		if runes[k] != '#' {
			continue
		}
		if k > 0 && runes[k-1] != '\n' {
			continue // 行中的 # 不是标题
		}
		end := k
		for end < len(runes) && runes[end] != '\n' {
			end++
		}
		line := trimRunes(runes[k:end])
		if len(line) == 0 {
			continue
		}
		if len(line) > maxHeadingRunes {
			line = line[:maxHeadingRunes]
		}
		return string(line)
	}
	return ""
}

func trimRunes(rs []rune) []rune {
	lo, hi := 0, len(rs)
	for lo < hi && isSpace(rs[lo]) {
		lo++
	}
	for hi > lo && isSpace(rs[hi-1]) {
		hi--
	}
	return rs[lo:hi]
}

func isSpace(r rune) bool { return unicode.IsSpace(r) }
