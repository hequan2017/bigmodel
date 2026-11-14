package kb

import (
	"strings"
	"sync"
	"unicode"

	"github.com/go-ego/gse"
)

// 全文检索词元上限：tsquery 词元数上限，防止超长查询生成过大的查询树。
const MaxQueryTokens = 64

var (
	segOnce  sync.Once
	segInst  gse.Segmenter
	segReady bool
)

// segmenter 返回全局中文分词器（gse 内嵌词典，纯 Go 无 cgo）。
// 懒加载且只初始化一次；词典加载失败时 segReady=false，
// 降级为按符号切分（segmentTokens 内兜底）。
func segmenter() *gse.Segmenter {
	segOnce.Do(func() {
		seg, err := gse.NewEmbed()
		if err == nil {
			segInst = seg
			segReady = true
		}
	})
	return &segInst
}

// isWordRune 判断字符是否属于词元成分（字母或数字；含各语言字母）。
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// segmentTokens 切分文本为词元列表：分词器可用时按词典分词，
// 否则降级为按非词元字符切分。两个分支都过滤纯符号词元。
func segmentTokens(text string) []string {
	segmenter() // 触发懒加载后再检查可用性
	if segReady {
		raw := segmenter().Cut(text)
		out := make([]string, 0, len(raw))
		for _, tok := range raw {
			tok = strings.TrimSpace(tok)
			if tok == "" || !hasWordRune(tok) {
				continue
			}
			out = append(out, tok)
		}
		return out
	}
	return naiveTokens(text)
}

// hasWordRune 判断词元中是否含字母/数字成分。
func hasWordRune(tok string) bool {
	for _, r := range tok {
		if isWordRune(r) {
			return true
		}
	}
	return false
}

// naiveTokens 是词典不可用时的兜底切分：保留连续字母/数字/汉字串。
func naiveTokens(text string) []string {
	var out []string
	var cur strings.Builder
	for _, r := range text {
		if isWordRune(r) {
			cur.WriteRune(r)
		} else if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// SegmentText 中文分词后以空格连接词元，供 to_tsvector('simple', …)
// 建立全文索引。不去重：词频参与 ts_rank 相关性计算。
func SegmentText(text string) string {
	return strings.Join(segmentTokens(text), " ")
}

// QueryTokens 返回用于构建 tsquery 的词元：去重并限制数量上限。
func QueryTokens(text string) []string {
	seen := make(map[string]struct{}, 16)
	out := make([]string, 0, 8)
	for _, tok := range segmentTokens(text) {
		if _, ok := seen[tok]; ok {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
		if len(out) >= MaxQueryTokens {
			break
		}
	}
	return out
}
