package artifacts

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/store"
)

// liveHub 在 fakeHub 之上记下画布广播，并模拟观看者登记。
type liveHub struct {
	fakeHub
	mu    sync.Mutex
	pages map[string][]string
	fns   []func(string)
	msgs  chan apigen.WsServerArtifactCanvas
}

func (h *liveHub) ArtifactCanvas(m apigen.WsServerArtifactCanvas) { h.msgs <- m }

func (h *liveHub) WatchPages(id string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.pages[id])
}

func (h *liveHub) OnWatchChange(fn func(string)) {
	h.mu.Lock()
	h.fns = append(h.fns, fn)
	h.mu.Unlock()
}

// watch 把 id 的观看者换成 pages（每个元素是一个观看者所在的画布页），并通知观看者变化。
func (h *liveHub) watch(id string, pages ...string) {
	h.mu.Lock()
	h.pages[id] = pages
	fns := slices.Clone(h.fns)
	h.mu.Unlock()
	for _, fn := range fns {
		fn(id)
	}
}

func (h *liveHub) next(t *testing.T) apigen.WsServerArtifactCanvas {
	t.Helper()
	select {
	case m := <-h.msgs:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("等不到 artifact.canvas 广播")
		return apigen.WsServerArtifactCanvas{}
	}
}

func (h *liveHub) none(t *testing.T) {
	t.Helper()
	select {
	case m := <-h.msgs:
		t.Fatalf("不该有广播：%+v", m)
	default:
	}
}

// fakeTimers 是可手动触发的静默计时器。
type fakeTimers struct {
	mu sync.Mutex
	ts []*fakeTimer
}

type fakeTimer struct {
	d   time.Duration
	f   func()
	off bool
}

func (ft *fakeTimers) after(d time.Duration, f func()) func() bool {
	t := &fakeTimer{d: d, f: f}
	ft.mu.Lock()
	ft.ts = append(ft.ts, t)
	ft.mu.Unlock()
	return func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		was := !t.off
		t.off = true
		return was
	}
}

// fire 触发全部还在等的计时器，返回触发了几个。
func (ft *fakeTimers) fire() int {
	ft.mu.Lock()
	var fs []func()
	for _, t := range ft.ts {
		if !t.off {
			t.off = true
			fs = append(fs, t.f)
		}
	}
	ft.mu.Unlock()
	for _, f := range fs {
		f()
	}
	return len(fs)
}

func (ft *fakeTimers) armed() []time.Duration {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var out []time.Duration
	for _, t := range ft.ts {
		if !t.off {
			out = append(out, t.d)
		}
	}
	return out
}

func newLiveEnv(t *testing.T) (*env, *liveHub, *fakeTimers) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	hub := &liveHub{pages: map[string][]string{}, msgs: make(chan apigen.WsServerArtifactCanvas, 256)}
	clk := &clock{t: time.Now()}
	s, err := New(Config{DataDir: dir, PublicURL: "http://localhost:7600", Now: clk.now}, st, hub)
	require.NoError(t, err)
	ft := &fakeTimers{}
	s.live.after = ft.after
	s.AttachCanvasHub(hub)
	return &env{s: s, st: st, hub: &hub.fakeHub, clk: clk, dir: dir}, hub, ft
}

const liveDoc = `{"format":1,"canvases":[{"id":"c1","name":"首页"},{"id":"c2","name":"细节"}],"pages":[` +
	`{"id":"p1","canvas":"c1","name":"封面","file":"a.html","x":0,"y":0,"width":1280,"height":800}]}`

func liveCanvas(t *testing.T, e *env) Artifact {
	t.Helper()
	a, err := e.s.Create(context.Background(), CreateParams{Title: "画布", Files: []FileChange{
		put("canvas.json", liveDoc), put("a.html", "<p>封面</p>"),
	}})
	require.NoError(t, err)
	return a
}

func headDoc(t *testing.T, e *env, id string) canvas.Doc {
	t.Helper()
	d, err := canvas.Parse([]byte(readFile(t, e.s, id, 0, "canvas.json")))
	require.NoError(t, err)
	return d
}

