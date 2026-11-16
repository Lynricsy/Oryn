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

// 评论有变化时，观看者收到 artifact.comments {id}；没在观看的连接收不到。
func TestArtifactCommentsWatch(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	svc, err := artifacts.New(artifacts.Config{DataDir: t.TempDir()}, e.st, e.hub)
	require.NoError(t, err)
	art, err := svc.Create(ctx, artifacts.CreateParams{Title: "页", Files: []artifacts.FileChange{{Path: "index.html", Data: strings.NewReader("<p>1</p>")}}})
	require.NoError(t, err)

	a := e.dial("dev_a")
	b := e.dial("dev_b")
	a.send(map[string]any{"type": "artifact.watch", "id": art.ID})
	require.Eventually(t, func() bool { return len(e.hub.Watchers(art.ID)) == 1 }, 5*time.Second, 10*time.Millisecond)

	_, err = svc.Comment(ctx, art.ID, artifacts.Anchor{Version: 1, Path: "index.html"}, "改一下")
	require.NoError(t, err)
	got := a.next(func(m msg) bool { return m.Type == "artifact.comments" })
	var v struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(got.raw, &v))
	assert.Equal(t, art.ID, v.ID)

	// b 没有观看：之后的列表变化通知之前不应出现 artifact.comments
	_, err = svc.Rename(ctx, art.ID, "新标题")
	require.NoError(t, err)
	for _, m := range b.collect(func(m msg) bool { return m.Type == "artifacts.changed" }) {
		assert.NotEqual(t, "artifact.comments", m.Type)
	}
}
