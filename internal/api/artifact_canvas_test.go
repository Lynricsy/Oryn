package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
)

// 实时画布 REST：状态、提交操作（seq 递增、非法批次 400）、非画布 400、未知 404，最后落版本仍走服务。
func TestArtifactCanvasREST(t *testing.T) {
	e, svc := newArtifactsEnv(t)
	ctx := t.Context()
	doc := `{"format":1,"canvases":[{"id":"c1","name":"首页"}],"pages":[{"id":"p1","canvas":"c1","name":"封面","file":"a.html","x":0,"y":0,"width":1280,"height":800}]}`
	a, err := svc.Create(ctx, artifacts.CreateParams{Title: "画布", Files: artFiles("canvas.json", doc, "a.html", "<p>a</p>")})
	require.NoError(t, err)
	base := "/api/v1/artifacts/" + a.ID + "/canvas"

	code, body := e.do("GET", base, nil)
	require.Equal(t, 200, code, string(body))
	st := decodeAs[apigen.ArtifactCanvasState](t, body)
	assert.Equal(t, 1, st.Version)
	assert.EqualValues(t, 0, st.Seq)
	assert.Len(t, st.Doc["pages"], 1)

	note := map[string]any{"op": "add", "object": map[string]any{"type": "note", "id": "n1", "kind": "sticky", "x": 1, "y": 2, "text": "改这里", "by": "thr_x"}}
	code, body = e.do("POST", base+"/ops", map[string]any{"ops": []any{note}, "client": "tab-a", "clientSeq": 1})
	require.Equal(t, 200, code, string(body))
	assert.EqualValues(t, 1, decodeAs[apigen.ArtifactCanvasSeq](t, body).Seq)
	move := map[string]any{"op": "set", "id": "p1", "key": "x", "value": 64}
	code, body = e.do("POST", base+"/ops", map[string]any{"ops": []any{move}, "client": "tab-b", "clientSeq": 1})
	require.Equal(t, 200, code, string(body))
	assert.EqualValues(t, 2, decodeAs[apigen.ArtifactCanvasSeq](t, body).Seq)

	code, body = e.do("GET", base, nil)
	require.Equal(t, 200, code, string(body))
	st = decodeAs[apigen.ArtifactCanvasState](t, body)
	assert.Equal(t, 1, st.Version, "静默之前不落版本")
	assert.EqualValues(t, 2, st.Seq)
	notes := st.Doc["notes"].([]any)
	require.Len(t, notes, 1)
	assert.Nil(t, notes[0].(map[string]any)["by"], "用户的标注 by 为 null")
	assert.Equal(t, 64.0, st.Doc["pages"].([]any)[0].(map[string]any)["x"])

	// 非法批次、缺 client、缺 ops → 400；不是画布 → 400；不存在 → 404
	code, _ = e.do("POST", base+"/ops", map[string]any{"ops": []any{map[string]any{"op": "set", "id": "p1", "key": "width", "value": 1}}, "client": "tab-a", "clientSeq": 2})
	assert.Equal(t, 400, code)
	code, _ = e.do("POST", base+"/ops", map[string]any{"ops": []any{move}, "client": "", "clientSeq": 2})
	assert.Equal(t, 400, code)
	code, _ = e.do("POST", base+"/ops", map[string]any{"client": "tab-a", "clientSeq": 2})
	assert.Equal(t, 400, code)
	w, err := svc.Create(ctx, artifacts.CreateParams{Title: "页面", Files: artFiles("index.html", "<p>x</p>")})
	require.NoError(t, err)
	code, _ = e.do("GET", "/api/v1/artifacts/"+w.ID+"/canvas", nil)
	assert.Equal(t, 400, code)
	code, _ = e.do("GET", "/api/v1/artifacts/art_00000000000000000000000000/canvas", nil)
	assert.Equal(t, 404, code)

	// 关停时落成用户版本
	svc.FlushCanvases()
	code, body = e.do("GET", base, nil)
	require.Equal(t, 200, code, string(body))
	assert.Equal(t, 2, decodeAs[apigen.ArtifactCanvasState](t, body).Version)
}