func headAuthor(t *testing.T, e *env, id string) string {
	t.Helper()
	v, err := e.s.Version(context.Background(), id, 0)
	require.NoError(t, err)
	return v.AuthorThreadID
}

func noteIDs(d canvas.Doc) []string {
	out := make([]string, 0, len(d.Notes))
	for _, n := range d.Notes {
		out = append(out, n.ID)
	}
	return out
}

func sticky(id, text string) string {
	return `{"op":"add","object":{"type":"note","id":"` + id + `","kind":"sticky","canvas":"c1","x":10,"y":10,"text":"` + text + `"}}`
}

func applyOps(t *testing.T, e *env, id, client string, clientSeq int64, ops ...string) int64 {
	t.Helper()
	raw := "[" + ops[0]
	for _, o := range ops[1:] {
		raw += "," + o
	}
	seq, err := e.s.ApplyCanvas(context.Background(), id, json.RawMessage(raw+"]"), client, clientSeq)
	require.NoError(t, err)
	return seq
}

// 两个客户端各发一条操作：双方（全部观看者）都收到对方的广播，seq 递增；状态里是合并后的文档。
func TestLiveTwoClients(t *testing.T) {
	e, hub, ft := newLiveEnv(t)
	a := liveCanvas(t, e)
	hub.watch(a.ID, "c1", "c2")

	s1 := applyOps(t, e, a.ID, "tab-a", 1, sticky("n1", "改成绿色"))
	m := hub.next(t)
	assert.Equal(t, apigen.WsServerArtifactCanvasTypeArtifactCanvas, m.Type)
	assert.Equal(t, a.ID, m.ID)
	assert.Equal(t, s1, m.Seq)
	assert.Equal(t, "tab-a", *m.Client)
	assert.EqualValues(t, 1, *m.ClientSeq)
	require.Len(t, m.Ops, 1)
	obj := m.Ops[0]["object"].(map[string]any)
	assert.Nil(t, obj["by"], "用户画的标注 by 为 null")
	assert.Equal(t, 240.0, obj["width"], "广播的是补全缺省值后的对象")

	s2 := applyOps(t, e, a.ID, "tab-b", 7, `{"op":"set","id":"p1","key":"x","value":100}`)
	m = hub.next(t)
	assert.Equal(t, s1+1, s2)
	assert.Equal(t, s2, m.Seq)
	assert.Equal(t, "tab-b", *m.Client)
	assert.EqualValues(t, 7, *m.ClientSeq)

	st, err := e.s.CanvasState(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, st.Version)
	assert.Equal(t, s2, st.Seq)
	assert.Equal(t, []string{"n1"}, noteIDs(st.Doc))
	assert.Equal(t, 100.0, st.Doc.Pages[0].X)
	assert.Equal(t, []time.Duration{CanvasQuiet}, ft.armed(), "每批操作都重置同一个静默计时器")

	// 用户提交的 by 被服务端改成 null；非法批次整批不生效、不广播
	_, err = e.s.ApplyCanvas(context.Background(), a.ID, json.RawMessage(`[{"op":"add","object":{"type":"note","id":"n2","kind":"text","canvas":"c1","x":0,"y":0,"text":"t","by":"thr_x"}}]`), "tab-a", 2)
	require.NoError(t, err)
	assert.Nil(t, hub.next(t).Ops[0]["object"].(map[string]any)["by"])
	_, err = e.s.ApplyCanvas(context.Background(), a.ID, json.RawMessage(`[{"op":"set","id":"p1","key":"name","value":"新"},{"op":"set","id":"p1","key":"width","value":1}]`), "tab-a", 3)
	assert.Equal(t, CodeInvalid, code(err))
	_, err = e.s.ApplyCanvas(context.Background(), a.ID, json.RawMessage(`[]`), "tab-a", 3)
	assert.Equal(t, CodeInvalid, code(err))
	_, err = e.s.ApplyCanvas(context.Background(), a.ID, json.RawMessage(`[`+sticky("n9", "x")+`]`), "", 3)
	assert.Equal(t, CodeInvalid, code(err))
	hub.none(t)
	assert.Equal(t, 1, headVersion(t, e, a.ID), "没有落版本")
}

