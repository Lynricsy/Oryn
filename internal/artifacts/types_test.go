package artifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 合法的图片文件头（后面跟几个字节，够 sniffBytes 读）。
const (
	pngBytes  = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	jpegBytes = "\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00"
	gifBytes  = "GIF89a\x01\x00\x01\x00\x80\x00\x00"
)

func createTyped(t *testing.T, e *env, typ string, files ...FileChange) (Artifact, error) {
	t.Helper()
	return e.s.Create(context.Background(), CreateParams{Title: "t", Type: typ, Files: files})
}

func TestTypeDocument(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	md := "# 周报\n\n![图](images/a.png)\n![远程](https://example.com/x.png)\n\n```md\n![不算](images/nope.png)\n```\n"
	a, err := createTyped(t, e, TypeDocument, put("document.md", md), put("images/a.png", pngBytes), put("images/b.JPG", jpegBytes))
	require.NoError(t, err)
	assert.Equal(t, KindDocument, a.Kind)
	assert.Equal(t, TypeDocument, a.Type)
	assert.Equal(t, 1, a.Pages)
	v, err := e.s.Version(ctx, a.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, v.Format)

	// 只改 document.md：没改的图片从内容存储读文件头
	b, err := e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("document.md", "# 改了\n\n![图](./images/a.png)\n")}})
	require.NoError(t, err)
	assert.Equal(t, 2, b.Version)

	for name, c := range map[string]struct {
		files []FileChange
		want  []string
	}{
		"missing image line": {[]FileChange{put("document.md", "# x\n\n![图](images/nope.png)\n")}, []string{"document.md:3: 图片 images/nope.png 不在这个版本里"}},
		"png is jpeg":        {[]FileChange{put("document.md", "# x"), put("images/c.png", jpegBytes)}, []string{"images/c.png: 扩展名是 .png，但文件内容是 JPEG 图片"}},
		"not an image":       {[]FileChange{put("document.md", "# x"), put("images/c.gif", "hello world, not a gif")}, []string{"images/c.gif: 扩展名是 .gif，但文件内容不是 GIF 图片"}},
		"svg rejected":       {[]FileChange{put("document.md", "# x"), put("images/c.svg", "<svg/>")}, []string{"images/c.svg"}},
		"outside images":     {[]FileChange{put("document.md", "# x"), put("index.html", "<p>")}, []string{"index.html"}},
		"no entry":           {[]FileChange{put("images/a.png", pngBytes)}, []string{"document.md: 缺少入口文件"}},
		"not utf8":           {[]FileChange{put("document.md", "\xff\xfe")}, []string{"document.md: 不是合法的 UTF-8"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := createTyped(t, e, TypeDocument, c.files...)
			require.Equal(t, CodeInvalid, code(err), "%v", err)
			for _, w := range c.want {
				assert.Contains(t, err.Error(), w)
			}
			assert.Contains(t, err.Error(), "read_artifact_guide 的 document 主题")
		})
	}
	_, err = createTyped(t, e, TypeDocument, put("document.md", "# x"), put("images/g.gif", gifBytes))
	require.NoError(t, err)
}

// 一份覆盖八种版式的合法幻灯片。
func validDeck() map[string]any {
	return map[string]any{
		"title": "季度回顾", "theme": "dark", "aspect": "4:3",
		"slides": []any{
			map[string]any{"layout": "title", "title": "封面", "subtitle": "副标题", "notes": "开场"},
			map[string]any{"layout": "section", "title": "第一部分"},
			map[string]any{"layout": "bullets", "title": "要点", "bullets": []any{"一", map[string]any{"text": "二", "bullets": []any{"二.一"}}}, "image": "images/a.png", "alt": "图"},
			map[string]any{"layout": "two-column", "left": map[string]any{"heading": "左"}, "right": map[string]any{"text": "右", "bullets": []any{"x"}}},
			map[string]any{"layout": "image", "image": "images/a.png", "alt": "图", "caption": "说明"},
			map[string]any{"layout": "quote", "quote": "引言", "attribution": "某人"},
			map[string]any{"layout": "stats", "stats": []any{map[string]any{"value": "42%", "label": "留存"}}},
			map[string]any{"layout": "table", "columns": []any{"a", "b"}, "rows": []any{[]any{"1", "2"}}},
		},
	}
}

func deckJSON(t *testing.T, d any) string {
	t.Helper()
	b, err := json.Marshal(d)
	require.NoError(t, err)
	return string(b)
}

