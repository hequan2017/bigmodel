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

	maxHeadingRunes = 200
)

// segmentKind 是结构段的类型。
type segmentKind int

const (
	segPara    segmentKind = iota // 普通段落（可自由切分）
	segHeading                    // Markdown 标题行（章节锚）
	segTable                      // Markdown 表格（连续表格行，倾向整体保留）
)

// blockSeg 是按行分类合并出的结构段：连续同类非空行为一段，
// 空行分隔，标题行自成一段。heading 为该段所属章节标题
// （标题段为其自身文本）。
type blockSeg struct {
	start, end int
	kind       segmentKind
	heading    string
}

// SplitText 按结构块聚合切分：先把文本按行分类合并为结构段
// （标题锚 / 表格 / 段落），再把连续段聚合到 targetSize 个 Unicode 字符
// 的片段；聚合不跨段拆开——表格与标题整体保留，仅在单个段超过窗口时
// 内部按行边界滑动切分（带 overlap）。offset 为原文 rune 下标。
func SplitText(text string, targetSize, overlap int) []Chunk {
	if targetSize <= 0 {
		targetSize = DefaultChunkSize
	}
	if overlap < 0 || overlap >= targetSize {
		overlap = targetSize / 6
	}

	runes := []rune(text)
	segs := splitSegments(runes)

	chunks := []Chunk{}
	emit := func(start, end int, heading string) {
		for start < end && isSpace(runes[start]) {
			start++
		}
		for end > start && isSpace(runes[end-1]) {
			end--
		}
		if start >= end {
			return
		}
		chunks = append(chunks, Chunk{
			Index:       len(chunks),
			Content:     string(runes[start:end]),
			StartOffset: start,
			EndOffset:   end,
			Heading:     heading,
		})
	}

	// 聚合：连续段并入当前片段，直到加入下一段会超出窗口。
	var aggStart, aggEnd int
	aggHeading := ""
	aggHas := false
	flush := func() {
		if aggHas {
			emit(aggStart, aggEnd, aggHeading)
			aggHas = false
		}
	}
	for _, seg := range segs {
		size := seg.end - seg.start
		if aggHas && aggEnd-aggStart+size > targetSize {
			flush()
		}
		if size > targetSize {
			// 超长段：内部按行边界滑动切分（表格行间在此退化为可切，
			// 这是"必须拆"的情形），先冲刷已聚合内容。
			flush()
			pos := seg.start
			for pos < seg.end {
				end := pos + targetSize
				if end > seg.end {
					end = seg.end
				}
				cut := end
				if seg.end-pos > targetSize {
					cut = simpleCut(runes, pos, targetSize, seg.end)
				}
				emit(pos, cut, seg.heading)
				if cut >= seg.end {
					break
				}
				next := cut - overlap
				if next <= pos {
					next = cut
				}
				pos = next
			}
			continue
		}
		if !aggHas {
			aggStart, aggEnd, aggHeading = seg.start, seg.end, seg.heading
			aggHas = true
			continue
		}
		aggEnd = seg.end
	}
	flush()
	return chunks
}

// splitSegments 按行分类合并结构段。状态：
// 空行分隔段落与表格；标题行自成一段并更新当前章节标题；
// 表格行聚段，普通行打断表格段。
func splitSegments(runes []rune) []blockSeg {
	segs := []blockSeg{}
	n := len(runes)
	curHeading := ""

	i := 0
	for i < n {
		// 跳过空行（整行空白，含行首空格的空白行）。
		if isSpace(runes[i]) && onlySpaceInLine(runes, i) {
			i = skipLine(runes, lineEnd(runes, i))
			continue
		}
		ls := i
		le := lineEnd(runes, i)
		line := runes[ls:le]

		switch {
		case isHeadingLine(line):
			text := string(trimRunes(line))
			if len(text) > maxHeadingRunes {
				text = text[:maxHeadingRunes]
			}
			segs = append(segs, blockSeg{ls, le, segHeading, text})
			curHeading = text
			i = skipLine(runes, le)
		case isTableRow(line):
			// 聚合连续表格行。
			start := ls
			end := le
			i = skipLine(runes, le)
			for i < n {
				nls := i
				nle := lineEnd(runes, i)
				nline := runes[nls:nle]
				if onlySpaceInLine(runes, i) || !isTableRow(nline) {
					break
				}
				end = nle
				i = skipLine(runes, nle)
			}
			segs = append(segs, blockSeg{start, end, segTable, curHeading})
		default:
			// 普通段落：连续非空、非标题、非表格行。
			start := ls
			end := le
			i = skipLine(runes, le)
			for i < n {
				nls := i
				nle := lineEnd(runes, i)
				nline := runes[nls:nle]
				if onlySpaceInLine(runes, i) || isHeadingLine(nline) || isTableRow(nline) {
					break
				}
				end = nle
				i = skipLine(runes, nle)
			}
			segs = append(segs, blockSeg{start, end, segPara, curHeading})
		}
	}
	return segs
}

// simpleCut 在 (pos+targetSize*2/3, pos+targetSize] 内从后向前寻找
// 空行（段落）边界，其次行边界，否则硬切——用于超长结构段内部切分。
func simpleCut(runes []rune, pos, targetSize, limit int) int {
	end := pos + targetSize
	if end > limit {
		end = limit
	}
	minPos := pos + targetSize*2/3
	if minPos < pos+1 {
		minPos = pos + 1
	}
	for i := end; i > minPos; i-- {
		if i < limit && runes[i-1] == '\n' && runes[i] == '\n' {
			return i
		}
	}
	for i := end; i > minPos; i-- {
		if runes[i-1] == '\n' {
			return i
		}
	}
	return end
}

// lineEnd 返回从 i 开始的行的换行符下标（行内容为 [i, le)）。
func lineEnd(runes []rune, i int) int {
	le := i
	for le < len(runes) && runes[le] != '\n' {
		le++
	}
	return le
}

// skipLine 返回下一行起点（越过换行符；文末返回 len）。
func skipLine(runes []rune, le int) int {
	if le < len(runes) && runes[le] == '\n' {
		return le + 1
	}
	return le
}

// onlySpaceInLine 判断从 i 到行尾是否全为空白。
func onlySpaceInLine(runes []rune, i int) bool {
	for k := i; k < len(runes) && runes[k] != '\n'; k++ {
		if !isSpace(runes[k]) {
			return false
		}
	}
	return true
}

// isHeadingLine 判断一行是否为 Markdown 标题（去除前导空白后以 # 开头）。
func isHeadingLine(line []rune) bool {
	t := trimRunes(line)
	return len(t) > 0 && t[0] == '#'
}

// isTableRow 判断一行是否为 Markdown 表格行（以 | 开头或为 ---| 分隔行）。
func isTableRow(line []rune) bool {
	t := trimRunes(line)
	if len(t) == 0 {
		return false
	}
	if t[0] == '|' {
		return true
	}
	if len(t) >= 3 && string(t[:3]) == "---" {
		for _, r := range t {
			if r == '|' {
				return true
			}
		}
	}
	return false
}

// headingBefore 保留供引用定位等场景使用：返回 start 之前最近的
// Markdown 标题行文本；没有则返回空串。
func headingBefore(runes []rune, start int) string {
	floor := start - 5000
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
		end := lineEnd(runes, k)
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
