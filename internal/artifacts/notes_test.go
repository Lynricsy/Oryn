package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// fakeDeliver 记下便签投递，应答由测试手动给。
type fakeDeliver struct {
	mu    sync.Mutex
	calls []deliverCall
	err   error // 非 nil 时同步失败
}

type deliverCall struct {
	thread string
	text   string
	ack    func(error)
}

func (f *fakeDeliver) deliver(_ context.Context, thread string, m threads.AutoMessage, ack func(error)) (func() bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, deliverCall{thread: thread, text: m.Text, ack: ack})
	return func() bool { return false }, nil
}

func (f *fakeDeliver) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeDeliver) last(t *testing.T) deliverCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.calls)
	return f.calls[len(f.calls)-1]
}

type notesEnv struct {
	*env
	timers *fakeTimers // 便签去抖
	live   *fakeTimers // 画布静默
	del    *fakeDeliver
	a      Artifact
}

// useNotes 给服务接上假时钟、同步执行与假投递。
func useNotes(s *Service, ft *fakeTimers, del *fakeDeliver) {
	s.notes.after = ft.after
	s.notes.spawn = func(f func()) { f() }
	s.SetNotesDelivery(del.deliver)
}

// newNotesEnv 建一个设计画布，创建线程 thr_a 在普通 orb 上、idle。
func newNotesEnv(t *testing.T) *notesEnv {
	t.Helper()
	e, _, lt := newLiveEnv(t)
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	mkOrb(t, e, "orb_a", "")
	mkOrbThread(t, e, "thr_a", "orb_a", "")
	ne := &notesEnv{env: e, timers: &fakeTimers{}, live: lt, del: &fakeDeliver{}}
	useNotes(e.s, ne.timers, ne.del)
	ne.a = designCanvas(t, e)
	return ne
}

func (ne *notesEnv) comment(t *testing.T, body string) CommentThread {
	t.Helper()
	loc := "src/frames/home.tsx:3:5"
	th, err := ne.s.Comment(context.Background(), ne.a.ID, Anchor{Version: ne.a.Version, Path: "dist/home.html", Text: "立即开始", Loc: &loc}, body)
	require.NoError(t, err)
	return th
}

func (ne *notesEnv) dirty(t *testing.T) bool {
	t.Helper()
	row, err := ne.st.Q.GetArtifactDesign(context.Background(), ne.a.ID)
	require.NoError(t, err)
	return row.DirtyAt != nil
}

func (ne *notesEnv) setState(t *testing.T, state string) {
	t.Helper()
	require.NoError(t, ne.st.Q.SetThreadState(context.Background(), db.SetThreadStateParams{State: state, StateChangedAt: 1, ID: "thr_a"}))
}

// idle 模拟线程从 working 转入 idle（先写库，再通知观察者，与线程服务的顺序一致）。
func (ne *notesEnv) idle(t *testing.T) {
	t.Helper()
	ne.setState(t, threads.StateIdle)
	ne.s.TurnObserver().StateChanged(threads.StateEvent{ThreadID: "thr_a", Prev: threads.StateWorking, State: threads.StateIdle})
}

func (ne *notesEnv) sentAt(t *testing.T, id string) time.Time {
	t.Helper()
	_, ts, err := ne.s.Threads(context.Background(), ne.a.ID)
	require.NoError(t, err)
	for _, th := range ts {
		if th.ID == id {
			return th.SentAt
		}
	}
	t.Fatalf("没有评论串 %s", id)
	return time.Time{}
}

// 新评论标脏并去抖 30 s：窗口内的第二条评论重置计时器，到点只投一次；应答前再到点不重复投；
// 应答成功后评论串记已发送、清脏；之后再标脏（没有新便签）不投。
func TestNotesDebounceDeliver(t *testing.T) {
	ne := newNotesEnv(t)
	c1 := ne.comment(t, "按钮太小")
	assert.True(t, ne.dirty(t))
	assert.Equal(t, []time.Duration{NotesDebounce}, ne.timers.armed())
	c2 := ne.comment(t, "标题换个颜色")
	assert.Equal(t, []time.Duration{NotesDebounce}, ne.timers.armed(), "第二条评论取代前一个计时器")
	assert.Zero(t, ne.del.n(), "去抖窗口内不投")

	require.Equal(t, 1, ne.timers.fire())
	require.Equal(t, 1, ne.del.n())
	call := ne.del.last(t)
	assert.Equal(t, "thr_a", call.thread)
	assert.True(t, strings.HasPrefix(call.text, "[Oryn 设计便签]"), call.text)
	assert.Contains(t, call.text, "design_notes")
	assert.Contains(t, call.text, "2 条评论")

	// 应答之前又标脏到点：不重复投
	ne.s.markNotesDirty(context.Background(), ne.a.ID)
	ne.timers.fire()
	assert.Equal(t, 1, ne.del.n())

	call.ack(nil)
	assert.False(t, ne.sentAt(t, c1.ID).IsZero())
	assert.False(t, ne.sentAt(t, c2.ID).IsZero())
	assert.False(t, ne.dirty(t))

	// 已投过的不再投
	ne.s.markNotesDirty(context.Background(), ne.a.ID)
	ne.timers.fire()
	assert.Equal(t, 1, ne.del.n())
	assert.False(t, ne.dirty(t), "没有待处理项时清脏")
}

