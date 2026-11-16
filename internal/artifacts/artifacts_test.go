package artifacts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// fakeHub 记下全部通知。
type fakeHub struct {
	mu       sync.Mutex
	changed  int
	versions []string
	comments []string
}

func (h *fakeHub) ArtifactsChanged() { h.mu.Lock(); h.changed++; h.mu.Unlock() }
func (h *fakeHub) ArtifactVersion(id string, v int) {
	h.mu.Lock()
	h.versions = append(h.versions, id+"@"+string(rune('0'+v)))
	h.mu.Unlock()
}
func (h *fakeHub) ArtifactComments(id string) {
	h.mu.Lock()
	h.comments = append(h.comments, id)
	h.mu.Unlock()
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type env struct {
	s   *Service
	st  *store.Store
	hub *fakeHub
	clk *clock
	dir string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	hub := &fakeHub{}
	clk := &clock{t: time.Now()}
	s, err := New(Config{DataDir: dir, PublicURL: "http://localhost:7600", Now: clk.now}, st, hub)
	require.NoError(t, err)
	return &env{s: s, st: st, hub: hub, clk: clk, dir: dir}
}

func mkThread(t *testing.T, st *store.Store, id, projectID string) {
	t.Helper()
	p := db.CreateThreadParams{Kind: "work", ID: id, TitleSource: "auto", State: "idle", SessionDir: "~/.oryn/threads/" + id}
	if projectID != "" {
		p.ProjectID = &projectID
	}
	require.NoError(t, st.Q.CreateThread(context.Background(), p))
}

func put(path, content string) FileChange {
	return FileChange{Path: path, Data: strings.NewReader(content)}
}

func readFile(t *testing.T, s *Service, id string, version int, path string) string {
	t.Helper()
	b, _, err := s.ReadFile(context.Background(), id, version, path)
	require.NoError(t, err)
	return string(b)
}

func code(err error) string {
	if oe, ok := AsOpError(err); ok {
		return oe.Code
	}
	if err != nil {
		return "other: " + err.Error()
	}
	return ""
}

func countContent(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(filepath.Join(dir, "artifacts", "content"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestCreateFilesAndDedup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.s.Create(ctx, CreateParams{Title: "报告", Files: []FileChange{
		put("index.html", "<h1>hi</h1>"), put("a.css", "same"), put("b.css", "same"),
	}})
	require.NoError(t, err)
	assert.Equal(t, 1, a.Version)
	assert.Equal(t, KindWeb, a.Kind)
	assert.Equal(t, 1, a.Pages)
	files, err := e.s.Files(ctx, a.ID, 0)
	require.NoError(t, err)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
		_, err := os.Stat(filepath.Join(e.dir, "artifacts", "content", f.Sha256[:2], f.Sha256))
		require.NoError(t, err, "content for %s", f.Path)
	}
	assert.Equal(t, []string{"a.css", "b.css", "index.html"}, paths)
	assert.Equal(t, 2, countContent(t, e.dir), "same content stored once")
	assert.Equal(t, "<h1>hi</h1>", readFile(t, e.s, a.ID, 1, "index.html"))
	assert.Equal(t, []string{a.ID + "@1"}, e.hub.versions)
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cases := map[string]CreateParams{
		"no entry":    {Title: "x", Files: []FileChange{put("a.html", "x")}},
		"bad path":    {Title: "x", Files: []FileChange{put("index.html", "x"), put("../a", "x")}},
		"case clash":  {Title: "x", Files: []FileChange{put("index.html", "x"), put("A.html", "x"), put("a.html", "y")}},
		"not utf8":    {Title: "x", Files: []FileChange{put("index.html", "\xff\xfe")}},
		"empty title": {Title: "  ", Files: []FileChange{put("index.html", "x")}},
		"typed html":  {Title: "x", Type: "document", Files: []FileChange{put("document.md", "# x"), put("index.html", "x")}},
		"bad cap":     {Title: "x", Files: []FileChange{put("index.html", "x")}, Capabilities: []string{"net"}},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := e.s.Create(ctx, p)
			assert.Equal(t, CodeInvalid, code(err))
		})
	}
	_, err := e.s.Create(ctx, CreateParams{Title: "big", Files: []FileChange{put("index.html", strings.Repeat("x", MaxFileBytes+1))}})
	assert.Equal(t, CodeTooLarge, code(err))
}

