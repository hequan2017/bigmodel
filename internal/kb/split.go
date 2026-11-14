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
// 全空白片段被跳过；内容为去除首尾空白后的原文切片。
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
		chunks = append(chunks, Chunk{
			Index:       len(chunks),
			Content:     string(runes[start:end]),
			StartOffset: start,
			EndOffset:   end,
			Heading:     headingBefore(runes, start),
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
// 优先空行（段落）边界，其次行边界，否则硬切。
func findCut(runes []rune, pos, targetSize int) int {
	end := pos + targetSize
	if end > len(runes) {
		end = len(runes)
	}
	minPos := pos + targetSize*2/3
	if minPos < 1 {
		minPos = 1
	}

	for i := end; i > minPos; i-- {
		if i < len(runes) && runes[i-1] == '\n' && runes[i] == '\n' {
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
