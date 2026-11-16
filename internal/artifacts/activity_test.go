package artifacts

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// actHub 模拟观看登记，并记下发给观看者的在场消息。
type actHub struct {
	mu      sync.Mutex
	viewers map[string][]Viewer
	fns     []func(string)
	sent    []apigen.WsServerArtifactActivity
}

func (h *actHub) Viewers(id string) []Viewer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.viewers[id])
}

func (h *actHub) ToWatchers(_ string, msg any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, msg.(apigen.WsServerArtifactActivity))
}

func (h *actHub) OnWatchChange(fn func(string)) {
	h.mu.Lock()
	h.fns = append(h.fns, fn)
	h.mu.Unlock()
}

// watch 把 id 的观看者换成 vs 并通知观看者变化。
func (h *actHub) watch(id string, vs ...Viewer) {
	h.mu.Lock()
	h.viewers[id] = vs
	fns := slices.Clone(h.fns)
	h.mu.Unlock()
	for _, fn := range fns {
		fn(id)
	}
}

// take 取出并清空已发的消息。
func (h *actHub) take() []apigen.WsServerArtifactActivity {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.sent
	h.sent = nil
	return out
}

func newActEnv(t *testing.T) (*env, *actHub, *fakeTimers) {
	t.Helper()
	e := newEnv(t)
	ft := &fakeTimers{}
	e.s.act.after = ft.after
	h := &actHub{viewers: map[string][]Viewer{}}
	e.s.AttachActivityHub(h)
	return e, h, ft
}

func threadsOf(m apigen.WsServerArtifactActivity) map[string]apigen.ArtifactThreadActivity {
	out := map[string]apigen.ArtifactThreadActivity{}
	for _, a := range m.Threads {
		out[a.ThreadID] = a
	}
	return out
}

func TestActivityUpdateWriteAndTurnEnd(t *testing.T) {
	e, _, _ := newActEnv(t)
	mkThread(t, e.st, "T1", "")
	c := threadCaller("T1")
	id := mustCall(t, e.s, c, OpCreate, `{"title":"报告","html":"<h1>hi</h1>"}`).Details.(obj)["id"].(string)
	assert.Empty(t, e.s.Activity(id).Threads, "create 不记活动")

	mustCall(t, e.s, c, OpRead, `{"id":"`+id+`"}`)
	got := threadsOf(e.s.Activity(id))["T1"]
	assert.Equal(t, apigen.ArtifactDoingRead, got.Doing)
	require.NotNil(t, got.Focus)
	assert.Equal(t, "index.html", got.Focus.Path)

	mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":1,"files":[{"path":"style.css","content":"h1{}"}]}`)
	got = threadsOf(e.s.Activity(id))["T1"]
	assert.Equal(t, apigen.ArtifactDoingWrite, got.Doing)
	require.NotNil(t, got.Focus)
	assert.Equal(t, "style.css", got.Focus.Path)
	assert.Nil(t, got.Focus.Page)

	// edits 不带 path 时焦点是入口文件
	mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":2,"edits":[{"oldText":"hi","newText":"你好"}]}`)
	assert.Equal(t, "index.html", threadsOf(e.s.Activity(id))["T1"].Focus.Path)

	// 回合结束：回调只登记（不阻塞，也不清），后台处理后才清掉
	obs := e.s.TurnObserver()
	obs.StateChanged(threads.StateEvent{ThreadID: "T1", Prev: threads.StateWorking, State: threads.StateWorking})
	obs.StateChanged(threads.StateEvent{ThreadID: "T1", Prev: threads.StateWorking, State: threads.StateIdle})
	obs.StateChanged(threads.StateEvent{ThreadID: "T1", Prev: threads.StateWorking, State: threads.StateIdle})
	assert.Len(t, e.s.Activity(id).Threads, 1)
	e.s.drainTurns()
	assert.Empty(t, e.s.Activity(id).Threads)

	// 带标签投递的终态同样清掉
	mustCall(t, e.s, c, OpRead, `{"id":"`+id+`"}`)
	obs.TurnEnded(threads.TurnEvent{ThreadID: "T1", Tag: "x"})
	e.s.drainTurns()
	assert.Empty(t, e.s.Activity(id).Threads)
}

func TestActivityIgnoresRenameAndFailures(t *testing.T) {
	e, _, _ := newActEnv(t)
	mkThread(t, e.st, "T1", "")
	c := threadCaller("T1")
	id := mustCall(t, e.s, c, OpCreate, `{"title":"报告","html":"<h1>hi</h1>"}`).Details.(obj)["id"].(string)
	mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","title":"新名字"}`)
	assert.Empty(t, e.s.Activity(id).Threads, "只改名不是内容活动")
	_, err := agentCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":1,"edits":[{"oldText":"没有","newText":"x"}]}`)
	require.Error(t, err)
	assert.Empty(t, e.s.Activity(id).Threads, "失败的调用不记活动")
}

