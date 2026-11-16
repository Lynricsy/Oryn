// Package htmlsrc 在 HTML 源码上做按字节偏移的扫描：列出起始标签、给起始标签打源码标记、找注入点、
// 取元素的纯文本内容范围。评论锚点、元素直接编辑与调节参数写回都建立在它上面。
//
// 偏移一律是原文件的 UTF-8 字节偏移（前端只回传，不自己算）。扫描用 golang.org/x/net/html 的 Tokenizer，
// 偏移靠逐个累加 len(z.Raw()) 得到（Tokenizer 的 token 在原文里首尾相接）。script / style / textarea 等
// 原始文本元素的内部由 Tokenizer 当作文本，里面的「<div>」字样不会被当成标签。
package htmlsrc

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Tag 是一个起始标签；[Start, End) 是它在原文里的字节范围（从 `<` 到 `>`）。
type Tag struct {
	Name       string
	Start, End int
}

// token 是扫描到的一个 token 及其字节范围。
type token struct {
	typ        html.TokenType
	name       string
	start, end int
}

// scan 逐个回调 src 里的 token；fn 返回 false 时停止。
func scan(src []byte, fn func(t token, z *html.Tokenizer) bool) {
	z := html.NewTokenizer(bytes.NewReader(src))
	off := 0
	for {
		typ := z.Next()
		if typ == html.ErrorToken {
			return
		}
		n := len(z.Raw())
		t := token{typ: typ, start: off, end: off + n}
		off += n
		if typ == html.StartTagToken || typ == html.SelfClosingTagToken || typ == html.EndTagToken {
			name, _ := z.TagName()
			t.name = string(name)
		}
		if !fn(t, z) {
			return
		}
	}
}

func isStart(typ html.TokenType) bool {
	return typ == html.StartTagToken || typ == html.SelfClosingTagToken
}

// StartTags 返回 src 里的全部起始标签（含自闭合写法），按出现顺序。注释、doctype 与 script / style /
// textarea 等原始文本元素的内部被跳过。
func StartTags(src []byte) []Tag {
	var out []Tag
	scan(src, func(t token, _ *html.Tokenizer) bool {
		if isStart(t.typ) {
			out = append(out, Tag{Name: t.name, Start: t.start, End: t.end})
		}
		return true
	})
	return out
}

// MarkSources 返回给每个起始标签加了 `data-oryn-src="<mark>:<Start>"` 的副本（属性紧跟在标签名之后，
// 所以页面自己写的同名属性被 HTML 解析器忽略）。Start 是该标签在 src 里的偏移。
func MarkSources(src []byte, mark string) []byte {
	tags := StartTags(src)
	if len(tags) == 0 {
		return bytes.Clone(src)
	}
	out := make([]byte, 0, len(src)+len(tags)*(len(mark)+24))
	prev := 0
	for _, t := range tags {
		// 起始标签的原文是 `<` + 标签名（字节长度与小写后的 Name 相同）+ 其余
		at := t.Start + 1 + len(t.Name)
		out = append(out, src[prev:at]...)
		out = append(out, ` data-oryn-src="`...)
		out = append(out, mark...)
		out = append(out, ':')
		out = strconv.AppendInt(out, int64(t.Start), 10)
		out = append(out, '"')
		prev = at
	}
	return append(out, src[prev:]...)
}

// InsertionPoint 返回注入脚本的位置：越过 BOM、空白、注释、doctype、`<html>` 与 `<head>` 起始标签之后的
// 第一个位置。没有这些前导内容时为 0（BOM 之后）。
func InsertionPoint(src []byte) int {
	pos := 0
	scan(src, func(t token, z *html.Tokenizer) bool {
		switch t.typ {
		case html.CommentToken, html.DoctypeToken:
		case html.TextToken:
			text := src[t.start:t.end]
			if t.start == 0 {
				text = bytes.TrimPrefix(text, []byte("\uFEFF"))
			}
			if len(bytes.TrimSpace(text)) != 0 {
				return false
			}
		case html.StartTagToken:
			if t.name != "html" && t.name != "head" {
				return false
			}
		default:
			return false
		}
		pos = t.end
		return true
	})
	if pos == 0 && bytes.HasPrefix(src, []byte("\uFEFF")) {
		return 3
	}
	return pos
}

// StartTagAt 返回恰好从 offset 开始的起始标签。
func StartTagAt(src []byte, offset int) (Tag, bool) {
	var out Tag
	found := false
	if offset < 0 || offset >= len(src) || src[offset] != '<' {
		return out, false
	}
	scan(src, func(t token, _ *html.Tokenizer) bool {
		if t.start > offset {
			return false
		}
		if t.start == offset && isStart(t.typ) {
			out, found = Tag{Name: t.name, Start: t.start, End: t.end}, true
			return false
		}
		return true
	})
	return out, found
}

// TextContentRange 返回元素 t 的内容范围 [start, end)：只有起始标签之后直到同名结束标签之间全是文本时
// ok 为真（空内容也算）。有子元素、注释、自闭合写法或缺结束标签时 ok 为假。
func TextContentRange(src []byte, t Tag) (start, end int, ok bool) {
	if t.End > len(src) || t.Start < 0 || t.Start >= t.End {
		return 0, 0, false
	}
	if bytes.HasSuffix(src[t.Start:t.End], []byte("/>")) {
		return 0, 0, false
	}
	// 从起始标签开头重新扫描，Tokenizer 才能按标签名进入原始文本状态（script / style / textarea / title）
	sub := src[t.Start:]
	first := true
	scan(sub, func(tk token, _ *html.Tokenizer) bool {
		if first {
			first = false
			return isStart(tk.typ) && tk.name == t.Name && tk.end == t.End-t.Start
		}
		switch {
		case tk.typ == html.TextToken:
			return true
		case tk.typ == html.EndTagToken && tk.name == t.Name:
			start, end, ok = t.End, t.Start+tk.start, true
		}
		return false
	})
	return start, end, ok
}

// PlainText 返回 src[from:from+maxBytes] 里的可见文本：去掉标签（以及 script / style 的内容）、解码实体、
// 把连续空白折叠成一个空格并去掉首尾空白。窗口截断在标签或多字节字符中间时丢掉残缺的部分。
func PlainText(src []byte, from, maxBytes int) string {
	if from < 0 || from >= len(src) || maxBytes <= 0 {
		return ""
	}
	end := min(len(src), from+maxBytes)
	win := src[from:end]
	if end < len(src) {
		if lt := bytes.LastIndexByte(win, '<'); lt >= 0 && lt > bytes.LastIndexByte(win, '>') {
			win = win[:lt]
		}
		// 去掉末尾残缺的多字节字符
		for i := len(win) - 1; i >= 0 && i >= len(win)-utf8.UTFMax; i-- {
			if utf8.RuneStart(win[i]) {
				if !utf8.FullRune(win[i:]) {
					win = win[:i]
				}
				break
			}
		}
	}
	var b strings.Builder
	skip := ""
	scan(win, func(t token, z *html.Tokenizer) bool {
		switch t.typ {
		case html.StartTagToken:
			if t.name == "script" || t.name == "style" {
				skip = t.name
			}
		case html.EndTagToken:
			if t.name == skip {
				skip = ""
			}
		case html.TextToken:
			if skip == "" {
				b.Write(z.Text())
				b.WriteByte(' ')
			}
		}
		return true
	})
	return strings.Join(strings.Fields(b.String()), " ")
}