func TestWriteFileGranularConflicts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.s.Create(ctx, CreateParams{Title: "x", Files: []FileChange{put("index.html", "i"), put("a.html", "a1"), put("b.html", "b1")}})
	require.NoError(t, err)
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("a.html", "a2")}})
	require.NoError(t, err)
	got, err := e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("b.html", "b2")}})
	require.NoError(t, err)
	assert.Equal(t, 3, got.Version)
	assert.Equal(t, "a2", readFile(t, e.s, a.ID, 3, "a.html"))
	assert.Equal(t, "b2", readFile(t, e.s, a.ID, 3, "b.html"))

	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("a.html", "a3")}})
	oe, ok := AsOpError(err)
	require.True(t, ok, "err = %v", err)
	assert.Equal(t, CodeConflict, oe.Code)
	assert.Equal(t, []string{"a.html"}, oe.State.Changed)
	assert.Equal(t, 3, oe.State.Version)
	cur, _ := e.s.Get(ctx, a.ID)
	assert.Equal(t, 3, cur.Version, "failed write adds no version")

	// Unchecked 的路径不参与检查
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("a.html", "a3")}, Unchecked: []string{"a.html"}})
	require.NoError(t, err)

	// 能力声明作为伪路径参与冲突
	caps := []string{"db"}
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 4, Capabilities: &caps})
	require.NoError(t, err)
	caps2 := []string{"files"}
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 4, Capabilities: &caps2})
	assert.Equal(t, CodeConflict, code(err))

	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 99, Files: []FileChange{put("x.html", "x")}})
	assert.Equal(t, CodeInvalid, code(err))
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 5, Files: []FileChange{{Path: "nope.html", Remove: true}}})
	assert.Equal(t, CodeInvalid, code(err))
	got, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 5, Files: []FileChange{{Path: "b.html", Remove: true}}})
	require.NoError(t, err)
	files, _ := e.s.Files(ctx, a.ID, got.Version)
	assert.Len(t, files, 2)
	v, err := e.s.Version(ctx, a.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"db"}, v.Capabilities, "capabilities carried over")
}

func TestRestoreVersion(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.s.Create(ctx, CreateParams{Title: "x", Files: []FileChange{put("index.html", "v1")}})
	require.NoError(t, err)
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("index.html", "v2"), put("extra.css", "c")}})
	require.NoError(t, err)
	got, err := e.s.RestoreVersion(ctx, a.ID, 1, 2, Author{})
	require.NoError(t, err)
	assert.Equal(t, 3, got.Version)
	files, _ := e.s.Files(ctx, a.ID, 3)
	require.Len(t, files, 1)
	assert.Equal(t, "v1", readFile(t, e.s, a.ID, 3, "index.html"))
	assert.Equal(t, "v2", readFile(t, e.s, a.ID, 2, "index.html"))
	// 以过期的 base 恢复：两版之间不同的文件在 base 之后被改过 → 冲突
	_, err = e.s.RestoreVersion(ctx, a.ID, 2, 2, Author{})
	assert.Equal(t, CodeConflict, code(err))
	vs, err := e.s.Versions(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, "恢复自版本 1", vs[0].Note)
}

func TestRenameDeletePurge(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.s.Create(ctx, CreateParams{Title: "x", Files: []FileChange{put("index.html", "unique-content")}})
	require.NoError(t, err)
	r, err := e.s.Rename(ctx, a.ID, "新名字")
	require.NoError(t, err)
	assert.Equal(t, "新名字", r.Title)
	assert.Equal(t, 1, r.Version, "rename adds no version")

	_, err = e.s.Delete(ctx, a.ID)
	require.NoError(t, err)
	live, _ := e.s.List(ctx, Filter{})
	assert.Empty(t, live)
	trash, _ := e.s.List(ctx, Filter{Deleted: true})
	require.Len(t, trash, 1)
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("index.html", "y")}})
	assert.Equal(t, CodeDeleted, code(err))
	_, err = e.s.Rename(ctx, a.ID, "y")
	assert.Equal(t, CodeDeleted, code(err))

	e.s.Purge(ctx)
	_, err = e.s.Get(ctx, a.ID)
	require.NoError(t, err, "not yet expired")

	e.clk.add(31 * 24 * time.Hour)
	e.s.Purge(ctx)
	_, err = e.s.Get(ctx, a.ID)
	assert.Equal(t, CodeNotFound, code(err))
	assert.Equal(t, 0, countContent(t, e.dir), "unreferenced content removed")
}