func TestActivityViewersAndSnapshot(t *testing.T) {
	e, h, ft := newActEnv(t)
	a, err := e.s.Create(t.Context(), CreateParams{Title: "页", Files: []FileChange{put("index.html", "<p>x</p>")}})
	require.NoError(t, err)

	// 没人在看：到点也不发
	h.watch(a.ID)
	assert.Equal(t, 1, ft.fire())
	assert.Empty(t, h.take())

	// 新观看者即使没有任何活动也会拿到一份快照
	h.watch(a.ID, Viewer{Conn: "c1", Page: "p1"}, Viewer{Conn: "c2"})
	assert.Equal(t, []string{"200ms"}, durations(ft.armed()))
	assert.Equal(t, 1, ft.fire())
	sent := h.take()
	require.Len(t, sent, 1)
	m := sent[0]
	assert.Equal(t, apigen.WsServerArtifactActivityTypeArtifactActivity, m.Type)
	assert.Equal(t, a.ID, m.ID)
	assert.Equal(t, 2, m.Viewers)
	require.Len(t, m.Clients, 2)
	assert.Equal(t, "c1", m.Clients[0].Client)
	require.NotNil(t, m.Clients[0].Page)
	assert.Equal(t, "p1", *m.Clients[0].Page)
	assert.Nil(t, m.Clients[1].Page)
	assert.NotNil(t, m.Threads)
	assert.Equal(t, 0, m.Visitors)

	// 访客数来自注入的函数
	e.s.SetActivityVisitors(func(string) int { return 3 })
	e.s.armActivity(a.ID)
	ft.fire()
	sent = h.take()
	require.Len(t, sent, 1)
	assert.Equal(t, 3, sent[0].Visitors)
}

func TestActivityDebounce(t *testing.T) {
	e, h, ft := newActEnv(t)
	mkThread(t, e.st, "T1", "")
	mkThread(t, e.st, "T2", "")
	c1, c2 := threadCaller("T1"), Caller{ThreadID: "T2", Label: "线程 T2", Scope: ScopeAll}
	id := mustCall(t, e.s, c1, OpCreate, `{"title":"报告","html":"<h1>hi</h1>"}`).Details.(obj)["id"].(string)
	h.watch(id, Viewer{Conn: "c1"})
	ft.fire()
	h.take()

	// 窗口内的连续变化（观看者加入、读、写、另一线程读）只发一条，内容是最后的状态
	h.watch(id, Viewer{Conn: "c1"}, Viewer{Conn: "c2"})
	mustCall(t, e.s, c1, OpRead, `{"id":"`+id+`"}`)
	mustCall(t, e.s, c1, OpUpdate, `{"id":"`+id+`","baseVersion":1,"html":"<h1>v2</h1>"}`)
	mustCall(t, e.s, c2, OpRead, `{"id":"`+id+`"}`)
	assert.Len(t, ft.armed(), 1)
	assert.Equal(t, 1, ft.fire())
	sent := h.take()
	require.Len(t, sent, 1)
	m := sent[0]
	assert.Equal(t, 2, m.Viewers)
	require.Len(t, m.Threads, 2)
	assert.Equal(t, "T1", m.Threads[0].ThreadID, "正在改的在前")
	assert.Equal(t, apigen.ArtifactDoingWrite, m.Threads[0].Doing)
	assert.Equal(t, apigen.ArtifactDoingRead, m.Threads[1].Doing)

	// 没有变化的重复活动不再安排发送
	mustCall(t, e.s, c2, OpRead, `{"id":"`+id+`"}`)
	assert.Empty(t, ft.armed())
}

func TestActivityCanvas(t *testing.T) {
	e, _, _ := newActEnv(t)
	mkThread(t, e.st, "T1", "")
	c := threadCaller("T1")
	a, err := e.s.Create(t.Context(), CreateParams{Title: "画布", ThreadID: "T1", Files: []FileChange{
		put("canvas.json", liveDoc), put("a.html", "<p>封面</p>"),
	}})
	require.NoError(t, err)
	focus := func() (apigen.ArtifactThreadActivityDoing, string, string) {
		got := threadsOf(e.s.Activity(a.ID))["T1"]
		require.NotNil(t, got.Focus)
		page := ""
		if got.Focus.Page != nil {
			page = *got.Focus.Page
		}
		return got.Doing, got.Focus.Path, page
	}

	mustCall(t, e.s, c, OpRead, `{"id":"`+a.ID+`","path":"a.html"}`)
	doing, path, page := focus()
	assert.Equal(t, apigen.ArtifactDoingRead, doing)
	assert.Equal(t, "a.html", path)
	assert.Equal(t, "p1", page)

	mustCall(t, e.s, c, OpUpdate, `{"id":"`+a.ID+`","baseVersion":1,"canvas":[{"op":"add","object":{"type":"page","id":"p2","canvas":"c1","name":"新页"}}]}`)
	doing, path, page = focus()
	assert.Equal(t, apigen.ArtifactDoingDraw, doing)
	assert.Equal(t, "p2", page)
	assert.Equal(t, "page-p2.html", path)

	mustCall(t, e.s, c, OpUpdate, `{"id":"`+a.ID+`","baseVersion":2,"canvas":[{"op":"set","id":"p1","key":"x","value":40}]}`)
	doing, path, page = focus()
	assert.Equal(t, apigen.ArtifactDoingMove, doing)
	assert.Equal(t, "p1", page)
	assert.Equal(t, "a.html", path)

	mustCall(t, e.s, c, OpUpdate, `{"id":"`+a.ID+`","baseVersion":3,"canvas":[{"op":"set","id":"p1","key":"name","value":"改名"}]}`)
	doing, path, page = focus()
	assert.Equal(t, apigen.ArtifactDoingWrite, doing)
	assert.Equal(t, "canvas.json", path)
	assert.Empty(t, page)

	// 改画框文件：焦点带画框 id
	mustCall(t, e.s, c, OpUpdate, `{"id":"`+a.ID+`","baseVersion":4,"files":[{"path":"page-p2.html","content":"<p>二</p>"}]}`)
	doing, path, page = focus()
	assert.Equal(t, apigen.ArtifactDoingWrite, doing)
	assert.Equal(t, "page-p2.html", path)
	assert.Equal(t, "p2", page)
}

func durations(ds []time.Duration) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.String()
	}
	return out
}
