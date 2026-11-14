package threads

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ctlOrbs 在 stubOrbs 之上提供一条经 net.Pipe 的 ctl（另一端扮演 orbd，记录收到的请求）与可配置的特性。
type ctlOrbs struct {
	*stubOrbs
	ctl      *link.Ctl
	features map[string]bool

	mu    sync.Mutex
	calls []ctlCall
	reqCh chan ctlCall
}

type ctlCall struct {
	method string
	params json.RawMessage
}

func newCtlOrbs(t *testing.T, base *stubOrbs) *ctlOrbs {
	o := &ctlOrbs{stubOrbs: base, features: map[string]bool{}, reqCh: make(chan ctlCall, 16)}
	a, b := net.Pipe()
	o.ctl = link.NewCtl(a, link.CtlHandlers{})
	orbd := link.NewCtl(b, link.CtlHandlers{OnRequest: func(_ context.Context, method string, params json.RawMessage) (any, error) {
		c := ctlCall{method: method, params: append(json.RawMessage(nil), params...)}
		o.mu.Lock()
		o.calls = append(o.calls, c)
		o.mu.Unlock()
		o.reqCh <- c
		switch method {
		case "agent.start":
			return apigen.LinkCtlAgentStartResult{Pid: 1}, nil
		case "agent.stop":
			return apigen.LinkCtlAgentStopResult{}, nil
		}
		return apigen.LinkCtlOkResult{Ok: true}, nil
	}})
	t.Cleanup(func() { _ = o.ctl.Close(); _ = orbd.Close() })
	return o
}

func (o *ctlOrbs) Ctl(string) (*link.Ctl, bool) { return o.ctl, true }

func (o *ctlOrbs) HasFeature(_, feature string) bool { return o.features[feature] }

// nextCall 等 orbd 一侧收到下一个 ctl 请求。
func (o *ctlOrbs) nextCall(t *testing.T) ctlCall {
	t.Helper()
	select {
	case c := <-o.reqCh:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("orbd 没有收到 ctl 请求")
		return ctlCall{}
	}
}

// fakeCoord 是测试用的 Coordinator。
type fakeCoord struct {
	orbID   string
	ensures int
	prof    AgentProfile
	calls   chan HostToolCall
	// block 非 nil 时 HostToolCall 等到 ctx 结束才返回（并在返回后关闭 done）。
	block bool
	done  chan struct{}
	res   HostToolResult
}

func (c *fakeCoord) EnsureOrb(context.Context) (string, error) {
	c.ensures++
	return c.orbID, nil
}

func (c *fakeCoord) AgentProfile(context.Context, string) (AgentProfile, error) { return c.prof, nil }

func (c *fakeCoord) HostToolCall(ctx context.Context, call HostToolCall) HostToolResult {
	c.calls <- call
	if c.block {
		<-ctx.Done()
		close(c.done)
		return HostToolResult{Text: "late"}
	}
	return c.res
}

func newFakeCoord() *fakeCoord {
	return &fakeCoord{
		prof:  AgentProfile{SystemPrompt: "You are a coordinator.\nBe brief.", Tools: []apigen.OmpFrame{{"name": "create_thread"}}},
		calls: make(chan HostToolCall, 4), done: make(chan struct{}),
		res: HostToolResult{Text: `{"ok":true}`, Details: map[string]any{"ok": true}},
	}
}

// coordThread 插入一个协调对话（kind）并加载。
func (f *fixture) coordThread(orbID, kind, state string) *thread {
	f.t.Helper()
	id := ids.Thread()
	var orb *string
	if orbID != "" {
		orb = &orbID
	}
	require.NoError(f.t, f.st.Q.CreateThread(f.ctx, db.CreateThreadParams{Kind: kind, ID: id, OrbID: orb, TitleSource: "auto", State: state, SessionDir: sessionDirOf(id), CreatedAt: 1, UpdatedAt: 1}))
	t, err := f.s.get(f.ctx, id)
	require.NoError(f.t, err)
	return t
}

// recvJSON 等 agent 流上的下一帧并解码。
func recvJSON(t *testing.T, a *agentConn) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(recvFrame(t, a)), &m))
	return m
}