func TestUndeleteAndErase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.s.Create(ctx, CreateParams{Title: "x", Files: []FileChange{put("index.html", "x")}})
	require.NoError(t, err)
	assert.Equal(t, CodeInvalid, code(e.s.Erase(ctx, a.ID)), "only trash can be erased")
	_, _ = e.s.Delete(ctx, a.ID)
	u, err := e.s.Undelete(ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, u.Deleted())
	_, _ = e.s.Delete(ctx, a.ID)
	require.NoError(t, e.s.Erase(ctx, a.ID))
	_, err = e.s.Get(ctx, a.ID)
	assert.Equal(t, CodeNotFound, code(err))
}

func TestListByThreadIncludesAuthors(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mkThread(t, e.st, "T-a", "")
	mkThread(t, e.st, "T-b", "")
	a, err := e.s.Create(ctx, CreateParams{Title: "x", ThreadID: "T-a", Author: Author{ThreadID: "T-a"}, Files: []FileChange{put("index.html", "x")}})
	require.NoError(t, err)
	list, _ := e.s.List(ctx, Filter{ThreadID: "T-b"})
	assert.Empty(t, list)
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Author: Author{ThreadID: "T-b", Label: "B"}, Files: []FileChange{put("index.html", "y")}})
	require.NoError(t, err)
	list, _ = e.s.List(ctx, Filter{ThreadID: "T-b"})
	assert.Len(t, list, 1)
	authors, err := e.s.AuthorThreads(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"T-b", "T-a"}, authors)
}

func TestRateLimitCountsOnlySuccess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	au := Author{ThreadID: "T-x"}
	a, err := e.s.Create(ctx, CreateParams{Title: "x", Author: au, Files: []FileChange{put("index.html", "0")}})
	require.NoError(t, err)
	for range 5 {
		_, err := e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 99, Author: au, Files: []FileChange{put("index.html", "f")}})
		require.Error(t, err)
	}
	head := 1
	for i := 1; i < WritesPerMinute; i++ {
		got, err := e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: head, Author: au, Files: []FileChange{put("index.html", strings.Repeat("y", i))}})
		require.NoError(t, err, "write %d", i)
		head = got.Version
	}
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: head, Author: au, Files: []FileChange{put("index.html", "z")}})
	assert.Equal(t, CodeRateLimited, code(err))
	e.clk.add(61 * time.Second)
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: head, Author: au, Files: []FileChange{put("index.html", "z")}})
	require.NoError(t, err)
}

func TestCanvasKindAndPages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	doc := `{"format":1,"canvases":[{"id":"c1","name":"首页"}],"pages":[` +
		`{"id":"a","name":"A","file":"a.html"},{"id":"b","name":"B","file":"b.html"}]}`
	a, err := e.s.Create(ctx, CreateParams{Title: "c", Files: []FileChange{
		put("canvas.json", doc), put("a.html", "a"), put("b.html", "b"),
	}})
	require.NoError(t, err)
	assert.Equal(t, KindCanvas, a.Kind)
	assert.Equal(t, 2, a.Pages)

	// 严格解析：未知字段、悬空引用、画框文件不在版本里都回 invalid，不落版本
	for _, bad := range []struct{ doc, want string }{
		{`{"pages":[{"id":"a"}]}`, "pages[0].name"},
		{`{"pages":[{"id":"a","name":"A","file":"a.html","color":"red"}]}`, "pages[0].color"},
		{`{"pages":[{"id":"a","canvas":"c9","name":"A","file":"a.html"}]}`, "pages[0].canvas"},
		{`{"pages":[{"id":"a","name":"A","file":"gone.html"}]}`, "gone.html"},
	} {
		_, err := e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("canvas.json", bad.doc)}})
		assert.Equal(t, CodeInvalid, code(err), bad.doc)
		assert.Contains(t, err.Error(), bad.want)
	}
	got, err := e.s.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Version)

	// 删掉一个画框连同它的文件，页数跟着变
	b, err := e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{
		put("canvas.json", `{"pages":[{"id":"a","name":"A","file":"a.html"}]}`), {Path: "b.html", Remove: true},
	}})
	require.NoError(t, err)
	assert.Equal(t, 1, b.Pages)
}

