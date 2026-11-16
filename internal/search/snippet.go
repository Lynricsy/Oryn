package search

import (
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 片段的取景：命中前留 snippetLead 个字符，整段至多 snippetMax 个字符（按 rune 计，空白折叠之后）。
const (
	snippetLead = 32
	snippetMax  = 160
)

// foldRune 把一个字符折叠成比较用的形式：小写，并去掉变音符（é → e）。与 trigram 分词器的
// `case_sensitive 0 remove_diacritics 1` 同一口径，且一个字符恰好折叠成一个字符，下标不必另做映射。
func foldRune(r rune) rune {
	if r < utf8.RuneSelf {
		if 'A' <= r && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}
	if d := norm.NFD.String(string(r)); d != "" {
		base, _ := utf8.DecodeRuneInString(d)
		r = base
	}
	return unicode.ToLower(r)
}

func foldRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = foldRune(r)
	}
	return out
}

// span 是 rune 下标上的 [start, end)。
type span struct{ start, end int }

// matchSpans 返回 hay 里各词全部出现位置合并后的区间（升序、互不重叠）。hay 与 terms 都已折叠。
func matchSpans(hay []rune, terms [][]rune) []span {
	var out []span
	for _, term := range terms {
		if len(term) == 0 {
			continue
		}
		for i := 0; i+len(term) <= len(hay); i++ {
			if hay[i] == term[0] && runesEqual(hay[i:i+len(term)], term) {
				out = append(out, span{i, i + len(term)})
			}
		}
	}
	if len(out) < 2 {
		return out
	}
	// 按起点排序后合并相交 / 相邻的区间
	sortSpans(out)
	merged := out[:1]
	for _, s := range out[1:] {
		last := &merged[len(merged)-1]
		if s.start <= last.end {
			last.end = max(last.end, s.end)
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

func runesEqual(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortSpans(s []span) {
	// 词数与命中数都很小：插入排序
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && (s[j].start < s[j-1].start || s[j].start == s[j-1].start && s[j].end < s[j-1].end); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// buildSnippet 从正文里截出第一个命中附近的一段：空白（含换行）折叠成单个空格，两端被截断时补 `…`；
// highlights 是片段里全部命中的区间，按 UTF-16 码元计（浏览器字符串的下标）。正文里找不到命中时取开头一段、不带高亮。
func buildSnippet(body string, terms [][]rune) (string, []apigen.TextRange) {
	// 先折叠空白：片段与高亮都在折叠后的文本上计算
	src := []rune(collapseSpace(body))
	folded := foldRunes(src)
	first := 0
	if spans := matchSpans(folded, terms); len(spans) > 0 {
		first = spans[0].start
	}
	start := max(0, first-snippetLead)
	end := min(len(src), start+snippetMax)
	// 命中靠近末尾时往前多取，尽量凑满长度
	start = max(0, min(start, end-snippetMax))
	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	b.WriteString(strings.TrimSpace(string(src[start:end])))
	if end < len(src) {
		b.WriteString("…")
	}
	snippet := b.String()
	sr := []rune(snippet)
	spans := matchSpans(foldRunes(sr), terms)
	ranges := make([]apigen.TextRange, 0, len(spans))
	for _, s := range spans {
		ranges = append(ranges, apigen.TextRange{Start: utf16Len(sr[:s.start]), End: utf16Len(sr[:s.end])})
	}
	return snippet, ranges
}

// collapseSpace 把连续空白（含换行、制表）折叠成一个空格，并去掉首尾空白。
func collapseSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

func utf16Len(rs []rune) int {
	n := 0
	for _, r := range rs {
		n += utf16.RuneLen(r)
	}
	return n
}