func headVersion(t *testing.T, e *env, id string) int {
	t.Helper()
	a, err := e.s.Get(context.Background(), id)
	require.NoError(t, err)
	return a.Version
}

// 静默计时器到点：累计的操作落成一个用户版本；新画框的文件是空白页，copyOf 复制来源画框的文件。
func TestLiveQuietFlush(t *testing.T) {
	e, hub, ft := newLiveEnv(t)
	a := liveCanvas(t, e)
	hub.watch(a.ID, "c1")
	applyOps(t, e, a.ID, "tab-a", 1, sticky("n1", "便签"))
	applyOps(t, e, a.ID, "tab-a", 2,
		`{"op":"add","object":{"type":"page","id":"p2","canvas":"c1","name":"新画框"}}`,
		`{"op":"add","object":{"type":"page","id":"p3","canvas":"c1","name":"副本","x":0,"y":900},"copyOf":"p1"}`)
	hub.next(t)
	hub.next(t)
	assert.Equal(t, 1, headVersion(t, e, a.ID))

	require.Equal(t, 1, ft.fire())
	assert.Equal(t, 2, headVersion(t, e, a.ID))
	assert.Empty(t, headAuthor(t, e, a.ID), "作者是用户")
	d := headDoc(t, e, a.ID)
	assert.Equal(t, []string{"n1"}, noteIDs(d))
	require.Len(t, d.Pages, 3)
	assert.Equal(t, "page-p2.html", d.Pages[1].File)
	assert.Equal(t, BlankPage, readFile(t, e.s, a.ID, 0, "page-p2.html"))
	assert.Equal(t, "<p>封面</p>", readFile(t, e.s, a.ID, 0, "page-p3.html"))
	a2, err := e.s.Get(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, a2.Pages)

	// 落版本后状态的底稿是新版本，没有待落的操作；自己的版本不触发整份文档的广播
	st, err := e.s.CanvasState(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, st.Version)
	assert.Empty(t, ft.armed())
	assert.Equal(t, 0, ft.fire(), "没有新操作时计时器不再落版本")
	assert.Equal(t, 2, headVersion(t, e, a.ID))
	// 后台的「新版本」处理对自己的版本什么也不做
	e.s.liveFollow(a.ID, 2)
	hub.none(t)
}

// 最后一个观看者离开：立即落成用户版本并回收实时状态。
func TestLiveLastWatcherLeaves(t *testing.T) {
	e, hub, ft := newLiveEnv(t)
	a := liveCanvas(t, e)
	hub.watch(a.ID, "c1", "")
	require.NotNil(t, e.s.liveOf(a.ID), "有人在看就有实时状态")
	applyOps(t, e, a.ID, "tab-a", 1, sticky("n1", "离开前"))
	hub.next(t)

	hub.watch(a.ID, "c1")
	assert.Equal(t, 1, headVersion(t, e, a.ID), "还有人在看，不落版本")
	hub.watch(a.ID)
	assert.Equal(t, 2, headVersion(t, e, a.ID))
	assert.Empty(t, headAuthor(t, e, a.ID))
	assert.Equal(t, []string{"n1"}, noteIDs(headDoc(t, e, a.ID)))
	assert.Nil(t, e.s.liveOf(a.ID))
	assert.Empty(t, ft.armed())

	// 回收之后序号不回退
	hub.watch(a.ID, "c1")
	seq := applyOps(t, e, a.ID, "tab-a", 2, sticky("n2", "回来了"))
	assert.Greater(t, seq, int64(1))
}

