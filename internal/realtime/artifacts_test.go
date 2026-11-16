package realtime_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts"
	"github.com/Lynricsy/Oryn/internal/realtime"
)

// artifact.watch 之后一次写入：观看者收到 artifact.version 与 artifacts.changed，未观看的连接只收到 artifacts.changed；
// unwatch 与断开都会清掉观看并回调 OnWatchChange。
func TestArtifactWatch(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	svc, err := artifacts.New(artifacts.Config{DataDir: t.TempDir()}, e.st, e.hub)
	require.NoError(t, err)
	art, err := svc.Create(ctx, artifacts.CreateParams{Title: "页", Files: []artifacts.FileChange{{Path: "index.html", Data: strings.NewReader("<p>1</p>")}}})
	require.NoError(t, err)

	var mu sync.Mutex
	var changes []string
	e.hub.OnWatchChange(func(id string) {
		// 回调在锁外：这里可以再读观看者
		_ = e.hub.Watchers(id)
		mu.Lock()
		changes = append(changes, id)
		mu.Unlock()
	})
	changeCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(changes)
	}

	a := e.dial("dev_a")
	b := e.dial("dev_b")
	a.send(map[string]any{"type": "artifact.watch", "id": art.ID, "page": "c1"})
	require.Eventually(t, func() bool { return len(e.hub.Watchers(art.ID)) == 1 }, 5*time.Second, 10*time.Millisecond)
	w := e.hub.Watchers(art.ID)[0]
	assert.Equal(t, "c1", w.Page)
	assert.NotEmpty(t, w.Conn)
	assert.Equal(t, 1, changeCount())

	// 重复 watch 更新 page
	a.send(map[string]any{"type": "artifact.watch", "id": art.ID, "page": "c2"})
	require.Eventually(t, func() bool { return e.hub.Watchers(art.ID)[0].Page == "c2" }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 2, changeCount())

	_, err = svc.Write(ctx, artifacts.WriteParams{ID: art.ID, BaseVersion: 1, Files: []artifacts.FileChange{{Path: "index.html", Data: strings.NewReader("<p>2</p>")}}})
	require.NoError(t, err)

	got := a.next(func(m msg) bool { return m.Type == "artifact.version" })
	var v struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	require.NoError(t, json.Unmarshal(got.raw, &v))
	assert.Equal(t, art.ID, v.ID)
	assert.Equal(t, 2, v.Version)
	a.next(func(m msg) bool { return m.Type == "artifacts.changed" })
	// b 没有观看：artifact.version 若误发，会排在 artifacts.changed 之前
	for _, m := range b.collect(func(m msg) bool { return m.Type == "artifacts.changed" }) {
		assert.NotEqual(t, "artifact.version", m.Type)
	}

	// 非法 id 回错误
	b.send(map[string]any{"type": "artifact.watch", "id": "nope"})
	assert.Equal(t, "bad_message", b.next(func(m msg) bool { return m.Type == "error" }).Code)

	// unwatch 清掉观看
	a.send(map[string]any{"type": "artifact.unwatch", "id": art.ID})
	require.Eventually(t, func() bool { return len(e.hub.Watchers(art.ID)) == 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 3, changeCount())

	// 断开等于全部 unwatch
	b.send(map[string]any{"type": "artifact.watch", "id": art.ID})
	require.Eventually(t, func() bool { return len(e.hub.Watchers(art.ID)) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, b.ws.CloseNow())
	require.Eventually(t, func() bool { return len(e.hub.Watchers(art.ID)) == 0 }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return changeCount() == 5 }, 5*time.Second, 10*time.Millisecond)

	// ToWatchers 只发给观看者
	a.send(map[string]any{"type": "artifact.watch", "id": art.ID})
	require.Eventually(t, func() bool { return len(e.hub.Watchers(art.ID)) == 1 }, 5*time.Second, 10*time.Millisecond)
	e.hub.ToWatchers(art.ID, map[string]string{"type": "artifact.comments", "id": art.ID})
	a.next(func(m msg) bool { return m.Type == "artifact.comments" })
}

var _ artifacts.Hub = (*realtime.Hub)(nil)
