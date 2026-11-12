package threads

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- 测试替身 ----

type entriesCall struct {
	seqs    []int64
	lastSeq int64
}

// recSink 记录线程服务推送的事件。
type recSink struct {
	nopSink
	mu           sync.Mutex
	entries      []entriesCall
	uiResolved   []string
	resets       []int64
	planResolved []string
}

func (r *recSink) Entries(_ string, es []Entry, _ *string, last int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := entriesCall{lastSeq: last}
	for _, e := range es {
		c.seqs = append(c.seqs, e.Seq)
	}
	r.entries = append(r.entries, c)
}

func (r *recSink) UIResolved(_, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.uiResolved = append(r.uiResolved, id)
}

func (r *recSink) TranscriptReset(_ string, rev int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resets = append(r.resets, rev)
}

func (r *recSink) PlanResolved(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.planResolved = append(r.planResolved, id)
}

// stubOrbs 记录 Wake，Create 直接写一行 requested 的 orb。
type stubOrbs struct {
	nopOrbs
	st    *store.Store
	wakes chan string
}

func (o *stubOrbs) Wake(_ context.Context, id string) error {
	o.wakes <- id
	return nil
}

func (o *stubOrbs) Create(ctx context.Context, _ OrbCreate) (db.Orb, error) {
	id := mkOrbRow(ctx, o.st, "requested")
	return o.st.Q.GetOrb(ctx, id)
}

type fixture struct {
	t    *testing.T
	ctx  context.Context
	st   *store.Store
	orbs *stubOrbs
	sink *recSink
	s    *Service
}

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	o := &stubOrbs{st: st, wakes: make(chan string, 16)}
	cfg.DataDir = dir
	s := New(cfg, st, o)
	t.Cleanup(s.Close)
	sink := &recSink{}
	s.SetSink(sink)
	return &fixture{t: t, ctx: ctx, st: st, orbs: o, sink: sink, s: s}
}

var provOnce sync.Map // store → provider id

func mkOrbRow(ctx context.Context, st *store.Store, state string) string {
	prov, ok := provOnce.Load(st)
	if !ok {
		id := ids.New("prov")
		if err := st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: id, Name: id, TokenHash: ids.Hash(id), CreatedAt: 1}); err != nil {
			panic(err)
		}
		prov, _ = provOnce.LoadOrStore(st, id)
	}
	id := ids.Orb()
	if err := st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: id, ProviderID: prov.(string), Type: "medium", Cpus: 4, MemoryMb: 8192, Image: "test",
		State: state, StateChangedAt: 1, TokenHash: ids.Hash("oot_" + id), CreatedAt: 1}); err != nil {
		panic(err)
	}
	return id
}

func (f *fixture) orb(state string) string { return mkOrbRow(f.ctx, f.st, state) }

func (f *fixture) setOrbState(id, state string) {
	require.NoError(f.t, f.st.Q.SetOrbState(f.ctx, db.SetOrbStateParams{State: state, StateChangedAt: time.Now().UnixMilli(), ID: id}))
}

// thread 在 orbID 上插入一个状态为 state 的线程并加载。
func (f *fixture) thread(orbID, state string) *thread {
	f.t.Helper()
	id := ids.Thread()
	require.NoError(f.t, f.st.Q.CreateThread(f.ctx, db.CreateThreadParams{ID: id, OrbID: &orbID, TitleSource: "auto", State: state, SessionDir: sessionDirOf(id), CreatedAt: 1, UpdatedAt: 1}))
	t, err := f.s.get(f.ctx, id)
	require.NoError(f.t, err)
	return t
}

// fakeAgent 是不连 link 的 agent 流：send 只写进 out（容量 n；0 = 必定写不进）。
func fakeAgent(n int) *agentConn {
	return &agentConn{out: make(chan []byte, n), done: make(chan struct{})}
}

func attach(t *thread, a *agentConn, status apigen.AgentStatus) {
	t.mu.Lock()
	t.agent, t.agentStatus = a, status
	t.live.AgentReady()
	t.mu.Unlock()
}

func sent(a *agentConn) []string {
	var out []string
	for {
		select {
		case b := <-a.out:
			out = append(out, string(b))
		default:
			return out
		}
	}
}

