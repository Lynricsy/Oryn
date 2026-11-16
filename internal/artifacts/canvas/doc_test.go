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