// 别处产生了新版本：重新加载底稿，把未落版本的操作重放（放不上的丢弃），广播整份文档与新版本。
func TestLiveReplayOnForeignVersion(t *testing.T) {
	e, hub, ft := newLiveEnv(t)
	a := liveCanvas(t, e)
	hub.watch(a.ID, "c1")
	applyOps(t, e, a.ID, "tab-a", 1, sticky("n1", "保留"), sticky("n2", "被别处占用"))
	hub.next(t)

	// 别处（agent 用 files）写了 canvas.json：改了画布页名，并且自己加了 id 为 n2 的标注
	foreign := `{"format":1,"canvases":[{"id":"c1","name":"改名了"},{"id":"c2","name":"细节"}],"pages":[` +
		`{"id":"p1","canvas":"c1","name":"封面","file":"a.html","x":0,"y":0,"width":1280,"height":800}],` +
		`"notes":[{"id":"n2","canvas":"c1","kind":"text","x":0,"y":0,"text":"agent 的"}]}`
	_, err := e.s.Write(context.Background(), WriteParams{ID: a.ID, BaseVersion: 1, Author: Author{ThreadID: "thr_a"}, Files: []FileChange{put("canvas.json", foreign)}})
	require.NoError(t, err)

	m := hub.next(t)
	require.NotNil(t, m.Version)
	assert.Equal(t, 2, *m.Version)
	assert.Nil(t, m.Ops)
	require.NotNil(t, m.Doc)
	st, err := e.s.CanvasState(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, st.Version)
	assert.Equal(t, m.Seq, st.Seq)
	assert.Equal(t, "改名了", st.Doc.Canvases[0].Name)
	assert.Equal(t, []string{"n2", "n1"}, noteIDs(st.Doc), "n1 重放在新底稿上，冲突的 n2 被丢弃")
	assert.Equal(t, "agent 的", st.Doc.Notes[0].Text)

	// 重放后的操作照常落成用户版本
	require.Equal(t, 1, ft.fire())
	assert.Equal(t, 3, headVersion(t, e, a.ID))
	assert.Equal(t, []string{"n2", "n1"}, noteIDs(headDoc(t, e, a.ID)))

	// 只改页面文件的新版本：底稿跟上，文档不变
	_, err = e.s.Write(context.Background(), WriteParams{ID: a.ID, BaseVersion: 3, Files: []FileChange{put("a.html", "<p>新封面</p>")}})
	require.NoError(t, err)
	m = hub.next(t)
	assert.Equal(t, 4, *m.Version)
}

// 落版本时发现底稿之后 canvas.json 被别处改过（后台重载还没轮到）：先重载重放再写，不丢别处的改动。
func TestLiveFlushRebases(t *testing.T) {
	e, hub, ft := newLiveEnv(t)
	a := liveCanvas(t, e)
	hub.watch(a.ID, "c1")
	applyOps(t, e, a.ID, "tab-a", 1, sticky("n1", "我的"))
	hub.next(t)
	unlock := e.s.live.locks.lock(a.ID) // 挡住后台重载
	foreign := `{"format":1,"canvases":[{"id":"c1","name":"首页"},{"id":"c2","name":"细节"}],"pages":[` +
		`{"id":"p1","canvas":"c1","name":"别处改名","file":"a.html","x":0,"y":0,"width":1280,"height":800}]}`
	_, err := e.s.Write(context.Background(), WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("canvas.json", foreign)}})
	require.NoError(t, err)
	l := e.s.liveOf(a.ID)
	require.NoError(t, e.s.flushLive(context.Background(), a.ID, l))
	unlock()
	assert.Equal(t, 3, headVersion(t, e, a.ID))
	d := headDoc(t, e, a.ID)
	assert.Equal(t, "别处改名", d.Pages[0].Name)
	assert.Equal(t, []string{"n1"}, noteIDs(d))
	assert.Empty(t, ft.armed())
}

