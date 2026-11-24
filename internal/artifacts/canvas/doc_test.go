package canvas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vector 是 schema/testdata/canvas-ops.json 的一条用例（TS 实现读同一份）。
type vector struct {
	Name  string          `json:"name"`
	Doc   json.RawMessage `json:"doc"`
	Ops   json.RawMessage `json:"ops"`
	Want  json.RawMessage `json:"want"`
	Error string          `json:"error"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "schema", "testdata", "canvas-ops.json"))
	require.NoError(t, err)
	var vs []vector
	require.NoError(t, json.Unmarshal(b, &vs))
	require.NotEmpty(t, vs)
	return vs
}

// run 解析文档与操作并应用。
func run(v vector) (Doc, error) {
	d, err := Parse(v.Doc)
	if err != nil {
		return Doc{}, err
	}
	ops, err := ParseOps(v.Ops)
	if err != nil {
		return Doc{}, err
	}
	if err := Apply(&d, ops); err != nil {
		return Doc{}, err
	}
	return d, nil
}

func decode(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal(b, &v))
	return v
}

func TestVectors(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			require.True(t, (v.Error == "") != (v.Want == nil), "用例必须恰好给 want 或 error 之一")
			d, err := run(v)
			if v.Error != "" {
				require.Error(t, err, "期望报错：%s", v.Error)
				var ce *Error
				require.ErrorAs(t, err, &ce)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, decode(t, v.Want), decode(t, d.JSON()))
			// 规范化输出再解析得到同一份文档
			again, err := Parse(d.JSON())
			require.NoError(t, err)
			assert.Equal(t, string(d.JSON()), string(again.JSON()))
		})
	}
}

func TestApplyIsAtomic(t *testing.T) {
	d, err := Parse([]byte(`{"pages":[{"id":"p1","name":"a","file":"a.html"}]}`))
	require.NoError(t, err)
	before := string(d.JSON())
	ops, err := ParseOps(json.RawMessage(`[{"op":"set","id":"p1","key":"name","value":"b"},{"op":"remove","id":"canvas-1"}]`))
	require.NoError(t, err)
	err = Apply(&d, ops)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "canvases")
	assert.Equal(t, before, string(d.JSON()))
}

func TestApplyDoesNotAliasInput(t *testing.T) {
	d, err := Parse([]byte(`{"pages":[{"id":"p1","name":"a","file":"a.html"},{"id":"p2","name":"b","file":"b.html"}],
		"notes":[{"id":"n1","kind":"line","x1":0,"y1":0,"x2":1,"y2":1,"from":"p1"}]}`))
	require.NoError(t, err)
	orig := d
	ops, err := ParseOps(json.RawMessage(`[{"op":"remove","id":"p1"},{"op":"order","id":"p2","index":0}]`))
	require.NoError(t, err)
	require.NoError(t, Apply(&d, ops))
	assert.Len(t, orig.Pages, 2)
	assert.Equal(t, "p1", *orig.Notes[0].From)
	assert.Nil(t, d.Notes[0].From)
}

func TestOpsRoundTrip(t *testing.T) {
	raw := `[{"op":"add","object":{"id":"p2","name":"x","type":"page"},"copyOf":"p1"},` +
		`{"op":"set","id":"n1","key":"from","value":null},{"op":"remove","id":"p1"},{"op":"order","id":"n1","index":3}]`
	ops, err := ParseOps(json.RawMessage(raw))
	require.NoError(t, err)
	assert.Equal(t, "p1", ops[0].CopyOf)
	assert.Equal(t, TypePage, ops[0].ObjectType())
	assert.Equal(t, "p2", ops[0].ObjectID())
	b, err := json.Marshal(ops)
	require.NoError(t, err)
	assert.JSONEq(t, raw, string(b))
}

func TestErrorWhere(t *testing.T) {
	_, err := Parse([]byte(`{"canvases":[{"id":"c1","name":"a"}],"pages":[{"id":"p1","name":"a","file":"a.html"},` +
		`{"id":"p2","name":"b","file":"b.html"},{"id":"p3","canvas":"c9","name":"c","file":"c.html"}]}`))
	var ce *Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "pages[2].canvas", ce.Where)
	assert.Contains(t, err.Error(), `"c9"`)

	_, err = Parse([]byte(`{`))
	require.Error(t, err)
	_, err = Parse(make([]byte, MaxJSONBytes+1))
	require.Error(t, err)
}

func TestMissingPageFiles(t *testing.T) {
	d, err := Parse([]byte(`{"pages":[{"id":"p1","name":"a","file":"a.html"},{"id":"p2","name":"b","file":"b.html"},` +
		`{"id":"p3","name":"c","file":"b.html"},{"id":"p4","name":"d","file":"c.html"}]}`))
	require.NoError(t, err)
	has := func(p string) bool { return p == "a.html" }
	assert.Equal(t, []string{"b.html", "c.html"}, MissingPageFiles(d, has))
	assert.Empty(t, MissingPageFiles(d, func(string) bool { return true }))
}

func TestRound2MatchesJS(t *testing.T) {
	for in, want := range map[float64]float64{1.236: 1.24, -0.004: 0, -1.005: -1, 2.345: 2.35, -2.5: -2.5, 0.125: 0.13} {
		got := round2(in)
		assert.Equal(t, want, got, "round2(%v)", in)
	}
	assert.Equal(t, "0", string(must(json.Marshal(round2(-0.001)))))
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// HTML 画布的规范化输出与改动前逐字节相同：format 1，不出现 design / source / device。
func TestHTMLCanvasStaysFormat1(t *testing.T) {
	in := `{"format":1,"canvases":[{"id":"c1","name":"首页"}],"pages":[{"id":"p1","canvas":"c1","name":"封面","file":"a.html","x":0,"y":0,"width":1280,"height":800,"locked":false}],"notes":[]}`
	d, err := Parse([]byte(in))
	require.NoError(t, err)
	want := "{\n  \"format\": 1,\n  \"canvases\": [\n    {\n      \"id\": \"c1\",\n      \"name\": \"首页\"\n    }\n  ],\n" +
		"  \"pages\": [\n    {\n      \"id\": \"p1\",\n      \"canvas\": \"c1\",\n      \"name\": \"封面\",\n      \"file\": \"a.html\",\n" +
		"      \"x\": 0,\n      \"y\": 0,\n      \"width\": 1280,\n      \"height\": 800,\n      \"locked\": false\n    }\n  ],\n" +
		"  \"notes\": []\n}\n"
	assert.Equal(t, want, string(d.JSON()))
	assert.Equal(t, Format, d.Format)
}

func TestApplySeal(t *testing.T) {
	d, err := Parse([]byte(`{"pages":[]}`))
	require.NoError(t, err)
	ops, err := ParseOps(json.RawMessage(`[{"op":"add","object":{"type":"page","id":"login","name":"登录","source":"src/frames/login.tsx","device":"phone"}},` +
		`{"op":"add","object":{"type":"page","id":"signup","name":"注册","source":"src/frames/signup.tsx"}}]`))
	require.NoError(t, err)

	// 普通 Apply 不能带 source
	plain := d
	err = Apply(&plain, ops)
	var ce *Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "ops[0].object.source", ce.Where)

	require.NoError(t, ApplySeal(&d, ops))
	require.NotNil(t, d.Design)
	assert.Equal(t, Design{Engine: DesignEngine}, *d.Design)
	assert.Equal(t, FormatDesign, d.Format)
	assert.Equal(t, "dist/login.html", d.Pages[0].File)
	assert.Equal(t, DevicePhone, d.Pages[0].Device)
	assert.Equal(t, "dist/signup.html", d.Pages[1].File)

	// 封存可以改 source 与 file（成对改），也保留已有的 design
	start := "signup"
	d.Design.Start = &start
	ops, err = ParseOps(json.RawMessage(`[{"op":"set","id":"signup","key":"source","value":"src/frames/register.tsx"},` +
		`{"op":"set","id":"signup","key":"file","value":"dist/register.html"}]`))
	require.NoError(t, err)
	require.NoError(t, ApplySeal(&d, ops))
	assert.Equal(t, "src/frames/register.tsx", d.Pages[1].Source)
	assert.Equal(t, "dist/register.html", d.Pages[1].File)
	assert.Equal(t, "signup", *d.Design.Start)

	// 只改 source 不改 file：不配套，整批不生效
	before := string(d.JSON())
	ops, err = ParseOps(json.RawMessage(`[{"op":"set","id":"signup","key":"source","value":"src/frames/x.tsx"}]`))
	require.NoError(t, err)
	err = ApplySeal(&d, ops)
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "pages[1].file", ce.Where)
	assert.Equal(t, before, string(d.JSON()))

	// 封存后的文档能被普通解析读回
	again, err := Parse(d.JSON())
	require.NoError(t, err)
	assert.Equal(t, string(d.JSON()), string(again.JSON()))
}

func TestRemoveStartDoesNotAliasInput(t *testing.T) {
	d, err := Parse([]byte(`{"design":{"engine":1,"start":"a"},"pages":[{"id":"a","name":"a","file":"dist/a.html","source":"src/frames/a.tsx"}]}`))
	require.NoError(t, err)
	orig := d
	ops, err := ParseOps(json.RawMessage(`[{"op":"remove","id":"a"}]`))
	require.NoError(t, err)
	require.NoError(t, Apply(&d, ops))
	assert.Nil(t, d.Design.Start)
	assert.Equal(t, "a", *orig.Design.Start)
}