// 线程忙时不投，等它转入 idle 再投；转入 idle 时没有等待的 artifact 什么也不做。
func TestNotesBusyWaitsForIdle(t *testing.T) {
	ne := newNotesEnv(t)
	ne.setState(t, threads.StateWorking)
	ne.comment(t, "间距不对")
	ne.timers.fire()
	assert.Zero(t, ne.del.n(), "忙时不投")
	assert.True(t, ne.dirty(t))

	ne.idle(t)
	require.Equal(t, 1, ne.del.n(), "转入 idle 后投递")
	ne.del.last(t).ack(nil)
	assert.False(t, ne.dirty(t))

	ne.idle(t)
	assert.Equal(t, 1, ne.del.n())
}

// 应答失败（或同步投递失败）不记已发送、保留脏标记，线程下一次转入 idle 时再投。
func TestNotesAckFailureRetries(t *testing.T) {
	ne := newNotesEnv(t)
	c := ne.comment(t, "logo 太大")
	ne.timers.fire()
	require.Equal(t, 1, ne.del.n())
	ne.del.last(t).ack(errors.New("agent_error"))
	assert.True(t, ne.sentAt(t, c.ID).IsZero(), "应答失败不记已发送")
	assert.True(t, ne.dirty(t))

	ne.idle(t)
	require.Equal(t, 2, ne.del.n(), "下一次 idle 再投")
	ne.del.last(t).ack(nil)
	assert.False(t, ne.sentAt(t, c.ID).IsZero())

	// 同步失败（如 queue_full）：同样保留，等下一次 idle
	ne.comment(t, "再改一下")
	ne.del.mu.Lock()
	ne.del.err = errors.New("queue_full")
	ne.del.mu.Unlock()
	ne.timers.fire()
	assert.True(t, ne.dirty(t))
	ne.del.mu.Lock()
	ne.del.err = nil
	ne.del.mu.Unlock()
	ne.idle(t)
	assert.Equal(t, 3, ne.del.n())
}

// auto_notes=0、目标线程归档、在 Runner 上或不存在时都不投（脏标记保留，供手动发送与之后打开开关）；
// 不是设计画布的评论不标脏。
func TestNotesNoDelivery(t *testing.T) {
	ne := newNotesEnv(t)
	ctx := context.Background()
	_, err := ne.s.SetDesignAutoNotes(ctx, ne.a.ID, false)
	require.NoError(t, err)
	ne.comment(t, "关了开关")
	ne.timers.fire()
	assert.Zero(t, ne.del.n(), "auto_notes=0 不投")
	assert.True(t, ne.dirty(t))
	_, err = ne.s.SetDesignAutoNotes(ctx, ne.a.ID, true)
	require.NoError(t, err)

	// Runner 线程不投
	mkOrb(t, ne.env, "orb_r", orbs.KindRunner)
	require.NoError(t, ne.st.Q.SetThreadOrb(ctx, db.SetThreadOrbParams{ID: "thr_a", OrbID: new("orb_r")}))
	ne.s.markNotesDirty(ctx, ne.a.ID)
	ne.timers.fire()
	assert.Zero(t, ne.del.n(), "Runner 线程不投")

	// 没有 orb 的线程不投
	require.NoError(t, ne.st.Q.SetThreadOrb(ctx, db.SetThreadOrbParams{ID: "thr_a"}))
	ne.s.markNotesDirty(ctx, ne.a.ID)
	ne.timers.fire()
	assert.Zero(t, ne.del.n(), "没有 orb 不投")

	// 没有创建线程的设计画布不投
	b, err := ne.s.Create(ctx, CreateParams{Title: "无主", Files: []FileChange{put("canvas.json", `{"format":2,"design":{"engine":1,"start":null},"canvases":[{"id":"c1","name":"页"}],"pages":[],"notes":[]}`)}})
	require.NoError(t, err)
	ne.s.markNotesDirty(ctx, b.ID)
	ne.timers.fire()
	assert.Zero(t, ne.del.n(), "无目标线程不投")

	// HTML 画布的评论不标脏
	h := liveCanvas(t, ne.env)
	_, err = ne.s.Comment(ctx, h.ID, Anchor{Version: 1, Path: "a.html"}, "普通画布")
	require.NoError(t, err)
	_, err = ne.st.Q.GetArtifactDesign(ctx, h.ID)
	assert.Error(t, err, "不是设计画布，不建行")
}