// agent 带 canvas 的 update_artifact：用户有未落版本的操作时先单独落成用户版本，再写 agent 的版本；
// agent 加的标注 by 是它的线程；新画框补空白页；与 canvas.json 的 files / edits 不能同时给。
func TestLiveAgentCanvas(t *testing.T) {
	e, hub, ft := newLiveEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	a, err := e.s.Create(context.Background(), CreateParams{Title: "画布", ThreadID: "thr_a", Author: c.author(), Files: []FileChange{
		put("canvas.json", liveDoc), put("a.html", "<p>封面</p>"),
	}})
	require.NoError(t, err)
	hub.watch(a.ID, "c2")
	applyOps(t, e, a.ID, "tab-a", 1, sticky("u1", "用户的便签"))
	hub.next(t)

	// read_artifact 对画布缺省读 canvas.json，并列出观看者所在的画布页
	r := mustCall(t, e.s, c, OpRead, `{"id":"`+a.ID+`"}`)
	h, body := header(t, r)
	assert.Equal(t, "canvas.json", h["path"])
	assert.Equal(t, []any{"细节"}, h["viewing"])
	assert.Contains(t, body, `"canvases"`)

	r = mustCall(t, e.s, c, OpUpdate, `{"id":"`+a.ID+`","baseVersion":1,"note":"加画框","canvas":[`+
		`{"op":"add","object":{"type":"page","id":"p2","canvas":"c1","name":"设置页"}},`+
		`{"op":"add","object":{"type":"note","id":"g1","kind":"text","canvas":"c1","x":5,"y":5,"text":"agent 的","by":null}}],`+
		`"files":[{"path":"style.css","content":"p{color:green}"}]}`)
	out := textObj(t, r)
	assert.EqualValues(t, 3, out["version"])
	vs, err := e.s.Versions(context.Background(), a.ID)
	require.NoError(t, err)
	require.Len(t, vs, 3)
	assert.Equal(t, "thr_a", vs[0].AuthorThreadID, "v3 是 agent 的")
	assert.Empty(t, vs[1].AuthorThreadID, "v2 是用户的")
	v2, err := canvas.Parse([]byte(readFile(t, e.s, a.ID, 2, "canvas.json")))
	require.NoError(t, err)
	assert.Equal(t, []string{"u1"}, noteIDs(v2))
	d := headDoc(t, e, a.ID)
	assert.Equal(t, []string{"u1", "g1"}, noteIDs(d))
	require.NotNil(t, d.Notes[1].By)
	assert.Equal(t, "thr_a", *d.Notes[1].By)
	assert.Nil(t, d.Notes[0].By)
	assert.Equal(t, BlankPage, readFile(t, e.s, a.ID, 0, "page-p2.html"))
	assert.Equal(t, "p{color:green}", readFile(t, e.s, a.ID, 0, "style.css"))
	assert.Empty(t, ft.armed())

	// 观看者收到整份文档与 agent 的版本
	m := hub.next(t)
	require.NotNil(t, m.Version)
	assert.Equal(t, 3, *m.Version)
	st, err := e.s.CanvasState(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, st.Version)
	assert.Equal(t, []string{"u1", "g1"}, noteIDs(st.Doc))

	// 过期的 baseVersion 写被改过的文件 → conflict；不落版本
	mustCall(t, e.s, c, OpUpdate, `{"id":"`+a.ID+`","baseVersion":3,"files":[{"path":"a.html","content":"<p>v4</p>"}]}`)
	hub.next(t)
	_, err = agentCall(t, e.s, c, OpUpdate, `{"id":"`+a.ID+`","baseVersion":3,"canvas":[{"op":"set","id":"p1","key":"x","value":5}],"files":[{"path":"a.html","content":"<p>old</p>"}]}`)
	assert.Equal(t, CodeConflict, code(err))
	// 只给 canvas：baseVersion 旧也不冲突（canvas.json 由服务端合并）
	r = mustCall(t, e.s, c, OpUpdate, `{"id":"`+a.ID+`","baseVersion":1,"canvas":[{"op":"set","id":"p1","key":"x","value":5}]}`)
	assert.EqualValues(t, 5, textObj(t, r)["version"])
	assert.Equal(t, 5.0, headDoc(t, e, a.ID).Pages[0].X)
	hub.next(t)

	for _, p := range []string{
		`{"id":"` + a.ID + `","baseVersion":5,"canvas":[]}`,
		`{"id":"` + a.ID + `","baseVersion":5,"canvas":{"op":"remove"}}`,
		`{"id":"` + a.ID + `","baseVersion":5,"canvas":[{"op":"remove","id":"c1"},{"op":"remove","id":"c2"}]}`,
		`{"id":"` + a.ID + `","baseVersion":5,"canvas":[{"op":"remove","id":"g1"}],"files":[{"path":"canvas.json","content":"{}"}]}`,
		`{"id":"` + a.ID + `","baseVersion":5,"canvas":[{"op":"remove","id":"g1"}],"edits":[{"oldText":"设置页","newText":"x"}]}`,
	} {
		_, err = agentCall(t, e.s, c, OpUpdate, p)
		assert.Equal(t, CodeInvalid, code(err), p)
	}
	assert.Equal(t, 5, headVersion(t, e, a.ID))

	// 不是画布的 artifact 不能用 canvas
	w, err := e.s.Create(context.Background(), CreateParams{Title: "页面", ThreadID: "thr_a", Author: c.author(), Files: []FileChange{put("index.html", "<p>x</p>")}})
	require.NoError(t, err)
	_, err = agentCall(t, e.s, c, OpUpdate, `{"id":"`+w.ID+`","baseVersion":1,"canvas":[{"op":"remove","id":"x"}]}`)
	assert.Equal(t, CodeInvalid, code(err))
}

