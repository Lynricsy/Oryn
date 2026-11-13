// Package glyph 处理界面用的单个字形（项目图标、agent 头像图标）与 omp 标题卡片
// （`<图标> <代码>: <标题>`，见 oh-my-pi coding-agent/src/utils/title-card.ts）。
package glyph

import (
	"regexp"
	"unicode"
	"unicode/utf8"
)

// Single 报告 s 是否恰为一个字形（一个字素簇）且不超过 maxBytes 字节：
// 首个码点不是空白或控制字符；其后只允许组合附加码点（变体选择符、肤色修饰、组合记号、标签序列、
// 键帽）、ZWJ 连接的下一个码点，以及与首个区域指示符配对的第二个区域指示符（国旗）。
// 这是覆盖 emoji 与 Nerd Font 私有区字符的近似判定，不是完整的 UAX #29 实现。
func Single(s string, maxBytes int) bool {
	if s == "" || len(s) > maxBytes || !utf8.ValidString(s) {
		return false
	}
	first, n := utf8.DecodeRuneInString(s)
	if unicode.IsSpace(first) || unicode.IsControl(first) || isExtender(first) || first == zwj {
		return false
	}
	afterZWJ, riPaired := false, !isRegional(first)
	for _, r := range s[n:] {
		switch {
		case unicode.IsSpace(r) || unicode.IsControl(r):
			return false
		case afterZWJ:
			afterZWJ = false
		case r == zwj:
			afterZWJ = true
		case isExtender(r):
		case isRegional(r) && !riPaired:
			riPaired = true
		default:
			return false
		}
	}
	return !afterZWJ
}

const zwj = '\u200D'

func isExtender(r rune) bool {
	return (r >= 0xFE00 && r <= 0xFE0F) || // 变体选择符
		(r >= 0x1F3FB && r <= 0x1F3FF) || // 肤色修饰
		(r >= 0xE0020 && r <= 0xE007F) || // 标签序列（地区旗帜）
		r == 0x20E3 || // 键帽
		unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc)
}

func isRegional(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// cardLine 与 omp 的 CARD_LINE 相同：`^(\S+) ([A-Z0-9]{1,6}): (\S.*)$`。
var cardLine = regexp.MustCompile(`^(\S+) ([A-Z0-9]{1,6}): (\S.*)$`)

// MaxIconBytes 是项目图标与卡片图标的字节上限。
const MaxIconBytes = 16

// TitleCard 是解析出的标题卡片。
type TitleCard struct {
	Icon, Code, Text string
}

// ParseTitleCard 解析 omp 卡片格式的标题；图标须是单个非 ASCII 字形（omp 只把非 ASCII 字符当图标）。
func ParseTitleCard(title string) (TitleCard, bool) {
	m := cardLine.FindStringSubmatch(title)
	if m == nil || !Single(m[1], MaxIconBytes) {
		return TitleCard{}, false
	}
	for i := range len(m[1]) {
		if m[1][i] < utf8.RuneSelf {
			return TitleCard{}, false
		}
	}
	return TitleCard{Icon: m[1], Code: m[2], Text: m[3]}, true
}
