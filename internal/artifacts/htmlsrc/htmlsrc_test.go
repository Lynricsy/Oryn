package htmlsrc

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const doc = "<!doctype html>\n<html lang=\"zh\">\n<head>\n<!-- 注释 <p> -->\n<title>标题</title>\n" +
	"<script>if (a < b) { document.write(\"<div>假的</div>\") }</script>\n" +
	"<style>p > span { color: red }</style>\n</head>\n<body>\n" +
	"<h1 class=x>中文标题</h1>\n<P>一段 <b>粗体</b> &amp; 实体</P>\n<img src=\"a.png\"/>\n<textarea><div>不是标签</div></textarea>\n</body>\n</html>\n"

var srcRe = regexp.MustCompile(`data-oryn-src="mk:(\d+)"`)

func TestMarkSourcesOffsets(t *testing.T) {
	src := []byte(doc)
	tags := StartTags(src)
	names := make([]string, 0, len(tags))
	for _, tg := range tags {
		names = append(names, tg.Name)
		assert.Equal(t, byte('<'), src[tg.Start], tg.Name)
		assert.Equal(t, byte('>'), src[tg.End-1], tg.Name)
	}
	// script / style / textarea 里的 <div> 与注释里的 <p> 不是标签
	assert.Equal(t, []string{"html", "head", "title", "script", "style", "body", "h1", "p", "b", "img", "textarea"}, names)

	out := MarkSources(src, "mk")
	ms := srcRe.FindAllSubmatch(out, -1)
	require.Len(t, ms, len(tags))
	for i, m := range ms {
		off, _ := strconv.Atoi(string(m[1]))
		assert.Equal(t, tags[i].Start, off)
	}
	// 偏移按字节计：h1 前面有中文
	h1 := bytes.Index(src, []byte("<h1"))
	assert.Contains(t, string(out), fmt.Sprintf(`<h1 data-oryn-src="mk:%d" class=x>`, h1))
	assert.Greater(t, h1, len([]rune(doc[:h1])), "前面有多字节字符，字节偏移大于字符数")
	assert.Contains(t, string(out), `<P data-oryn-src="mk:`)
	assert.Contains(t, string(out), `document.write("<div>假的</div>")`, "脚本内容原样保留")
	// 去掉标记后与原文逐字节相同
	assert.Equal(t, doc, regexp.MustCompile(` data-oryn-src="mk:\d+"`).ReplaceAllString(string(out), ""))
}

func TestInsertionPoint(t *testing.T) {
	cases := map[string]string{
		"<!doctype html><html><head><meta charset=utf-8>": "<meta",
		"\uFEFF<!-- c -->\n<html>\n<body>x":               "<body",
		"<h1>hi</h1>":                                     "<h1",
		"\uFEFFhello":                                     "hello",
		"<html><head>\n  <title>t</title>":                "<title",
	}
	for src, want := range cases {
		assert.Equal(t, bytes.Index([]byte(src), []byte(want)), InsertionPoint([]byte(src)), src)
	}
	assert.Equal(t, 0, InsertionPoint(nil))
}

func TestStartTagAtAndText(t *testing.T) {
	src := []byte(doc)
	h1 := bytes.Index(src, []byte("<h1"))
	tg, ok := StartTagAt(src, h1)
	require.True(t, ok)
	assert.Equal(t, "h1", tg.Name)
	s, e, ok := TextContentRange(src, tg)
	require.True(t, ok)
	assert.Equal(t, "中文标题", string(src[s:e]))

	p := bytes.Index(src, []byte("<P>"))
	tg, ok = StartTagAt(src, p)
	require.True(t, ok)
	_, _, ok = TextContentRange(src, tg)
	assert.False(t, ok, "有子元素")

	img := bytes.Index(src, []byte("<img"))
	tg, ok = StartTagAt(src, img)
	require.True(t, ok)
	_, _, ok = TextContentRange(src, tg)
	assert.False(t, ok, "自闭合")

	_, ok = StartTagAt(src, h1+1)
	assert.False(t, ok)
	_, ok = StartTagAt(src, bytes.Index(src, []byte("<div>假的")))
	assert.False(t, ok, "脚本里的字样")
	_, ok = StartTagAt(src, bytes.Index(src, []byte("</h1>")))
	assert.False(t, ok, "结束标签")

	empty := []byte("<p></p>")
	tg, _ = StartTagAt(empty, 0)
	s, e, ok = TextContentRange(empty, tg)
	assert.True(t, ok)
	assert.Equal(t, s, e)
}

func TestPlainText(t *testing.T) {
	src := []byte(doc)
	body := bytes.Index(src, []byte("<body"))
	assert.Equal(t, "中文标题 一段 粗体 & 实体 <div>不是标签</div>", PlainText(src, body, len(src)))
	head := bytes.Index(src, []byte("<head"))
	assert.Equal(t, "标题", PlainText(src, head, body-head), "script / style 的内容不算")
	// 窗口截在多字节字符中间
	h1 := bytes.Index(src, []byte("<h1"))
	got := PlainText(src, h1, len("<h1 class=x>中文")+1)
	assert.Equal(t, "中文", got)
	assert.Equal(t, "", PlainText(src, len(src), 10))
}

func TestFindAnchor(t *testing.T) {
	src := []byte("<h2>概览</h2><p>一</p><h2>细节  说明</h2><p>二</p><h2>概览</h2>")
	first := bytes.Index(src, []byte("<h2>概览"))
	second := bytes.Index(src, []byte("<h2>细节"))
	third := bytes.LastIndex(src, []byte("<h2>概览"))

	// 文本前缀匹配（空白折叠），同名多个时取离旧偏移最近的
	off, ok := FindAnchor(src, "h2", "细节 说明", 0)
	require.True(t, ok)
	assert.Equal(t, second, off)
	off, ok = FindAnchor(src, "h2", "概览", third-3)
	require.True(t, ok)
	assert.Equal(t, third, off)
	off, ok = FindAnchor(src, "h2", "概览", 0)
	require.True(t, ok)
	assert.Equal(t, first, off)
	// 标签名不同或文本不在都找不到
	_, ok = FindAnchor(src, "h3", "概览", 0)
	assert.False(t, ok)
	_, ok = FindAnchor(src, "h2", "没有的", 0)
	assert.False(t, ok)
	// 文本为空：只认旧偏移处的同名标签
	off, ok = FindAnchor(src, "p", "", bytes.Index(src, []byte("<p>")))
	require.True(t, ok)
	assert.Equal(t, bytes.Index(src, []byte("<p>")), off)
	_, ok = FindAnchor(src, "p", "", second)
	assert.False(t, ok)
}