func TestCoordinatorInitFrames(t *testing.T) {
	f := newFixture(t, Config{})
	th := f.coordThread(f.orb("ready"), KindAssistant, StateIdle)
	prof := &AgentProfile{Tools: []apigen.OmpFrame{{"name": "list_threads"}}}
	frames := initFrames(th, prof)
	assert.Equal(t, []string{"open_session", "set_event_filter", "set_host_tools"}, frameTypes(frames))
	assert.Equal(t, prof.Tools, frames[2]["tools"])
	work := f.thread(f.orb("ready"), StateIdle)
	assert.Contains(t, frameTypes(initFrames(work, nil)), "terminal_configure", "工作线程不变")
}

// 协调线程的 agent.start 带协调档与系统提示词；所在 orb 没有 coordinator 特性时线程进入 error、不发 agent.start。
func TestCoordinatorAgentStart(t *testing.T) {
	f := newFixture(t, Config{})
	o := newCtlOrbs(t, f.orbs)
	f.s.orbs = o
	coord := newFakeCoord()
	f.s.SetCoordinator(coord)
	th := f.coordThread(f.orb("ready"), KindSpace, StateProvisioning)

	o.features["coordinator"] = true
	f.s.startAgent(f.ctx, th.id)
	c := o.nextCall(t)
	require.Equal(t, "agent.start", c.method)
	var p apigen.LinkCtlAgentStartParams
	require.NoError(t, json.Unmarshal(c.params, &p))
	require.NotNil(t, p.Profile)
	assert.Equal(t, apigen.AgentProfileCoordinator, *p.Profile)
	require.NotNil(t, p.SystemPrompt)
	assert.Equal(t, coord.prof.SystemPrompt, *p.SystemPrompt)
	assert.Equal(t, []string{"open_session", "set_event_filter", "set_host_tools"}, frameTypes(p.InitFrames))
	assert.Empty(t, p.Env)

	o.features["coordinator"] = false
	other := f.coordThread(th.orbID, KindAssistant, StateProvisioning)
	f.s.startAgent(f.ctx, other.id)
	other.mu.Lock()
	assert.Equal(t, StateError, other.state, "协调宿主之外的 orb 不能跑协调线程")
	other.mu.Unlock()
	o.mu.Lock()
	assert.Len(t, o.calls, 1, "没有第二次 agent.start")
	o.mu.Unlock()
}

func TestCoordinatorCommandWhitelist(t *testing.T) {
	f := newFixture(t, Config{})
	th := f.coordThread(f.orb("ready"), KindAssistant, StateIdle)
	attach(th, fakeAgent(16), apigen.AgentStatusReady)
	for _, frame := range []string{
		`{"type":"bash","command":"id"}`, `{"type":"set_host_tools","tools":[]}`, `{"type":"terminal_open"}`,
		`{"type":"get_git_diff"}`, `{"type":"get_git_changes"}`, `{"type":"get_git_status"}`, `{"type":"get_ssh_hosts"}`,
		`{"type":"set_plan_mode","enabled":true}`, `{"type":"mcp_add"}`, `{"type":"terminal_list"}`,
		`{"type":"prompt","message":"x","orynAttachments":["att_1"]}`,
	} {
		err := f.s.Command(f.ctx, th.id, json.RawMessage(frame), noReply)
		var te *Error
		require.ErrorAs(t, err, &te, frame)
		assert.Equal(t, CodeCoordinatorRestricted, te.Code, frame)
	}
	for _, frame := range []string{`{"type":"prompt","message":"hi"}`, `{"type":"get_state"}`, `{"type":"set_model","provider":"a","modelId":"b"}`} {
		assert.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(frame), noReply), frame)
	}
	err := f.s.UIResponse(f.ctx, th.id, json.RawMessage(`{"type":"host_tool_result","id":"h-1","result":{}}`))
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeCoordinatorRestricted, te.Code, "客户端不能冒充宿主工具的结果")
	_, err = f.s.Fork(f.ctx, th.id, nil, "share")
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeCoordinatorRestricted, te.Code)
}