// 方案组的选定：用户提交的 pickedBy（add 带的、单独 set 的）一律为 null；set picked 在服务端展开成搬移操作，
// 广播的就是展开后的批次；agent 选定时 pickedBy 盖章为调用线程，伪造的值被覆盖。
func TestLivePickStamped(t *testing.T) {
	e, hub, _ := newLiveEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	a, err := e.s.Create(context.Background(), CreateParams{Title: "画布", ThreadID: "thr_a", Author: c.author(), Files: []FileChange{
		put("canvas.json", liveDoc), put("a.html", "<p>封面</p>"),
	}})
	require.NoError(t, err)
	hub.watch(a.ID, "c1")
	applyOps(t, e, a.ID, "tab", 1,
		`{"op":"add","object":{"type":"page","id":"p2","canvas":"c1","name":"暖调","x":1400,"y":0,"width":1280,"height":800}}`,
		`{"op":"add","object":{"type":"group","id":"home","name":"首页首屏","variants":[`+
			`{"id":"a","name":"冷静","note":"","theme":"","pages":["p1"]},{"id":"b","name":"暖调","note":"","theme":"","pages":["p2"]}],`+
			`"picked":"a","pickedBy":"thr_x"}}`)
	g := hub.next(t).Ops[1]["object"].(map[string]any)
	assert.Nil(t, g["pickedBy"], "add 带的 pickedBy 被改成 null")
	assert.Equal(t, "c1", g["canvas"], "广播的是补全缺省值后的方案组")

	applyOps(t, e, a.ID, "tab", 2,
		`{"op":"set","id":"home","key":"picked","value":"b"}`,
		`{"op":"set","id":"home","key":"pickedBy","value":"thr_x"}`)
	m := hub.next(t)
	var keys []string
	for _, op := range m.Ops {
		if k, ok := op["key"].(string); ok {
			keys = append(keys, k)
		}
	}
	assert.Equal(t, []string{"picked", "canvas", "x", "y"}, keys, "广播展开后的批次，提交的 pickedBy 被剔掉")
	assert.Equal(t, canvas.AlternatesID, m.Ops[1]["object"].(map[string]any)["id"], "备选方案页先 add")
	st, err := e.s.CanvasState(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, "b", *st.Doc.Groups[0].Picked)
	assert.Nil(t, st.Doc.Groups[0].PickedBy)
	p1, _ := st.Doc.PageByID("p1")
	assert.Equal(t, canvas.AlternatesID, p1.Canvas)

	// agent 改选 a：先落用户版本，再写 agent 的版本；pickedBy 是它的线程
	mustCall(t, e.s, c, OpUpdate, `{"id":"`+a.ID+`","baseVersion":1,"canvas":[`+
		`{"op":"set","id":"home","key":"picked","value":"a"},{"op":"set","id":"home","key":"pickedBy","value":"thr_x"}]}`)
	d := headDoc(t, e, a.ID)
	require.NotNil(t, d.Groups[0].PickedBy)
	assert.Equal(t, "thr_a", *d.Groups[0].PickedBy)
	p1, _ = d.PageByID("p1")
	assert.Equal(t, "c1", p1.Canvas)
	p2, _ := d.PageByID("p2")
	assert.Equal(t, canvas.AlternatesID, p2.Canvas)
}