// respond 以 omp 应答回复 agent 流上的命令帧。
func (f *fixture) respond(t *thread, frame string, success bool, data string) {
	f.t.Helper()
	env, err := omprpc.ParseEnvelope([]byte(frame))
	require.NoError(f.t, err)
	var h struct {
		Type string `json:"type"`
	}
	require.NoError(f.t, json.Unmarshal([]byte(frame), &h))
	if data == "" {
		data = "null"
	}
	resp := fmt.Sprintf(`{"type":"response","id":%q,"command":%q,"success":%t,"data":%s}`, env.ID, h.Type, success, data)
	renv, err := omprpc.ParseEnvelope([]byte(resp))
	require.NoError(f.t, err)
	f.s.onResponse(f.ctx, t, renv, json.RawMessage(resp))
}

func (f *fixture) appendLines(t *thread, orbID, file string, base int64, lines ...string) (apigen.LinkCtlEntriesAppendResult, int64) {
	f.t.Helper()
	end := base
	for _, l := range lines {
		end += int64(len(l) + 1)
	}
	raw, _ := json.Marshal(apigen.LinkCtlEntriesAppendParams{ThreadID: t.id, File: file, BaseOffset: base, EndOffset: end, Lines: lines})
	res, err := f.s.onAppend(f.ctx, orbID, raw)
	require.NoError(f.t, err)
	return res.(apigen.LinkCtlEntriesAppendResult), end
}