// 清理列出过期项之后、删除之前被恢复（或恢复后重新删除）的 artifact 不能被删掉。
func TestPurgeLosesToConcurrentUndelete(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.s.Create(ctx, CreateParams{Title: "x", Files: []FileChange{put("index.html", "x")}})
	require.NoError(t, err)
	b, err := e.s.Create(ctx, CreateParams{Title: "y", Files: []FileChange{put("index.html", "y")}})
	require.NoError(t, err)
	_, _ = e.s.Delete(ctx, a.ID)
	_, _ = e.s.Delete(ctx, b.ID)
	e.clk.add(31 * 24 * time.Hour)
	cutoff := e.clk.now().Add(-KeepDeleted).UnixMilli()
	stale, err := e.st.Q.ListArtifactsDeletedBefore(ctx, &cutoff)
	require.NoError(t, err)
	require.Len(t, stale, 2)

	// 交错：a 被恢复；b 被恢复后又重新删除（deleted_at 变新，未过期）
	_, err = e.s.Undelete(ctx, a.ID)
	require.NoError(t, err)
	_, _ = e.s.Undelete(ctx, b.ID)
	_, _ = e.s.Delete(ctx, b.ID)

	assert.False(t, e.s.purgeOne(ctx, a.ID, cutoff))
	assert.False(t, e.s.purgeOne(ctx, b.ID, cutoff))
	got, err := e.s.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, got.Deleted())
	_, err = e.s.Get(ctx, b.ID)
	require.NoError(t, err)
	files, err := e.s.Files(ctx, a.ID, 0)
	require.NoError(t, err)
	assert.Len(t, files, 1)
}

// Erase 的删除语句自带「仍在回收站里」的条件：检查之后被恢复的不会被永久删除。
func TestEraseGuardedAgainstUndelete(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.s.Create(ctx, CreateParams{Title: "x", Files: []FileChange{put("index.html", "x")}})
	require.NoError(t, err)
	_, _ = e.s.Delete(ctx, a.ID)
	_, _ = e.s.Undelete(ctx, a.ID) // 发生在 Erase 的检查之后
	n, err := e.st.Q.EraseArtifact(ctx, a.ID)
	require.NoError(t, err)
	assert.Zero(t, n)
	_, err = e.s.Get(ctx, a.ID)
	require.NoError(t, err)
}

// 旧孤儿内容被一次写入复用（put 已刷新 mtime、引用行还没提交）时，同时进行的内容清理不能把它删掉。
// 时钟拨到未来，让刷新后的 mtime 仍早于 cutoff：只有「写入持读锁、清理逐个持写锁再查引用」能保住它。
func TestContentGCDoesNotRemoveReusedOrphan(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.s.Create(ctx, CreateParams{Title: "x", Files: []FileChange{put("index.html", "reused-orphan")}})
	require.NoError(t, err)
	_, err = e.s.Delete(ctx, a.ID)
	require.NoError(t, err)
	require.NoError(t, e.s.Erase(ctx, a.ID))
	require.Equal(t, 1, countContent(t, e.dir), "orphan content still on disk")
	e.clk.add(48 * time.Hour)

	reached := make(chan struct{})
	var once sync.Once
	e.s.gcBeforeLock = func(string) { once.Do(func() { close(reached) }) }
	gcDone := make(chan struct{})
	e.s.afterStage = func() {
		e.s.afterStage = nil
		go func() {
			defer close(gcDone)
			e.s.Purge(ctx)
		}()
		<-reached // 清理已选中这个旧孤儿、正要拿写锁；此刻引用行还没提交
		assert.False(t, e.s.contentMu.TryLock(), "writer must hold the content read lock across stage → commit")
	}
	b, err := e.s.Create(ctx, CreateParams{Title: "y", Files: []FileChange{put("index.html", "reused-orphan")}})
	require.NoError(t, err)
	<-gcDone
	assert.Equal(t, "reused-orphan", readFile(t, e.s, b.ID, 1, "index.html"))
	assert.Equal(t, 1, countContent(t, e.dir))
}