// 没人在看（没有实时状态）时 agent 的画布操作直接写一个版本，copyOf 复制来源画框的文件。
func TestLiveAgentCanvasNoViewers(t *testing.T) {
	e, hub, _ := newLiveEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	a, err := e.s.Create(context.Background(), CreateParams{Title: "画布", ThreadID: "thr_a", Author: c.author(), Files: []FileChange{
		put("canvas.json", liveDoc), put("a.html", "<p>封面</p>"),
	}})
	require.NoError(t, err)
	r := mustCall(t, e.s, c, OpUpdate, `{"id":"`+a.ID+`","baseVersion":1,"canvas":[{"op":"add","object":{"type":"page","id":"p2","canvas":"c1","name":"副本"},"copyOf":"p1"}]}`)
	assert.EqualValues(t, 2, textObj(t, r)["version"])
	assert.Equal(t, "<p>封面</p>", readFile(t, e.s, a.ID, 0, "page-p2.html"))
	h, _ := header(t, mustCall(t, e.s, c, OpRead, `{"id":"`+a.ID+`"}`))
	_, has := h["viewing"]
	assert.False(t, has)
	assert.Nil(t, e.s.liveOf(a.ID))
	hub.none(t)
}

// 关停时把未落版本的操作写成版本。
func TestLiveFlushOnShutdown(t *testing.T) {
	e, hub, ft := newLiveEnv(t)
	a := liveCanvas(t, e)
	hub.watch(a.ID, "c1")
	applyOps(t, e, a.ID, "tab-a", 1, sticky("n1", "关停前"))
	e.s.FlushCanvases()
	assert.Equal(t, 2, headVersion(t, e, a.ID))
	assert.Nil(t, e.s.liveOf(a.ID))
	assert.Empty(t, ft.armed())
}

// 落版本暂时失败（这里用「在回收站里」制造）：已接受并广播的操作保留，按退避重新安排；恢复后重试即落成用户版本。
// 最后一个观看者离开时失败也一样保留，直到落成才回收实时状态。
func TestLiveFlushFailureKeepsPendingAndRetries(t *testing.T) {
	e, hub, ft := newLiveEnv(t)
	ctx := context.Background()
	a := liveCanvas(t, e)
	hub.watch(a.ID, "c1")
	applyOps(t, e, a.ID, "tab-a", 1, sticky("n1", "别丢"))
	hub.next(t)
	_, err := e.s.Delete(ctx, a.ID)
	require.NoError(t, err)

	require.Equal(t, 1, ft.fire())
	assert.Equal(t, 1, headVersion(t, e, a.ID))
	require.NotNil(t, e.s.liveOf(a.ID), "实时状态保留")
	assert.Equal(t, []time.Duration{2 * CanvasQuiet}, ft.armed(), "按退避安排重试")

	hub.watch(a.ID) // 最后一个观看者离开：仍落不成，不回收
	require.NotNil(t, e.s.liveOf(a.ID))
	assert.Equal(t, []time.Duration{4 * CanvasQuiet}, ft.armed())

	_, err = e.s.Undelete(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 1, ft.fire())
	assert.Equal(t, 2, headVersion(t, e, a.ID))
	assert.Empty(t, headAuthor(t, e, a.ID))
	assert.Equal(t, []string{"n1"}, noteIDs(headDoc(t, e, a.ID)))
	assert.Nil(t, e.s.liveOf(a.ID), "落成后没人在看就回收")
	assert.Empty(t, ft.armed())
}

// 只有 artifact 已被永久删除时才丢弃未落版本的操作。
func TestLiveErasedDropsPending(t *testing.T) {
	e, hub, ft := newLiveEnv(t)
	ctx := context.Background()
	a := liveCanvas(t, e)
	hub.watch(a.ID, "c1")
	applyOps(t, e, a.ID, "tab-a", 1, sticky("n1", "x"))
	_, err := e.s.Delete(ctx, a.ID)
	require.NoError(t, err)
	require.NoError(t, e.s.Erase(ctx, a.ID))
	require.Equal(t, 1, ft.fire())
	assert.Nil(t, e.s.liveOf(a.ID))
	assert.Empty(t, ft.armed())
	_, err = e.s.CanvasState(ctx, a.ID)
	assert.Equal(t, CodeNotFound, code(err))
}