func msgLine(id, parent, role string, ts int) string {
	p := "null"
	if parent != "" {
		p = fmt.Sprintf("%q", parent)
	}
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%s,"message":{"role":%q,"content":"x","timestamp":%d}}`, id, p, role, ts)
}

// ---- CP7 / L1 ----

func TestRouteCommandStrictKeys(t *testing.T) {
	cases := map[string]string{
		`{"type":"prompt","message":"hi"}`:            RoutePass,
		`{"type":"login","Type":"prompt"}`:            RouteInvalidFrame, // Go 读出 prompt、omp 执行 login
		`{"TYPE":"prompt","type":"login"}`:            RouteInvalidFrame,
		`{"type":"prompt","ID":"x"}`:                  RouteInvalidFrame, // 注入 id 后 Go 与 omp 看到的 id 不同
		`{"type":"open_session"}`:                     RouteManaged,
		`{"type":"fork_from","entryId":"a"}`:          RouteUseOrynFork,
		`{"type":"extension_ui_response","id":"u-1"}`: RouteInvalidFrame, // 侧通道应答只走 ui.response
		`{"type":""}`:                                 RouteInvalidFrame,
		`[1]`:                                         RouteInvalidFrame,
	}
	for frame, want := range cases {
		got, _ := RouteCommand(json.RawMessage(frame))
		assert.Equal(t, want, got, frame)
	}
	for _, ok := range []string{`{"type":"extension_ui_response","id":"u","value":"a"}`, `{"type":"host_tool_result","id":"h","result":{}}`, `{"type":"host_uri_result","id":"h"}`} {
		_, _, valid := RouteSideChannel(json.RawMessage(ok))
		assert.True(t, valid, ok)
	}
	for _, bad := range []string{`{"type":"open_session","id":"u"}`, `{"type":"login","id":"x"}`, `{"type":"extension_ui_response"}`, `{"type":"extension_ui_response","id":"u","Type":"login"}`, `{"type":"extension_ui_response","id":7}`} {
		_, _, valid := RouteSideChannel(json.RawMessage(bad))
		assert.False(t, valid, bad)
	}
}

// ui.response 不能夹带普通命令绕过 Command 的托管检查（CP7）。
func TestUIResponseRejectsOrdinaryCommands(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	for _, frame := range []string{`{"type":"open_session","id":"x","sessionDir":"/tmp"}`, `{"type":"login","id":"y"}`, `{"type":"fork_from","id":"z","entryId":"a"}`} {
		err := f.s.UIResponse(f.ctx, th.id, json.RawMessage(frame))
		var te *Error
		require.ErrorAs(t, err, &te, frame)
		assert.Equal(t, RouteInvalidFrame, te.Code)
	}
	assert.Empty(t, sent(a), "没有任何帧到达 agent")
	th.mu.Lock()
	assert.Empty(t, th.queue)
	th.mu.Unlock()
}

// ---- CP13 / OD1 ----

func pendingAsk(th *thread, id string) {
	th.mu.Lock()
	th.live.ApplyFrame(json.RawMessage(fmt.Sprintf(`{"type":"extension_ui_request","id":%q,"method":"select","options":["a"]}`, id)))
	th.mu.Unlock()
}

// 应答没能交给 agent 时请求保持待答、不广播 ui.resolved，重试可以成功（CP13）。
func TestUIResponseRetainedUntilDelivered(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	attach(th, fakeAgent(0), apigen.AgentStatusReady)
	pendingAsk(th, "ui-1")
	resp := json.RawMessage(`{"type":"extension_ui_response","id":"ui-1","value":"a"}`)

	err := f.s.UIResponse(f.ctx, th.id, resp)
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "agent_busy", te.Code)
	th.mu.Lock()
	assert.True(t, th.live.HasUI("ui-1"), "写不进 agent 流：请求仍待答")
	th.mu.Unlock()
	assert.Empty(t, f.sink.uiResolved)

	a := fakeAgent(4)
	attach(th, a, apigen.AgentStatusReady)
	require.NoError(t, f.s.UIResponse(f.ctx, th.id, resp))
	assert.Equal(t, []string{string(resp)}, sent(a))
	assert.Equal(t, []string{"ui-1"}, f.sink.uiResolved)
	require.ErrorIs(t, f.s.UIResponse(f.ctx, th.id, resp), ErrUIResolved)
}

// agent 流已建立但仍在启动（扩展的 session_start 正等着对话框）：UI 应答直接写入流，而不是排队等 ready（OD1）。
func TestUIResponseReachesStartingAgent(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(4)
	attach(th, a, apigen.AgentStatusStarting)
	pendingAsk(th, "ui-boot")
	resp := `{"type":"extension_ui_response","id":"ui-boot","value":"a"}`
	require.NoError(t, f.s.UIResponse(f.ctx, th.id, json.RawMessage(resp)))
	assert.Equal(t, []string{resp}, sent(a))
	th.mu.Lock()
	assert.Empty(t, th.queue)
	th.mu.Unlock()
}

// ---- CP6 ----

// 叶子与条目、游标同一事务提交：事务提交之后的任何失败（这里让之后的写全部因 ctx 取消而失败）都不会让库里的叶子落后。
func TestIngestCommitsLeafWithEntries(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	ctx, cancel := context.WithCancel(f.ctx)
	f.s.afterIngestCommit = cancel
	raw, _ := json.Marshal(apigen.LinkCtlEntriesAppendParams{ThreadID: th.id, File: "main.jsonl", BaseOffset: 0, EndOffset: 10,
		Lines: []string{msgLine("u1", "", "user", 1), msgLine("a1", "u1", "assistant", 2)}})
	res, err := f.s.onAppend(ctx, orb, raw)
	require.NoError(t, err)
	assert.True(t, res.(apigen.LinkCtlEntriesAppendResult).Accepted)
	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	require.NotNil(t, row.LeafEntryID)
	assert.Equal(t, "a1", *row.LeafEntryID)
	require.NotNil(t, row.LastMessageAt)
	cur, err := f.st.Q.GetEntryCursor(f.ctx, db.GetEntryCursorParams{ThreadID: th.id, File: "main.jsonl"})
	require.NoError(t, err)
	assert.Equal(t, int64(10), cur.ByteOffset)
}

// ---- CP2（实时入库路径） ----

// 一次入库超过一批时，每批的 lastSeq 只到该批覆盖的 seq。
func TestLiveEntriesBatchCursor(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	lines := make([]string, 450)
	for i := range lines {
		parent := ""
		if i > 0 {
			parent = fmt.Sprintf("e%03d", i-1)
		}
		lines[i] = msgLine(fmt.Sprintf("e%03d", i), parent, "user", i+1)
	}
	res, _ := f.appendLines(th, orb, "main.jsonl", 0, lines...)
	require.True(t, res.Accepted)
	require.Len(t, f.sink.entries, 3)
	for _, c := range f.sink.entries {
		assert.Equal(t, c.seqs[len(c.seqs)-1], c.lastSeq, "lastSeq = 本批覆盖到的最大 seq")
	}
	assert.Equal(t, []int64{200, 400, 450}, []int64{f.sink.entries[0].lastSeq, f.sink.entries[1].lastSeq, f.sink.entries[2].lastSeq})
}

// ---- CP11 ----

// 会话文件重写：reset_done 删除旧行后转录版本加一并广播，叶子随被删条目改指剩余的最新主会话条目。
func TestResetDoneBumpsTranscriptRev(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	u1, a1 := msgLine("u1", "", "user", 1), msgLine("a1", "u1", "assistant", 2)
	f.appendLines(th, orb, "main.jsonl", 0, u1, a1)
	file := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}
	f.s.onReset(f.ctx, orb, file(apigen.LinkCtlEntriesResetParams{ThreadID: th.id, File: "main.jsonl"}))
	res, _ := f.appendLines(th, orb, "main.jsonl", 0, u1)
	require.True(t, res.Accepted)
	f.s.onResetDone(f.ctx, orb, file(apigen.LinkCtlEntriesResetDoneParams{ThreadID: th.id, File: "main.jsonl"}))

	assert.Equal(t, []int64{1}, f.sink.resets)
	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	assert.Equal(t, int64(1), row.TranscriptRev)
	require.NotNil(t, row.LeafEntryID)
	assert.Equal(t, "u1", *row.LeafEntryID, "叶子 a1 被删除后改指 u1")
	var snap Snapshot
	require.NoError(t, f.s.Subscribe(f.ctx, th.id, func(s Snapshot) { snap = s }))
	assert.Equal(t, int64(1), snap.TranscriptRev)
	rows, err := f.st.Q.ListEntriesAfter(f.ctx, db.ListEntriesAfterParams{ThreadID: th.id, AfterSeq: 0, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "u1", rows[0].EntryID)
}

// ---- CP8 ----

// 冻结闸门：commit 在持有线程锁时执行；commit 之后到达的命令看到的是冻结中的 orb——排队并唤醒，不写入即将暂停的流。
func TestTryQuiesceSerializesAdmission(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	require.Equal(t, "ready", f.s.orbState(orb))

	ok := f.s.TryQuiesce(orb, func() bool {
		assert.False(t, th.mu.TryLock(), "commit 期间线程锁被闸门持有，命令无法准入")
		f.setOrbState(orb, "freezing") // orbs 的 ready → freezing CAS
		return true
	})
	require.True(t, ok)

	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"late"}`), func(json.RawMessage) {}))
	assert.Empty(t, sent(a), "命令不得写进即将冻结的 agent 流")
	th.mu.Lock()
	assert.Len(t, th.queue, 1)
	th.mu.Unlock()
	select {
	case id := <-f.orbs.wakes:
		assert.Equal(t, orb, id)
	case <-time.After(5 * time.Second):
		t.Fatal("排队的命令没有唤醒 orb")
	}

	// 有待发命令时不静止：commit 不被调用
	called := false
	assert.False(t, f.s.TryQuiesce(orb, func() bool { called = true; return true }))
	assert.False(t, called)
}

