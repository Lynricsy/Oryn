package realtime_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts"
)

// 实时画布经 Hub：两个观看者各提交一批操作，双方都收到两条 artifact.canvas（seq 递增、带提交方）；
// 观看者全部离开时落成一个用户版本。
func TestArtifactCanvasLive(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	svc, err := artifacts.New(artifacts.Config{DataDir: t.TempDir()}, e.st, e.hub)
	require.NoError(t, err)
	svc.AttachCanvasHub(e.hub)
	doc := `{"format":1,"canvases":[{"id":"c1","name":"首页"}],"pages":[{"id":"p1","canvas":"c1","name":"封面","file":"a.html","x":0,"y":0,"width":1280,"height":800}]}`
	art, err := svc.Create(ctx, artifacts.CreateParams{Title: "画布", Files: []artifacts.FileChange{
		{Path: "canvas.json", Data: strings.NewReader(doc)}, {Path: "a.html", Data: strings.NewReader("<p>a</p>")},
	}})
	require.NoError(t, err)

	a := e.dial("dev_a")
	b := e.dial("dev_b")
	a.send(map[string]any{"type": "artifact.watch", "id": art.ID, "page": "c1"})
	b.send(map[string]any{"type": "artifact.watch", "id": art.ID})
	require.Eventually(t, func() bool { return len(e.hub.WatchPages(art.ID)) == 2 }, 5*time.Second, 10*time.Millisecond)
	assert.ElementsMatch(t, []string{"c1", ""}, e.hub.WatchPages(art.ID))

	_, err = svc.ApplyCanvas(ctx, art.ID, json.RawMessage(`[{"op":"set","id":"p1","key":"x","value":10}]`), "tab-a", 1)
	require.NoError(t, err)
	_, err = svc.ApplyCanvas(ctx, art.ID, json.RawMessage(`[{"op":"set","id":"p1","key":"y","value":20}]`), "tab-b", 1)
	require.NoError(t, err)

	type canvasMsg struct {
		ID     string           `json:"id"`
		Seq    int64            `json:"seq"`
		Client string           `json:"client"`
		Ops    []map[string]any `json:"ops"`
	}
	for _, c := range []*client{a, b} {
		var got []canvasMsg
		for range 2 {
			m := c.next(func(m msg) bool { return m.Type == "artifact.canvas" })
			var cm canvasMsg
			require.NoError(t, json.Unmarshal(m.raw, &cm))
			got = append(got, cm)
		}
		assert.Equal(t, art.ID, got[0].ID)
		assert.Equal(t, []string{"tab-a", "tab-b"}, []string{got[0].Client, got[1].Client})
		assert.Equal(t, got[0].Seq+1, got[1].Seq)
		assert.Equal(t, "set", got[1].Ops[0]["op"])
	}

	a.send(map[string]any{"type": "artifact.unwatch", "id": art.ID})
	b.send(map[string]any{"type": "artifact.unwatch", "id": art.ID})
	require.Eventually(t, func() bool {
		cur, err := svc.Get(ctx, art.ID)
		return err == nil && cur.Version == 2
	}, 5*time.Second, 10*time.Millisecond)
	st, err := svc.CanvasState(ctx, art.ID)
	require.NoError(t, err)
	assert.Equal(t, 10.0, st.Doc.Pages[0].X)
	assert.Equal(t, 20.0, st.Doc.Pages[0].Y)
}
