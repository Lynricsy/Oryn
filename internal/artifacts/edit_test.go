package artifacts

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 与 web/src/lib/artifacts/editable.test.ts 同一组正反例：两份校验逐条相同。
func TestEditElementStyleRules(t *testing.T) {
	cases := []struct {
		prop, value string
		ok          bool
	}{
		{"color", "#ff0000", true},
		{"color", "#FF000080", true},
		{"color", "#f00", false},
		{"color", "red", false},
		{"background-color", "#00000", false},
		{"font-size", "16px", true},
		{"font-size", "1px", true},
		{"font-size", "0.5px", false},
		{"font-size", "0px", false},
		{"font-size", "12.25px", true},
		{"font-size", "12.255px", false},
		{"font-size", "10000px", false},
		{"font-size", "16", false},
		{"border-radius", "0px", true},
		{"border-radius", "01px", false},
		{"font-weight", "700", true},
		{"font-weight", "1000", false},
		{"font-weight", "bold", false},
		{"text-align", "justify", true},
		{"text-align", "start", false},
		{"padding", "4px", true},
		{"padding", "1px 2px 3px 4.5px", true},
		{"padding", "1px 2px 3px 4px 5px", false},
		{"padding", "1px  2px", false},
		{"padding", " 1px", false},
		{"padding", "", false},
		{"margin", "1px", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.ok, ValidStyleValue(c.prop, c.value), "%s: %q", c.prop, c.value)
	}
	assert.Len(t, EditableProps, 7)
	assert.True(t, ValidEditText(strings.Repeat("😀", MaxEditTextRunes)))
	assert.False(t, ValidEditText(strings.Repeat("a", MaxEditTextRunes+1)))
}

func TestEditElementSetStyle(t *testing.T) {
	cases := []struct {
		name, tag string
		style     map[string]string
		want      string
	}{
		{"没有 style 时紧跟标签名加一个", `<h1 class="t">`, map[string]string{"color": "#ff0000"}, `<h1 style="color: #ff0000" class="t">`},
		{"同名替换，其余保留", `<h1 style="margin: 4px; COLOR: red">`, map[string]string{"color": "#00ff00"}, `<h1 style="margin: 4px; color: #00ff00">`},
		{"同名的后续声明删掉", `<p style='color:red;color:blue;margin:0'>`, map[string]string{"color": "#000000"}, `<p style="color: #000000; margin:0">`},
		{"新属性按固定顺序追加", `<p style="margin:0;">`, map[string]string{"padding": "1px 2px", "font-weight": "700"}, `<p style="margin:0; font-weight: 700; padding: 1px 2px">`},
		{"括号与引号里的分号不拆", `<div style="background: url(&quot;a;b.png&quot;); color: red">`, map[string]string{"color": "#111111"}, `<div style="background: url(&#34;a;b.png&#34;); color: #111111">`},
		{"不带引号的值与自闭合", `<img src=a.png style=color:red />`, map[string]string{"border-radius": "4px"}, `<img src=a.png style="color:red; border-radius: 4px" />`},
		{"data-style 不是 style", `<b data-style="x">`, map[string]string{"color": "#123456"}, `<b style="color: #123456" data-style="x">`},
	}
	for _, c := range cases {
		name := c.tag[1:strings.IndexAny(c.tag, " >")]
		assert.Equal(t, c.want, string(setStyle([]byte(c.tag), len(name), c.style)), c.name)
	}
}