// ---- CP9 ----

func TestOrbEventSeqDropsStale(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	f.thread(orb, StateIdle)
	ev := func(to string, seq int64) OrbEvent {
		return OrbEvent{OrbID: orb, Kind: OrbEventState, To: to, Seq: seq}
	}
	f.s.handleOrbEvent(f.ctx, ev("ready", 5))
	assert.Equal(t, "ready", f.s.orbState(orb))
	f.s.handleOrbEvent(f.ctx, ev("frozen", 3)) // 更早提交、迟到的事件
	assert.Equal(t, "ready", f.s.orbState(orb), "过时的状态事件被丢弃")
	f.s.handleOrbEvent(f.ctx, ev("freezing", 6))
	assert.Equal(t, "freezing", f.s.orbState(orb))
}

// ---- CP10 ----

// 重启时线程正忙：加载后保持 working 且不算静止，直到 get_state 给出权威值。
func TestReloadedBusyThreadRestoredFromGetState(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		working    bool
	}{
		{"still-streaming", `{"isStreaming":true,"isSettled":false}`, true},
		{"finished-meanwhile", `{"isStreaming":false,"isSettled":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, Config{})
			orb := f.orb("ready")
			th := f.thread(orb, StateWorking)
			a := fakeAgent(8)
			attach(th, a, apigen.AgentStatusReady)
			th.mu.Lock()
			assert.Equal(t, StateWorking, f.s.status(th), "重启前的 agent_start 不会重放：按库里的状态保持 working")
			assert.False(t, th.quiescentLocked(), "get_state 之前不静止")
			f.s.refreshStateLocked(th)
			th.mu.Unlock()
			frames := sent(a)
			require.Len(t, frames, 1)
			require.True(t, strings.Contains(frames[0], `"get_state"`))
			f.respond(th, frames[0], true, tc.data)
			th.mu.Lock()
			defer th.mu.Unlock()
			if tc.working {
				assert.Equal(t, StateWorking, f.s.status(th))
				assert.False(t, th.quiescentLocked())
			} else {
				assert.Equal(t, StateIdle, f.s.status(th))
				assert.True(t, th.quiescentLocked())
			}
		})
	}
}

// ---- CP14 ----

// 冻结的 orb 上 agent 流与 ready 状态仍在：focus 也要唤醒。
func TestFocusWakesFrozenOrb(t *testing.T) {
	f := newFixture(t, Config{WakeOnFocus: true})
	orb := f.orb("frozen")
	th := f.thread(orb, StateIdle)
	attach(th, fakeAgent(8), apigen.AgentStatusReady)
	f.s.Focus(f.ctx, "conn-1", th.id)
	select {
	case id := <-f.orbs.wakes:
		assert.Equal(t, orb, id)
	case <-time.After(5 * time.Second):
		t.Fatal("focus 没有唤醒冻结的 orb")
	}
}

// ---- W7 ----

func TestPlanApprovalBroadcastsResolved(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	th.mu.Lock()
	th.live.ApplyFrame(json.RawMessage(`{"type":"plan_proposal","plan":"x"}`))
	th.mu.Unlock()
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"plan_approval","approved":false,"feedback":"no"}`), func(json.RawMessage) {}))
	frames := sent(a)
	require.Len(t, frames, 1)
	f.respond(th, frames[0], false, "")
	assert.Empty(t, f.sink.planResolved, "失败的批准不清除提案")
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"plan_approval","approved":true,"option":"execute"}`), func(json.RawMessage) {}))
	frames = sent(a)
	require.Len(t, frames, 1)
	f.respond(th, frames[0], true, "")
	assert.Equal(t, []string{th.id}, f.sink.planResolved)
	th.mu.Lock()
	assert.Nil(t, th.live.PlanProposal)
	th.mu.Unlock()
}

// ---- CP5 ----

// 已受理的首条提示词（含图片）与线程行同事务落库；重启后重新排队；agent 应答后才清除。
func TestStartupPromptSurvivesRestart(t *testing.T) {
	f := newFixture(t, Config{})
	img := apigen.ImageContent{Type: "image", Data: "aGVsbG8=", MimeType: "image/png"}
	out, err := f.s.Create(f.ctx, CreateParams{Prompt: "durable hello", Images: []apigen.ImageContent{img}, Title: "named"})
	require.NoError(t, err)
	row, err := f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	require.NotEmpty(t, row.StartupFrames, "应答之前首批命令已落库")

	// 模拟控制面重启：同一数据库上的新服务
	f.s.Close()
	s2 := New(Config{DataDir: t.TempDir()}, f.st, f.orbs)
	t.Cleanup(s2.Close)
	th, err := s2.get(f.ctx, out.ID)
	require.NoError(t, err)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	th.mu.Lock()
	require.Len(t, th.queue, 2)
	s2.onReadyLocked(f.ctx, th)
	th.mu.Unlock()
	frames := sent(a)
	require.GreaterOrEqual(t, len(frames), 2)
	assert.Contains(t, frames[0], `"durable hello"`)
	assert.Contains(t, frames[0], `"aGVsbG8="`)
	assert.Contains(t, frames[1], `"set_session_name"`)

	f2 := &fixture{t: t, ctx: f.ctx, st: f.st, s: s2}
	f2.respond(th, frames[0], true, `{"agentInvoked":true}`)
	row, err = f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	assert.Empty(t, row.StartupFrames, "agent 应答 prompt 后清除")
}

// 重启前 prompt 已被 agent 接收（主会话里已有消息）：不重发，免得执行两遍。
func TestStartupPromptNotRepeatedWhenDelivered(t *testing.T) {
	f := newFixture(t, Config{})
	out, err := f.s.Create(f.ctx, CreateParams{Prompt: "once"})
	require.NoError(t, err)
	row, err := f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	f.s.Close()
	_, err = f.st.Q.UpsertEntry(f.ctx, db.UpsertEntryParams{ThreadID: out.ID, File: "s.jsonl", EntryID: "u1", Type: "message", Payload: []byte(msgLine("u1", "", "user", 1))})
	require.NoError(t, err)
	s2 := New(Config{DataDir: t.TempDir()}, f.st, f.orbs)
	t.Cleanup(s2.Close)
	th, err := s2.get(f.ctx, row.ID)
	require.NoError(t, err)
	th.mu.Lock()
	assert.Empty(t, th.queue)
	th.mu.Unlock()
	row, err = f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	assert.Empty(t, row.StartupFrames)
}

// 冻结已提交时就绪的 agent 不收命令（排队并请求唤醒）；orb 回到 ready 时排队的命令随状态事件下发，
// 即使 agent 不会再次上报就绪（orbd 的 agent.start 对在跑的 agent 幂等、不重发 ready）。
func TestQueuedCommandsFlushedWhenOrbResumes(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("freezing")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(8)
	th.mu.Lock()
	th.queue = append(th.queue, []byte(`{"id":"q1","type":"prompt","message":"queued"}`))
	th.mu.Unlock()
	attach(th, a, apigen.AgentStatusReady)
	th.mu.Lock()
	f.s.onReadyLocked(f.ctx, th)
	th.mu.Unlock()
	assert.Empty(t, sent(a), "冻结中不下发")
	select {
	case <-f.orbs.wakes:
	case <-time.After(5 * time.Second):
		t.Fatal("排队的命令没有请求唤醒")
	}

	f.setOrbState(orb, "ready")
	f.s.handleOrbEvent(f.ctx, OrbEvent{OrbID: orb, Kind: OrbEventState, From: "resuming", To: "ready", Seq: 9})
	frames := sent(a)
	require.NotEmpty(t, frames)
	assert.Contains(t, frames[0], `"queued"`)
}

// orbd 为写进已退出进程的命令合成的「未送达」失败不是 agent 的确认：首条提示词重新排队、持久化记录保留，
// 重启后的进程就绪时再发；真正的应答之后才清除。
func TestStartupPromptRequeuedWhenUndelivered(t *testing.T) {
	f := newFixture(t, Config{})
	out, err := f.s.Create(f.ctx, CreateParams{Prompt: "bounce"})
	require.NoError(t, err)
	th, err := f.s.get(f.ctx, out.ID)
	require.NoError(t, err)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	th.mu.Lock()
	f.s.onReadyLocked(f.ctx, th)
	th.mu.Unlock()
	frames := sent(a)
	require.NotEmpty(t, frames)
	require.Contains(t, frames[0], `"bounce"`)

	env, err := omprpc.ParseEnvelope([]byte(frames[0]))
	require.NoError(t, err)
	resp := fmt.Sprintf(`{"type":"response","id":%q,"command":"prompt","success":false,"error":%q}`, env.ID, undeliveredError)
	renv, err := omprpc.ParseEnvelope([]byte(resp))
	require.NoError(t, err)
	f.s.onResponse(f.ctx, th, renv, json.RawMessage(resp))
	assert.Empty(t, sent(a), "不立即重写到已退出的进程")
	th.mu.Lock()
	require.Len(t, th.queue, 1)
	assert.Contains(t, string(th.queue[0]), `"bounce"`)
	th.mu.Unlock()
	row, err := f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, row.StartupFrames, "未送达：持久化记录保留")

	// 重启后的进程就绪
	th.mu.Lock()
	f.s.onReadyLocked(f.ctx, th)
	th.mu.Unlock()
	frames = sent(a)
	require.NotEmpty(t, frames)
	require.Contains(t, frames[0], `"bounce"`)
	f.respond(th, frames[0], true, `{"agentInvoked":true}`)
	row, err = f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	assert.Empty(t, row.StartupFrames)
}