// host_tool_call → Coordinator 被调用、结果帧写回；host_tool_cancel 取消其 ctx 且不写结果。
func TestHostToolCallDispatch(t *testing.T) {
	f := newFixture(t, Config{})
	coord := newFakeCoord()
	f.s.SetCoordinator(coord)
	th := f.coordThread(f.orb("ready"), KindSpace, StateIdle)
	th.mu.Lock()
	th.spaceID = "spc_X"
	th.mu.Unlock()
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusReady)

	f.s.onFrame(f.ctx, th, th.orbID, []byte(`{"type":"host_tool_call","id":"h-1","toolCallId":"tc-1","toolName":"list_threads","arguments":{"scope":"all"}}`))
	call := <-coord.calls
	assert.Equal(t, "list_threads", call.Name)
	assert.Equal(t, "h-1", call.CallID)
	assert.Equal(t, "tc-1", call.ToolCallID)
	assert.Equal(t, KindSpace, call.Kind)
	assert.Equal(t, "spc_X", call.SpaceID)
	assert.True(t, call.OwnerTurn)
	assert.JSONEq(t, `{"scope":"all"}`, string(call.Args))
	res := recvJSON(t, a)
	assert.Equal(t, "host_tool_result", res["type"])
	assert.Equal(t, "h-1", res["id"])
	assert.Equal(t, false, res["isError"])
	result := res["result"].(map[string]any)
	assert.Equal(t, map[string]any{"ok": true}, result["details"])
	assert.Equal(t, []any{map[string]any{"type": "text", "text": `{"ok":true}`}}, result["content"])

	coord.block = true
	f.s.onFrame(f.ctx, th, th.orbID, []byte(`{"type":"host_tool_call","id":"h-2","toolCallId":"tc-2","toolName":"read_thread","arguments":{}}`))
	<-coord.calls
	f.s.onFrame(f.ctx, th, th.orbID, []byte(`{"type":"host_tool_cancel","id":"c-1","targetId":"h-2"}`))
	<-coord.done
	f.s.Close() // 等后台的工具调用返回
	assert.Empty(t, sent(a), "取消的调用不写回结果")
}

// 工作线程的 host_tool_call 不交给协调层。
func TestHostToolCallIgnoredForWorkThreads(t *testing.T) {
	f := newFixture(t, Config{})
	coord := newFakeCoord()
	f.s.SetCoordinator(coord)
	th := f.thread(f.orb("ready"), StateIdle)
	attach(th, fakeAgent(4), apigen.AgentStatusReady)
	f.s.onFrame(f.ctx, th, th.orbID, []byte(`{"type":"host_tool_call","id":"h-1","toolName":"x"}`))
	f.s.Close()
	assert.Empty(t, coord.calls)
}

// seenRecorder 记录 Observer.Seen（用户交互）。
type seenRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *seenRecorder) StateChanged(StateEvent)      {}
func (r *seenRecorder) InputRequested(InputEvent)    {}
func (r *seenRecorder) InputResolved(string, string) {}
func (r *seenRecorder) TurnEnded(TurnEvent)          {}
func (r *seenRecorder) EntriesAppended(string)       {}
func (r *seenRecorder) Seen(id string) {
	r.mu.Lock()
	r.seen = append(r.seen, id)
	r.mu.Unlock()
}

func (r *seenRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

// Deliver 以自动化身份追加消息：followUp 的 prompt、不清未读、不通知 Seen；ack 恰好一次；已下发后不能撤回；
// 之后这段活动里的宿主工具调用记为自动化回合，session_settled 后恢复。
func TestDeliverAutomation(t *testing.T) {
	f := newFixture(t, Config{})
	coord := newFakeCoord()
	f.s.SetCoordinator(coord)
	rec := &seenRecorder{}
	f.s.AddObserver(rec)
	th := f.coordThread(f.orb("ready"), KindAssistant, StateIdle)
	require.NoError(t, f.st.Q.SetThreadUnread(f.ctx, db.SetThreadUnreadParams{Unread: true, ID: th.id}))
	th.mu.Lock()
	th.unread = true
	th.mu.Unlock()
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusReady)

	acks := make(chan error, 4)
	cancel, err := f.s.Deliver(f.ctx, th.id, AutoMessage{Text: "tick"}, func(e error) { acks <- e })
	require.NoError(t, err)
	frame := recvFrame(t, a)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(frame), &m))
	assert.Equal(t, "prompt", m["type"])
	assert.Equal(t, "tick", m["message"])
	assert.Equal(t, "followUp", m["streamingBehavior"])
	assert.False(t, cancel(), "已写到 agent 流的消息不能撤回")
	f.respond(th, frame, true, "")
	require.Len(t, acks, 1)
	assert.NoError(t, <-acks)

	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	assert.True(t, row.Unread, "自动化消息不清未读")
	assert.Zero(t, rec.count(), "自动化消息不算用户交互")

	f.s.onFrame(f.ctx, th, th.orbID, []byte(`{"type":"host_tool_call","id":"h-1","toolName":"archive_thread","arguments":{}}`))
	assert.False(t, (<-coord.calls).OwnerTurn, "自动化消息开启的回合")
	_ = recvFrame(t, a)
	f.s.onFrame(f.ctx, th, th.orbID, []byte(`{"type":"session_settled"}`))
	f.s.onFrame(f.ctx, th, th.orbID, []byte(`{"type":"host_tool_call","id":"h-2","toolName":"archive_thread","arguments":{}}`))
	assert.True(t, (<-coord.calls).OwnerTurn, "静止后恢复为主人的回合")

	// 用户命令照常清未读、记交互
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply))
	assert.Equal(t, 1, rec.count())
}

