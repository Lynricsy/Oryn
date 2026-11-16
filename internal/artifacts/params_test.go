package artifacts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/render"
)

// paramVectors 是 schema/testdata/artifact-params.json（TS 的 params.test.ts 读同一份）。
type paramVectors struct {
	Decls []struct {
		Name string              `json:"name"`
		Decl json.RawMessage     `json:"decl"`
		Want []any               `json:"want"`
		CSS  map[string]ParamCSS `json:"css"`
	} `json:"decls"`
	Values []struct {
		Name   string          `json:"name"`
		Param  json.RawMessage `json:"param"`
		Value  any             `json:"value"`
		Repeat *struct {
			Text  string `json:"text"`
			Count int    `json:"count"`
		} `json:"repeat"`
		Want     any  `json:"want"`
		WantSame bool `json:"wantSame"`
		Reject   bool `json:"reject"`
	} `json:"values"`
}

func loadParamVectors(t *testing.T) paramVectors {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "schema", "testdata", "artifact-params.json"))
	require.NoError(t, err)
	var v paramVectors
	require.NoError(t, json.Unmarshal(b, &v))
	require.NotEmpty(t, v.Decls)
	require.NotEmpty(t, v.Values)
	return v
}

// 与 TS 同一组正反例：声明的规整、值的校验与 CSS 形式两边逐条相同。
func TestParamsVectors(t *testing.T) {
	v := loadParamVectors(t)
	for _, c := range v.Decls {
		got := ParseParams(c.Decl)
		b, err := json.Marshal(got)
		require.NoError(t, err)
		var gotAny []any
		require.NoError(t, json.Unmarshal(b, &gotAny))
		if len(c.Want) == 0 {
			assert.Empty(t, gotAny, c.Name)
		} else {
			assert.Equal(t, c.Want, gotAny, c.Name)
		}
		if c.CSS != nil {
			css := map[string]ParamCSS{}
			for _, p := range got {
				css[p.Name] = CSSOf(p, p.Value)
			}
			assert.Equal(t, c.CSS, css, c.Name)
		}
	}
	for _, c := range v.Values {
		ps := ParseParams([]byte(`{"p":` + string(c.Param) + `}`))
		require.Len(t, ps, 1, c.Name)
		value := c.Value
		if c.Repeat != nil {
			value = strings.Repeat(c.Repeat.Text, c.Repeat.Count)
		}
		got, ok := CoerceParamValue(ps[0], value)
		if c.Reject {
			assert.False(t, ok, c.Name)
			continue
		}
		require.True(t, ok, c.Name)
		want := c.Want
		if c.WantSame {
			want = value
		}
		assert.Equal(t, want, got, c.Name)
	}
}

func TestParamsLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("{")
	for i := range MaxParams + 2 {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"p` + string(rune('a'+i)) + `":{"type":"boolean","value":true}`)
	}
	b.WriteString("}")
	ps := ParseParams([]byte(b.String()))
	require.Len(t, ps, MaxParams)
	assert.Equal(t, "pa", ps[0].Name)
}

const paramsPage = "<!doctype html>\n<html><head>\n<title>卡片</title>\n" +
	"<script type=\"application/json\" data-oryn-params>\n" +
	"{ \"accent\": {\"label\": \"强调色\", \"type\": \"color\", \"value\": \"#2563eb\"},\n" +
	"  \"density\": {\"type\": \"number\", \"value\": 16, \"min\": 8, \"max\": 32, \"unit\": \"px\"},\n" +
	"  \"title\": {\"type\": \"text\", \"value\": \"欢迎\"},\n" +
	"  \"layout\": {\"type\": \"choice\", \"value\": \"grid\", \"options\": [\"grid\", \"list\"]},\n" +
	"  \"broken\": {\"type\": \"color\", \"value\": \"red\"} }\n" +
	"</script>\n<style>h1{color:var(--accent)}</style></head>\n<body><h1>标题</h1></body></html>\n"

func TestParamsSet(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.s.Create(ctx, CreateParams{Title: "页面", Files: []FileChange{put("index.html", paramsPage)}})
	require.NoError(t, err)

	// 越界的数字夹回范围；值里的 </script> 被转义；文件其余部分逐字节不变
	got, err := e.s.SetParams(ctx, a.ID, 1, "index.html", map[string]any{
		"density": 99.0,
		"title":   "</script><script>alert(1)</script><!--",
		"accent":  "#ff0000",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, got.Version)
	v2 := readFile(t, e.s, a.ID, 2, "index.html")
	open := `<script type="application/json" data-oryn-params>`
	cs := strings.Index(paramsPage, open) + len(open)
	ce := strings.Index(paramsPage, "</script>")
	assert.Equal(t, paramsPage[:cs], v2[:cs], "声明之前逐字节不变")
	tail := paramsPage[ce:]
	assert.True(t, strings.HasSuffix(v2, tail), "声明之后逐字节不变")
	decl := v2[cs : len(v2)-len(tail)]
	want := "\n{\n" +
		"  \"accent\": {\n    \"label\": \"强调色\",\n    \"type\": \"color\",\n    \"value\": \"#ff0000\"\n  },\n" +
		"  \"density\": {\n    \"type\": \"number\",\n    \"value\": 32,\n    \"min\": 8,\n    \"max\": 32,\n    \"unit\": \"px\"\n  },\n" +
		"  \"title\": {\n    \"type\": \"text\",\n    \"value\": \"<\\/script><script>alert(1)<\\/script>\\u003c!--\"\n  },\n" +
		"  \"layout\": {\n    \"type\": \"choice\",\n    \"value\": \"grid\",\n    \"options\": [\n      \"grid\",\n      \"list\"\n    ]\n  },\n" +
		"  \"broken\": {\n    \"type\": \"color\",\n    \"value\": \"red\"\n  }\n}\n"
	assert.Equal(t, want, decl)
	assert.Equal(t, 1, strings.Count(v2, "</script>"), "声明里没有能结束 <script> 的字样")
	ps := PageParams([]byte(v2))
	require.Len(t, ps, 4)
	assert.Equal(t, "</script><script>alert(1)</script><!--", ps[2].Value, "转义后读回的值不变")
	assert.Equal(t, 32.0, ps[1].Value)
	vs, err := e.s.Versions(ctx, a.ID)
	require.NoError(t, err)
	assert.Empty(t, vs[0].AuthorThreadID, "作者是用户")

	// 不在 options 里的值、未声明 / 不合规的参数、非法颜色、空改动、非 HTML 文件都被拒，不落版本
	bad := []struct {
		path   string
		values map[string]any
	}{
		{"index.html", map[string]any{"layout": "cards"}},
		{"index.html", map[string]any{"missing": true}},
		{"index.html", map[string]any{"broken": "#000"}},
		{"index.html", map[string]any{"accent": "red"}},
		{"index.html", map[string]any{"density": "16"}},
		{"index.html", map[string]any{}},
		{"a.css", map[string]any{"accent": "#000"}},
	}
	for i, b := range bad {
		_, err = e.s.SetParams(ctx, a.ID, 2, b.path, b.values)
		assert.Equal(t, CodeInvalid, code(err), "第 %d 条", i)
	}
	cur, err := e.s.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, cur.Version, "失败的写回不落版本")

	// 文件在 baseVersion 之后被改过：走 Write 的并发规则回 conflict
	_, err = e.s.SetParams(ctx, a.ID, 1, "index.html", map[string]any{"accent": "#00ff00"})
	assert.Equal(t, CodeConflict, code(err))

	// 没有声明的页面
	plain, err := e.s.Create(ctx, CreateParams{Title: "无参数", Files: []FileChange{put("index.html", page)}})
	require.NoError(t, err)
	_, err = e.s.SetParams(ctx, plain.ID, 1, "index.html", map[string]any{"accent": "#000"})
	assert.Equal(t, CodeInvalid, code(err))

	// 文件已不在：conflict 带 state
	_, err = e.s.SetParams(ctx, a.ID, 2, "gone.html", map[string]any{"accent": "#000"})
	require.Equal(t, CodeConflict, code(err))

	// 回收站里不能写
	_, err = e.s.Delete(ctx, a.ID)
	require.NoError(t, err)
	_, err = e.s.SetParams(ctx, a.ID, 2, "index.html", map[string]any{"accent": "#000"})
	assert.Equal(t, CodeDeleted, code(err))
}

// 写回时定义里没有 value 的项追加一个，重复的键同 JSON.parse（第一次的位置、最后一次的值）。
func TestParamsSetKeepsOrder(t *testing.T) {
	src := []byte(`<script data-oryn-params type="Application/JSON">{"a":{"type":"boolean","value":false},"a":{"value":true,"type":"boolean","label":"开"}}</script>`)
	out, err := setParams(src, "x.html", map[string]any{"a": false})
	require.NoError(t, err)
	assert.Equal(t, "<script data-oryn-params type=\"Application/JSON\">{\n  \"a\": {\n    \"value\": false,\n    \"type\": \"boolean\",\n    \"label\": \"开\"\n  }\n}</script>", string(out))
	// type 不是 application/json 的不算声明
	_, err = setParams([]byte(`<script data-oryn-params>{"a":{"type":"boolean","value":false}}</script>`), "x.html", map[string]any{"a": true})
	assert.Equal(t, CodeInvalid, code(err))
}

var preludeRe = regexp.MustCompile(`<script data-oryn-params-prelude>\(function\(r,v,a\)\{[^<]*\}\)\(document\.documentElement,(\[\[.*?\]\]),(\[\[.*?\]\])\);</script>`)

// assertPrelude 断言 body 里恰有一段设参数的内联脚本、值与运行时的转换相同、`</script>` 被转义。
func assertPrelude(t *testing.T, body string) {
	t.Helper()
	m := preludeRe.FindStringSubmatch(body)
	require.NotNil(t, m, body)
	assert.Equal(t, 1, strings.Count(body, "data-oryn-params-prelude"))
	var vars, attrs [][2]string
	require.NoError(t, json.Unmarshal([]byte(m[1]), &vars))
	require.NoError(t, json.Unmarshal([]byte(m[2]), &attrs))
	assert.Equal(t, [][2]string{
		{"--accent", "#2563eb"}, {"--density", "16px"}, {"--title", `"</script>欢迎"`}, {"--layout", "grid"},
	}, vars)
	assert.Equal(t, [][2]string{
		{"data-accent", "#2563eb"}, {"data-density", "16"}, {"data-title", "</script>欢迎"}, {"data-layout", "grid"},
	}, attrs)
	assert.NotContains(t, m[0][:len(m[0])-len("</script>")], "</", "值里的 </script> 截不断脚本")
	assert.Less(t, strings.Index(body, "data-oryn-params-prelude"), strings.Index(body, "<title"), "插在页面自己的内容之前")
}

// 声明里带 </script> 字样的文本（原文写成 <\/script>）。
var preludePage = strings.Replace(paramsPage, `"value": "欢迎"`, `"value": "<\/script>欢迎"`, 1)

// 没有页面运行时的三处：渲染模式、顶层帧、发布页都插入设参数的脚本；没有声明的页面逐字节不变。
func TestParamsPreludeRender(t *testing.T) {
	e := newEnv(t)
	h := newHost(t, e, "localhost", fakeRuntime)
	a := mkPage(t, e, put("index.html", preludePage), put("plain.html", page), put("a.css", "x"))
	serve := func(p string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://"+render.Host+"/tok"+p, nil)
		rec := httptest.NewRecorder()
		h.ServeRender(rec, req, render.Target{ArtifactID: a.ID, Version: 1, Self: "http://" + render.Host + "/tok/"}, p)
		return rec
	}
	rec := serve("/")
	require.Equal(t, http.StatusOK, rec.Code)
	assertPrelude(t, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "__orynStart")
	assert.Equal(t, page, serve("/plain.html").Body.String())
	assert.Equal(t, "x", serve("/a.css").Body.String())
}

func TestParamsPreludeTopLevel(t *testing.T) {
	e := newEnv(t)
	h := newHost(t, e, "localhost", fakeRuntime)
	a := mkPage(t, e, put("index.html", preludePage), put("plain.html", page))
	link, err := h.OpenLink(context.Background(), a.ID, 0, "")
	require.NoError(t, err)
	host := strings.TrimSuffix(strings.TrimPrefix(link, "http://"), "/")
	mw := h.Middleware(http.NotFoundHandler())
	rec := do(mw, hostReq{host: host, path: "/"})
	require.Equal(t, http.StatusOK, rec.Code)
	assertPrelude(t, rec.Body.String())
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "'unsafe-inline'")
	assert.Equal(t, page, do(mw, hostReq{host: host, path: "/plain.html"}).Body.String())

	// 普通帧也插这段（运行时要等 DOMContentLoaded 才读声明，这段让首次绘制就带上参数），且在运行时之后、页面内容之前
	token, _, err := h.OpenFrame(context.Background(), a.ID, 0, "", "")
	require.NoError(t, err)
	rec = do(mw, hostReq{host: "a-" + token + ".localhost:7600", path: "/"})
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assertPrelude(t, body)
	assert.Contains(t, body, "__orynStart")
	assert.Contains(t, body, `data-oryn-src="`, "源码标记照常")
	rec = do(mw, hostReq{host: "a-" + token + ".localhost:7600", path: "/plain.html"})
	assert.NotContains(t, rec.Body.String(), "data-oryn-params-prelude")
}