func TestDeckValid(t *testing.T) {
	e := newEnv(t)
	a, err := createTyped(t, e, TypeSlides, put("slides.json", deckJSON(t, validDeck())), put("images/a.png", pngBytes))
	require.NoError(t, err)
	assert.Equal(t, KindSlides, a.Kind)
	assert.Equal(t, 8, a.Pages)
}

func TestDeckProblems(t *testing.T) {
	e := newEnv(t)
	slide := func(s map[string]any) string {
		return deckJSON(t, map[string]any{"slides": []any{s}})
	}
	long := strings.Repeat("字", MaxSlideTitleRunes+1)
	many := make([]any, MaxBullets+1)
	for i := range many {
		many[i] = "x"
	}
	cases := map[string]struct{ deck, want string }{
		"missing title":    {slide(map[string]any{"layout": "title"}), "slides[0].title: 版式 title 缺少必填字段 title"},
		"empty title":      {slide(map[string]any{"layout": "title", "title": " "}), "slides[0].title: 不能为空"},
		"long title":       {slide(map[string]any{"layout": "section", "title": long}), "slides[0].title: 有 201 个字符，上限是 200"},
		"unknown field":    {slide(map[string]any{"layout": "quote", "quote": "q", "color": "red"}), "slides[0].color: 版式 quote 没有这个字段"},
		"unknown layout":   {slide(map[string]any{"layout": "hero"}), "slides[0].layout: 未知的版式"},
		"no layout":        {slide(map[string]any{"title": "x"}), "slides[0].layout: 缺少必填字段 layout"},
		"top unknown":      {`{"slides":[{"layout":"section","title":"x"}],"bogus":1}`, "bogus: 未知字段"},
		"bad theme":        {`{"theme":"blue","slides":[{"layout":"section","title":"x"}]}`, "theme: 只能是"},
		"bad aspect":       {`{"aspect":"1:1","slides":[{"layout":"section","title":"x"}]}`, "aspect: 只能是"},
		"no slides":        {`{"title":"x"}`, "slides: 缺少必填字段 slides"},
		"empty slides":     {`{"slides":[]}`, "slides: 至少要有 1 项"},
		"too many bullets": {slide(map[string]any{"layout": "bullets", "title": "t", "bullets": many}), "slides[0].bullets: 有 13 项，上限是 12"},
		"nested too deep":  {slide(map[string]any{"layout": "bullets", "title": "t", "bullets": []any{map[string]any{"text": "a", "bullets": []any{map[string]any{"text": "b"}}}}}), "slides[0].bullets[0].bullets[0]: 应该是字符串"},
		"missing image":    {slide(map[string]any{"layout": "image", "image": "images/nope.png", "alt": "x"}), "slides[0].image: 图片 images/nope.png 不在这个版本里"},
		"stat number":      {slide(map[string]any{"layout": "stats", "stats": []any{map[string]any{"value": 42}}}), "slides[0].stats[0].value: 应该是字符串"},
		"row width":        {slide(map[string]any{"layout": "table", "columns": []any{"a", "b"}, "rows": []any{[]any{"1"}}}), "slides[0].rows[0]: 有 1 格，columns 有 2 列"},
		"empty column":     {slide(map[string]any{"layout": "two-column", "left": map[string]any{}, "right": map[string]any{"text": "x"}}), "slides[0].left: 至少要有"},
		"not json":         {`{"slides":`, "slides.json: 不是合法的 JSON"},
		"trailing":         {`{"slides":[{"layout":"section","title":"x"}]} {}`, "slides.json: JSON 后面还有多余的内容"},
		"not object":       {`[]`, "slides.json: 应该是一个对象"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := createTyped(t, e, TypeSlides, put("slides.json", c.deck))
			require.Equal(t, CodeInvalid, code(err), "%v", err)
			assert.Contains(t, err.Error(), c.want)
		})
	}

	// 问题最多列 20 条
	var slides []any
	for range 30 {
		slides = append(slides, map[string]any{"layout": "title"})
	}
	_, err := createTyped(t, e, TypeSlides, put("slides.json", deckJSON(t, map[string]any{"slides": slides})))
	require.Equal(t, CodeInvalid, code(err))
	assert.Equal(t, maxProblems, strings.Count(err.Error(), "\n- "))
	assert.Contains(t, err.Error(), fmt.Sprintf("只列出了前 %d 条", maxProblems))
}