// orb 冻结时 Deliver 排队并唤醒；cancel 撤回排队的消息且不调 ack。
func TestDeliverQueuedCancel(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("frozen")
	th := f.thread(orb, StateIdle)
	acked := false
	cancel, err := f.s.Deliver(f.ctx, th.id, AutoMessage{Text: "later"}, func(error) { acked = true })
	require.NoError(t, err)
	assert.Equal(t, orb, <-f.orbs.wakes)
	th.mu.Lock()
	assert.Len(t, th.queue, 1)
	th.mu.Unlock()
	assert.True(t, cancel())
	assert.False(t, cancel())
	th.mu.Lock()
	assert.Empty(t, th.queue)
	assert.Empty(t, th.pending)
	th.mu.Unlock()
	assert.False(t, acked)
}

func TestDeliverSteerAndArchived(t *testing.T) {
	f := newFixture(t, Config{})
	th := f.thread(f.orb("ready"), StateIdle)
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusReady)
	th.mu.Lock()
	th.state = StateWorking
	th.mu.Unlock()
	_, err := f.s.Deliver(f.ctx, th.id, AutoMessage{Text: "now", Steer: true}, nil)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(recvFrame(t, a)), &m))
	assert.Equal(t, "steer", m["type"])

	require.NoError(t, f.s.Archive(f.ctx, th.id))
	_, err = f.s.Deliver(f.ctx, th.id, AutoMessage{Text: "x"}, nil)
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "archived", te.Code)
	_, err = f.s.Deliver(f.ctx, "T-missing", AutoMessage{Text: "x"}, nil)
	require.ErrorIs(t, err, ErrNotFound)
}