func TestEditElement(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := "<!doctype html>\n<h1 style=\"margin: 8px\">标题</h1>\n<div class=\"card\"><p>说明</p></div>\n<p id=\"x\">旧文字</p>\n"
	a, err := e.s.Create(ctx, CreateParams{Title: "页面", Files: []FileChange{put("index.html", src)}})
	require.NoError(t, err)
	h1 := strings.Index(src, "<h1")

	// 改 color：只有那个起始标签的 style 变了，原有的 margin 还在；落成一个用户版本
	got, err := e.s.EditElement(ctx, a.ID, EditParams{BaseVersion: 1, Path: "index.html", Offset: h1, Tag: "h1", Style: map[string]string{"color": "#ff0000"}})
	require.NoError(t, err)
	assert.Equal(t, 2, got.Version)
	v2 := readFile(t, e.s, a.ID, 2, "index.html")
	assert.Equal(t, strings.Replace(src, `<h1 style="margin: 8px">`, `<h1 style="margin: 8px; color: #ff0000">`, 1), v2)
	vs, err := e.s.Versions(ctx, a.ID)
	require.NoError(t, err)
	assert.Empty(t, vs[0].AuthorThreadID, "作者是用户")

	// 改文字（同时改样式）：文字里的 <script> 被转义
	p := strings.Index(v2, `<p id="x">`)
	text := `新的 <script>alert(1)</script> & 文字`
	_, err = e.s.EditElement(ctx, a.ID, EditParams{BaseVersion: 2, Path: "index.html", Offset: p, Tag: "p", Text: &text, Style: map[string]string{"text-align": "center"}})
	require.NoError(t, err)
	v3 := readFile(t, e.s, a.ID, 3, "index.html")
	assert.Contains(t, v3, `<p style="text-align: center" id="x">新的 &lt;script&gt;alert(1)&lt;/script&gt; &amp; 文字</p>`)
	assert.NotContains(t, v3, "<script>")

	// 含子元素的标签改文字被拒，不落版本
	div := strings.Index(v3, "<div")
	_, err = e.s.EditElement(ctx, a.ID, EditParams{BaseVersion: 3, Path: "index.html", Offset: div, Tag: "div", Text: new("x")})
	assert.Equal(t, CodeInvalid, code(err))

	// 偏移处的标签与 Tag 不符回 conflict，带 state
	_, err = e.s.EditElement(ctx, a.ID, EditParams{BaseVersion: 3, Path: "index.html", Offset: div, Tag: "section", Style: map[string]string{"color": "#000000"}})
	require.Equal(t, CodeConflict, code(err))
	oe, _ := AsOpError(err)
	require.NotNil(t, oe.State)
	assert.Equal(t, 3, oe.State.Version)
	assert.Equal(t, []string{"index.html"}, oe.State.Changed)

	// 文件在 baseVersion 之后被改过：走 Write 的并发规则回 conflict
	_, err = e.s.EditElement(ctx, a.ID, EditParams{BaseVersion: 1, Path: "index.html", Offset: h1, Tag: "h1", Style: map[string]string{"color": "#00ff00"}})
	assert.Equal(t, CodeConflict, code(err))

	// 非法值、未知属性、没有改动、非 HTML 文件、script 的文字
	bad := []EditParams{
		{BaseVersion: 3, Path: "index.html", Offset: h1, Tag: "h1", Style: map[string]string{"color": "red"}},
		{BaseVersion: 3, Path: "index.html", Offset: h1, Tag: "h1", Style: map[string]string{"margin": "1px"}},
		{BaseVersion: 3, Path: "index.html", Offset: h1, Tag: "h1"},
		{BaseVersion: 3, Path: "a.css", Offset: 0, Tag: "h1", Style: map[string]string{"color": "#000000"}},
		{BaseVersion: 3, Path: "index.html", Offset: h1, Tag: "script", Text: new("x")},
		{BaseVersion: 3, Path: "index.html", Offset: h1, Tag: "H1", Style: map[string]string{"color": "#000000"}},
	}
	for i, b := range bad {
		_, err = e.s.EditElement(ctx, a.ID, b)
		assert.Equal(t, CodeInvalid, code(err), "第 %d 条", i)
	}
	cur, err := e.s.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, cur.Version, "失败的编辑不落版本")

	// 回收站里不能编辑
	_, err = e.s.Delete(ctx, a.ID)
	require.NoError(t, err)
	_, err = e.s.EditElement(ctx, a.ID, EditParams{BaseVersion: 3, Path: "index.html", Offset: h1, Tag: "h1", Style: map[string]string{"color": "#000000"}})
	assert.Equal(t, CodeDeleted, code(err))
}