// 重启后按 dirty_at 恢复：补上剩余的去抖时长，到点投递。
func TestNotesRecoverDirty(t *testing.T) {
	ne := newNotesEnv(t)
	ne.comment(t, "重启前留的")
	ne.clk.add(10 * time.Second)

	s2, err := New(Config{DataDir: ne.dir, PublicURL: "http://localhost:7600", Now: ne.clk.now}, ne.st, ne.hub)
	require.NoError(t, err)
	ft, del := &fakeTimers{}, &fakeDeliver{}
	useNotes(s2, ft, del)
	s2.RecoverNotes(context.Background())
	armed := ft.armed()
	require.Len(t, armed, 1)
	assert.InDelta(t, float64(NotesDebounce-10*time.Second), float64(armed[0]), float64(time.Millisecond), "dirty_at 以毫秒存")
	ft.fire()
	require.Equal(t, 1, del.n())
	del.last(t).ack(nil)
	row, err := ne.st.Q.GetArtifactDesign(context.Background(), ne.a.ID)
	require.NoError(t, err)
	assert.Nil(t, row.DirtyAt)
}

// 用户画的 sticky：画布改动落成版本后标脏，投递成功后记进 delivered，不再重复投；agent 画的 sticky 不算。
func TestNotesStickyDelivered(t *testing.T) {
	ne := newNotesEnv(t)
	applyOps(t, ne.env, ne.a.ID, "tab", 1, sticky("s1", "这里换成插画"))
	assert.Equal(t, 1, ne.live.fire(), "静默后落版本")
	assert.True(t, ne.dirty(t))
	ne.timers.fire()
	require.Equal(t, 1, ne.del.n())
	assert.Contains(t, ne.del.last(t).text, "1 张便签")
	ne.del.last(t).ack(nil)
	row, err := ne.st.Q.GetArtifactDesign(context.Background(), ne.a.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `["s1"]`, row.Delivered)
	assert.Nil(t, row.DirtyAt)

	// 再改一次画布（不加 sticky）：没有新便签，不投
	applyOps(t, ne.env, ne.a.ID, "tab", 2, `{"op":"set","id":"s1","key":"text","value":"这里换成插画！"}`)
	ne.live.fire()
	ne.timers.fire()
	assert.Equal(t, 1, ne.del.n())

	// agent 画的 sticky 不是便签
	_, err = ne.s.Call(context.Background(), threadCaller("thr_a"), OpUpdate, json.RawMessage(`{"id":"`+ne.a.ID+`","baseVersion":`+itoa(headVersion(t, ne.env, ne.a.ID))+`,"canvas":[`+sticky("s2", "agent 的说明")+`]}`))
	require.NoError(t, err)
	np, ok, err := ne.s.loadNotes(context.Background(), ne.a.ID)
	require.NoError(t, err)
	require.True(t, ok)
	cs, ss := np.fresh()
	assert.Empty(t, cs)
	assert.Empty(t, ss)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// design_notes 列出评论串与用户 sticky（按画框分组、连续编号、附第一帧截图）；design_resolve_note 两种 ref。
func TestNotesDesignNotesAndResolve(t *testing.T) {
	ne := newNotesEnv(t)
	ctx := context.Background()
	r := &domRenderer{doms: map[string]string{"dist/home.html": "PNGDATA"}}
	withRenderer(ne.env, r)
	c := ne.comment(t, "按钮太小")
	applyOps(t, ne.env, ne.a.ID, "tab", 1, sticky("s1", "这里换成插画"),
		`{"op":"add","object":{"type":"note","id":"s9","kind":"sticky","canvas":"c1","x":5000,"y":5000,"text":"画框外"}}`)
	caller := threadCaller("thr_a")

	res, err := ne.s.agentDesignNotes(ctx, caller, json.RawMessage(`{"id":"`+ne.a.ID+`"}`))
	require.NoError(t, err)
	head, body, ok := strings.Cut(res.Text, "\n")
	require.True(t, ok)
	assert.Equal(t, notesHeader, head)
	var got struct {
		Frames []struct {
			Frame *string          `json:"frame"`
			Name  *string          `json:"name"`
			Notes []map[string]any `json:"notes"`
		} `json:"frames"`
		Screenshot *string `json:"screenshot"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	require.Len(t, got.Frames, 2)
	assert.Equal(t, "home", *got.Frames[0].Frame)
	require.Len(t, got.Frames[0].Notes, 2)
	n0, n1 := got.Frames[0].Notes[0], got.Frames[0].Notes[1]
	assert.Equal(t, c.ID, n0["ref"])
	assert.Equal(t, "comment", n0["kind"])
	assert.EqualValues(t, 1, n0["n"])
	assert.Equal(t, "src/frames/home.tsx:3:5", n0["element"].(map[string]any)["loc"])
	assert.Equal(t, "按钮太小", n0["comments"].([]any)[0].(map[string]any)["text"])
	assert.Equal(t, "s1", n1["ref"])
	assert.Equal(t, "sticky", n1["kind"])
	assert.EqualValues(t, 2, n1["n"])
	assert.Nil(t, got.Frames[1].Frame, "画框外的 sticky 单列一组")
	assert.Equal(t, "s9", got.Frames[1].Notes[0]["ref"])
	require.NotNil(t, res.Image)
	assert.Equal(t, "image/png", res.Image.MimeType)
	assert.Equal(t, "home", *got.Screenshot)

	// 渲染不可用：省略图片，不报错
	r.down = "自检失败"
	res, err = ne.s.agentDesignNotes(ctx, caller, json.RawMessage(`{"id":"`+ne.a.ID+`"}`))
	require.NoError(t, err)
	assert.Nil(t, res.Image)

	// 评论串：回复并解决
	_, err = ne.s.agentDesignResolveNote(ctx, caller, json.RawMessage(`{"id":"`+ne.a.ID+`","ref":"`+c.ID+`","reply":"已放大到 44px"}`))
	require.NoError(t, err)
	_, ts, err := ne.s.Threads(ctx, ne.a.ID)
	require.NoError(t, err)
	require.Len(t, ts, 1)
	assert.True(t, ts[0].Resolved())
	assert.Equal(t, "已放大到 44px", ts[0].Comments[1].Body)
	assert.Equal(t, "thr_a", ts[0].Comments[1].AuthorThreadID)

	// sticky：标完成并画一张 agent 署名的回复便签
	_, err = ne.s.agentDesignResolveNote(ctx, caller, json.RawMessage(`{"id":"`+ne.a.ID+`","ref":"s1","reply":"换成了插画"}`))
	require.NoError(t, err)
	doc := headDoc(t, ne.env, ne.a.ID)
	var s1 canvas.Note
	var replies []canvas.Note
	for _, n := range doc.Notes {
		if n.ID == "s1" {
			s1 = n
		} else if n.By != nil {
			replies = append(replies, n)
		}
	}
	assert.True(t, s1.Done)
	require.Len(t, replies, 1)
	assert.Equal(t, "换成了插画", replies[0].Text)
	assert.Equal(t, "thr_a", *replies[0].By)

	// 已完成、agent 的便签、不存在的 ref
	_, err = ne.s.agentDesignResolveNote(ctx, caller, json.RawMessage(`{"id":"`+ne.a.ID+`","ref":"s1","reply":"再来"}`))
	assert.Equal(t, CodeInvalid, code(err))
	_, err = ne.s.agentDesignResolveNote(ctx, caller, json.RawMessage(`{"id":"`+ne.a.ID+`","ref":"`+replies[0].ID+`","reply":"x"}`))
	assert.Equal(t, CodeNotFound, code(err))
	_, err = ne.s.agentDesignResolveNote(ctx, caller, json.RawMessage(`{"id":"`+ne.a.ID+`","ref":"nope","reply":"x"}`))
	assert.Equal(t, CodeNotFound, code(err))
	_, err = ne.s.agentDesignResolveNote(ctx, caller, json.RawMessage(`{"id":"`+ne.a.ID+`","ref":"s9","reply":"  "}`))
	assert.Equal(t, CodeInvalid, code(err))

	// 解决之后只剩画框外的 s9
	res, err = ne.s.agentDesignNotes(ctx, caller, json.RawMessage(`{"id":"`+ne.a.ID+`"}`))
	require.NoError(t, err)
	assert.EqualValues(t, 0, res.Details.(obj)["comments"])
	assert.EqualValues(t, 1, res.Details.(obj)["stickies"])

	// 不是设计画布
	h := liveCanvas(t, ne.env)
	_, err = ne.s.agentDesignNotes(ctx, Caller{Scope: ScopeAll}, json.RawMessage(`{"id":"`+h.ID+`"}`))
	assert.Equal(t, CodeInvalid, code(err))
}