// 协调 orb 被删除后，协调线程在下一条命令时经 EnsureOrb 挂到协调 orb 上。
func TestCoordinatorSelfHeal(t *testing.T) {
	f := newFixture(t, Config{})
	coord := newFakeCoord()
	coord.orbID = f.orb("frozen")
	f.s.SetCoordinator(coord)
	th := f.coordThread("", KindAssistant, StateIdle)
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply))
	assert.Equal(t, 1, coord.ensures)
	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	require.NotNil(t, row.OrbID)
	assert.Equal(t, coord.orbID, *row.OrbID)
	assert.Equal(t, coord.orbID, <-f.orbs.wakes, "排队并唤醒")
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"again"}`), noReply))
	assert.Equal(t, 1, coord.ensures, "已有 orb 时不再调用")
}

// 协调 orb 就绪时只启动需要的协调对话：尚未启动过的、有排队命令的；空闲的等到有命令或被唤醒。工作线程照旧全部启动。
func TestCoordinatorOnDemandStart(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	idle := f.coordThread(orb, KindAssistant, StateIdle)
	queued := f.coordThread(orb, KindAssistant, StateIdle)
	queued.mu.Lock()
	queued.queue = append(queued.queue, []byte(`{"type":"prompt","id":"oryn-x","message":"hi"}`))
	queued.mu.Unlock()
	fresh := f.coordThread(orb, KindSpace, StateProvisioning)
	work := f.thread(orb, StateIdle)
	f.s.handleOrbEvent(f.ctx, OrbEvent{OrbID: orb, Kind: OrbEventState, From: "resuming", To: "ready", Seq: 10})
	f.s.Close()
	status := func(th *thread) apigen.AgentStatus {
		th.mu.Lock()
		defer th.mu.Unlock()
		return th.agentStatus
	}
	assert.Equal(t, apigen.AgentStatusNone, status(idle))
	assert.Equal(t, apigen.AgentStatusStarting, status(queued))
	assert.Equal(t, apigen.AgentStatusStarting, status(fresh))
	assert.Equal(t, apigen.AgentStatusStarting, status(work))
}

// RefreshAgent：正忙时等 session_settled 再停；停下后的 exited 不算重启耗尽。
func TestRefreshAgentStopsAfterSettled(t *testing.T) {
	f := newFixture(t, Config{})
	o := newCtlOrbs(t, f.orbs)
	f.s.orbs = o
	th := f.coordThread(f.orb("ready"), KindSpace, StateIdle)
	attach(th, fakeAgent(16), apigen.AgentStatusReady)
	th.mu.Lock()
	th.settled = false
	th.mu.Unlock()
	f.s.RefreshAgent(f.ctx, th.id)
	o.mu.Lock()
	assert.Empty(t, o.calls, "正忙：先不停")
	o.mu.Unlock()
	f.s.onFrame(f.ctx, th, th.orbID, []byte(`{"type":"session_settled"}`))
	c := o.nextCall(t)
	assert.Equal(t, "agent.stop", c.method)

	raw, _ := json.Marshal(apigen.LinkCtlAgentStatusParams{ThreadID: th.id, Status: "exited"})
	f.s.onAgentStatus(f.ctx, th.orbID, raw)
	th.mu.Lock()
	defer th.mu.Unlock()
	assert.NotEqual(t, StateError, th.state, "主动停止不是故障")
	assert.False(t, th.stopping)
	assert.Equal(t, apigen.AgentStatusExited, th.agentStatus)
}

// 列表默认只有工作线程；kind=all 含协调对话；space 过滤；Thread.schedule 来自线程内定时。
func TestListKindSpaceAndSchedule(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	w := f.thread(orb, StateIdle)
	other := f.thread(orb, StateIdle)
	c := f.coordThread(orb, KindSpace, StateIdle)
	_, err := f.st.Q.SetThreadSpace(f.ctx, db.SetThreadSpaceParams{SpaceID: new("spc_A"), UpdatedAt: 2, ID: w.id})
	require.NoError(t, err)
	next := int64(1_900_000_000_000)
	require.NoError(t, f.st.Q.InsertSchedule(f.ctx, db.InsertScheduleParams{
		ID: ids.New("sch"), Mode: "thread", ThreadID: &w.id, Kind: "every", Spec: "1h0m0s", Timezone: "UTC", Prompt: "p",
		Enabled: true, NextRunAt: &next, CreatedBy: "user", CreatedAt: 1, UpdatedAt: 1, Overlap: "skip", KeepLast: 5,
	}))

	idsOf := func(list []apigen.Thread) []string {
		var out []string
		for _, t := range list {
			out = append(out, t.ID)
		}
		return out
	}
	list, err := f.s.List(f.ctx, ListFilter{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{w.id, other.id}, idsOf(list))
	list, err = f.s.List(f.ctx, ListFilter{Kind: "all"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{w.id, other.id, c.id}, idsOf(list))
	list, err = f.s.List(f.ctx, ListFilter{Kind: KindSpace})
	require.NoError(t, err)
	assert.Equal(t, []string{c.id}, idsOf(list))
	list, err = f.s.List(f.ctx, ListFilter{Space: "spc_A"})
	require.NoError(t, err)
	require.Equal(t, []string{w.id}, idsOf(list))
	require.NotNil(t, list[0].Schedule)
	assert.True(t, list[0].Schedule.Enabled)
	assert.Equal(t, next, *list[0].Schedule.NextRunAt)
	require.NotNil(t, list[0].SpaceID)

	got, err := f.s.Get(f.ctx, c.id)
	require.NoError(t, err)
	require.NotNil(t, got.Kind)
	assert.Equal(t, apigen.ThreadKindSpace, *got.Kind)
	got, err = f.s.Get(f.ctx, w.id)
	require.NoError(t, err)
	assert.Nil(t, got.Kind, "work 省略")
	require.NotNil(t, got.Schedule)
	idx, err := f.s.Index(f.ctx)
	require.NoError(t, err)
	assert.Contains(t, idsOf(idx), c.id, "index 含协调对话")
}

// space 行（需要一个 kind=space 的聊天线程）。
func (f *fixture) space(name string) (spaceID string, chat *thread) {
	f.t.Helper()
	chat = f.coordThread(f.orb("ready"), KindSpace, StateIdle)
	spaceID = ids.New("spc")
	require.NoError(f.t, f.st.Q.CreateSpace(f.ctx, db.CreateSpaceParams{ID: spaceID, Name: name, ThreadID: chat.id, CreatedAt: 1, UpdatedAt: 1}))
	return spaceID, chat
}

func TestPatchSpaceAndShipped(t *testing.T) {
	f := newFixture(t, Config{})
	sp, chat := f.space("m9")
	w := f.thread(f.orb("ready"), StateIdle)

	got, err := f.s.Patch(f.ctx, w.id, apigen.ThreadPatch{SpaceID: &sp})
	require.NoError(t, err)
	require.NotNil(t, got.SpaceID)
	assert.Equal(t, sp, *got.SpaceID)
	w.mu.Lock()
	assert.Equal(t, sp, w.spaceID)
	w.mu.Unlock()
	empty := ""
	got, err = f.s.Patch(f.ctx, w.id, apigen.ThreadPatch{SpaceID: &empty})
	require.NoError(t, err)
	assert.Nil(t, got.SpaceID)
	missing := "spc_01JZZZZZZZZZZZZZZZZZZZZZZZ"
	_, err = f.s.Patch(f.ctx, w.id, apigen.ThreadPatch{SpaceID: &missing})
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "space_not_found", te.Code)
	_, err = f.s.Patch(f.ctx, chat.id, apigen.ThreadPatch{SpaceID: &sp})
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeCoordinatorRestricted, te.Code)

	yes, no := true, false
	got, err = f.s.Patch(f.ctx, w.id, apigen.ThreadPatch{Shipped: &yes})
	require.NoError(t, err)
	require.NotNil(t, got.ShippedAt)
	require.NotNil(t, got.ArchivedAt, "标记已交付即归档")
	got, err = f.s.Patch(f.ctx, w.id, apigen.ThreadPatch{Shipped: &no})
	require.NoError(t, err)
	assert.Nil(t, got.ShippedAt)
	assert.NotNil(t, got.ArchivedAt, "清除标记不取消归档")

	err = f.s.Delete(f.ctx, chat.id)
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "space_thread", te.Code)
	require.NoError(t, f.s.DeleteSpaceThread(f.ctx, chat.id, nil))
	_, err = f.st.Q.GetSpace(f.ctx, sp)
	assert.Error(t, err, "spaces 行随聊天线程级联删除")
}

// 新建线程：不能挂到协调 orb、不能指定 @host；Space / 父线程 / 定时运行写入线程行。
func TestCreateM9Params(t *testing.T) {
	f := newFixture(t, Config{})
	coordOrb := ids.Orb()
	prov := mkOrbRow(f.ctx, f.st, "ready") // 确保 provider 已存在
	o, err := f.st.Q.GetOrb(f.ctx, prov)
	require.NoError(t, err)
	require.NoError(t, f.st.Q.CreateOrb(f.ctx, db.CreateOrbParams{ID: coordOrb, ProviderID: o.ProviderID, Type: "host", Image: "",
		State: "ready", StateChangedAt: 1, TokenHash: ids.Hash("oot_" + coordOrb), CreatedAt: 1, Coordinator: true}))
	var te *Error
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "x", OrbID: coordOrb})
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeCoordinatorRestricted, te.Code)
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "x", Provider: "@host"})
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeCoordinatorRestricted, te.Code)
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "x", SpaceID: "spc_01JZZZZZZZZZZZZZZZZZZZZZZZ"})
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "space_not_found", te.Code)

	sp, chat := f.space("m9")
	got, err := f.s.Create(f.ctx, CreateParams{Prompt: "x", SpaceID: sp, ParentThreadID: chat.id, ScheduleID: "sch_A", ScheduleRun: 3})
	require.NoError(t, err)
	require.NotNil(t, got.SpaceID)
	assert.Equal(t, sp, *got.SpaceID)
	require.NotNil(t, got.ParentThreadID)
	assert.Equal(t, chat.id, *got.ParentThreadID)
	require.NotNil(t, got.ScheduleRun)
	assert.Equal(t, apigen.ThreadScheduleRun{ScheduleID: "sch_A", Run: 3}, *got.ScheduleRun)
}