func TestTypeRestoreKeepsShape(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := createTyped(t, e, TypeSlides, put("slides.json", deckJSON(t, validDeck())), put("images/a.png", pngBytes))
	require.NoError(t, err)
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("slides.json", `{"slides":[{"layout":"section","title":"x"}]}`)}})
	require.NoError(t, err)
	r, err := e.s.RestoreVersion(ctx, a.ID, 1, 2, Author{})
	require.NoError(t, err)
	assert.Equal(t, KindSlides, r.Kind)
	assert.Equal(t, 8, r.Pages)
	v, err := e.s.Version(ctx, a.ID, 3)
	require.NoError(t, err)
	assert.Equal(t, 1, v.Format)
	assert.NotEqual(t, filesKey("", 0, manifest{"a": {sha: "x"}}), filesKey(TypeSlides, 1, manifest{"a": {sha: "x"}}))
}

// 类型化 artifact 的 agent 操作：create 带 type；html / htmlSource 一律被拒；edits 缺省改入口文件。
func TestAgentTyped(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	c.Fetch = fakeFetch{"/tmp/oryn-artifact-1/0": "<h1>html</h1>"}.fetch

	_, err := agentCall(t, e.s, c, OpCreate, `{"title":"文档","type":"document","html":"<p>x</p>"}`)
	require.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), "文档 / 幻灯片 artifact 不接受 html")
	_, err = agentCall(t, e.s, c, OpCreate, `{"title":"文档","type":"slides","htmlSource":"/tmp/oryn-artifact-1/0"}`)
	require.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), "slides.json")

	r := mustCall(t, e.s, c, OpCreate, `{"title":"文档","type":"document","files":[{"path":"document.md","content":"# 标题\n\n正文一\n"}]}`)
	assert.Equal(t, KindDocument, r.Details.(obj)["kind"])
	id := textObj(t, r)["id"].(string)

	h, body := header(t, mustCall(t, e.s, c, OpRead, `{"id":"`+id+`"}`))
	assert.Equal(t, "document.md", h["path"])
	assert.Equal(t, "document", h["type"])
	assert.EqualValues(t, 1, h["format"])
	assert.Equal(t, "# 标题\n\n正文一\n", body)

	for _, p := range []string{`"html":"<p>x</p>"`, `"htmlSource":"/tmp/oryn-artifact-1/0"`} {
		_, err = agentCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":1,`+p+`}`)
		require.Equal(t, CodeInvalid, code(err), p)
		assert.Contains(t, err.Error(), "不接受 html", p)
	}

	mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":1,"edits":[{"oldText":"正文一","newText":"正文二"}]}`)
	assert.Equal(t, "# 标题\n\n正文二\n", readFile(t, e.s, id, 0, "document.md"))

	// 不合法的类型化内容：update 报出位置，不落版本
	_, err = agentCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":2,"edits":[{"oldText":"正文二","newText":"![x](images/none.png)"}]}`)
	require.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), "document.md:3")

	g := mustCall(t, e.s, c, OpReadGuide, `{"topic":"slides"}`)
	assert.Contains(t, g.Text, "two-column")
	g = mustCall(t, e.s, c, OpReadGuide, `{"topic":"document"}`)
	assert.Contains(t, g.Text, "document.md")
}

// htmlSource 是扩展把 html 参数写成的临时文件：对普通页面等价于 html。
func TestAgentHTMLSource(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	c.Fetch = fakeFetch{"/tmp/oryn-artifact-1/0": "<h1>html</h1>"}.fetch
	id := textObj(t, mustCall(t, e.s, c, OpCreate, `{"title":"页","htmlSource":"/tmp/oryn-artifact-1/0"}`))["id"].(string)
	assert.Equal(t, "<h1>html</h1>", readFile(t, e.s, id, 0, "index.html"))

	_, err := agentCall(t, e.s, c, OpCreate, `{"title":"页","html":"x","htmlSource":"/tmp/oryn-artifact-1/0"}`)
	assert.Equal(t, CodeInvalid, code(err))
	// 没有 Fetch 的调用方（全局助手）不接受 htmlSource
	_, err = agentCall(t, e.s, threadCaller("thr_a"), OpCreate, `{"title":"页","htmlSource":"/tmp/oryn-artifact-1/0"}`)
	assert.Equal(t, CodeInvalid, code(err))
}