func TestParamsPreludePublic(t *testing.T) {
	pe := newPubEnv(t, "localhost")
	ctx := context.Background()
	a := mkPage(t, pe.env, put("index.html", preludePage), put("plain.html", page))
	p, err := pe.s.Publish(ctx, a.ID, PublishParams{Access: AccessPublic})
	require.NoError(t, err)
	rec := pubReq(pe, p.Slug, "GET", "/", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assertPrelude(t, body)
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "'unsafe-inline'")
	// noindex 与心跳在前，参数脚本紧随其后
	assert.Less(t, strings.Index(body, "noindex"), strings.Index(body, "data-oryn-beat"))
	assert.Less(t, strings.Index(body, "data-oryn-beat"), strings.Index(body, "data-oryn-params-prelude"))

	rec = pubReq(pe, p.Slug, "GET", "/plain.html", nil)
	assert.NotContains(t, rec.Body.String(), "data-oryn-params-prelude")
	assert.Equal(t, page, stripInjected(rec.Body.String()))
}

func TestParamsPreludeNoDecl(t *testing.T) {
	src := []byte(page)
	assert.Nil(t, ParamsPrelude(src))
	assert.Equal(t, src, withParamsPrelude(src))
	bad := []byte(`<script type="application/json" data-oryn-params>{not json</script><p>x</p>`)
	assert.Equal(t, bad, withParamsPrelude(bad), "解析失败的声明不插脚本，页面照常")
}
