package threads

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// recObserver 记录 Observer 回调。
type recObserver struct {
	mu       sync.Mutex
	states   []StateEvent
	inputs   []InputEvent
	resolved []string
	seen     []string
}

func (r *recObserver) StateChanged(ev StateEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, ev)
}

func (r *recObserver) InputRequested(ev InputEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, ev)
}

func (r *recObserver) InputResolved(_, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolved = append(r.resolved, id)
}

func (r *recObserver) Seen(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, id)
}

func (r *recObserver) snapshot() recObserver {
	r.mu.Lock()
	defer r.mu.Unlock()
	return recObserver{states: append([]StateEvent(nil), r.states...), inputs: append([]InputEvent(nil), r.inputs...),
		resolved: append([]string(nil), r.resolved...), seen: append([]string(nil), r.seen...)}
}

func (f *fixture) frame(th *thread, s string) {
	f.t.Helper()
	f.s.onFrame(f.ctx, th, th.orbID, []byte(s))
}

func readyThread(f *fixture) (*thread, *agentConn, *recObserver) {
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(64)
	attach(th, a, apigen.AgentStatusReady)
	o := &recObserver{}
	f.s.AddObserver(o)
	return th, a, o
}

func TestObserverStateChanged(t *testing.T) {
	f := newFixture(t, Config{})
	th, _, o := readyThread(f)

	f.frame(th, `{"type":"agent_start"}`)
	f.frame(th, `{"type":"agent_end","isTerminal":true}`)
	got := o.snapshot().states
	require.Len(t, got, 2)
	assert.Equal(t, StateEvent{ThreadID: th.id, Prev: StateIdle, State: StateWorking}, got[0])
	assert.Equal(t, StateEvent{ThreadID: th.id, Prev: StateWorking, State: StateIdle}, got[1])

	// 有连接在看：Focused 为真
	f.s.Focus(f.ctx, "c1", th.id)
	f.frame(th, `{"type":"agent_start"}`)
	f.frame(th, `{"type":"agent_end","isTerminal":true}`)
	got = o.snapshot().states
	require.Len(t, got, 4)
	assert.True(t, got[3].Focused)
	f.s.Focus(f.ctx, "c1", "")
	assert.False(t, f.s.Focused(th.id))

	// 没有变化时不回调
	f.frame(th, `{"type":"agent_end","isTerminal":true}`)
	assert.Len(t, o.snapshot().states, 4)
}

// fork 产生的 provisioning→idle 同样上报（过滤在通知侧）。
func TestObserverProvisioningToIdle(t *testing.T) {
	f := newFixture(t, Config{})
	o := &recObserver{}
	f.s.AddObserver(o)
	orb := f.orb("ready")
	th := f.thread(orb, StateProvisioning)
	th.mu.Lock()
	th.agent, th.agentStatus = fakeAgent(16), apigen.AgentStatusReady
	f.s.onReadyLocked(f.ctx, th)
	f.s.recompute(f.ctx, th)
	th.mu.Unlock()
	got := o.snapshot().states
	require.NotEmpty(t, got)
	assert.Equal(t, StateProvisioning, got[0].Prev)
	assert.Equal(t, StateIdle, got[0].State)
}

func TestObserverErrorReason(t *testing.T) {
	f := newFixture(t, Config{})
	th, _, o := readyThread(f)
	f.frame(th, `{"type":"agent_start"}`)
	f.frame(th, `{"type":"prompt_result","status":"error"}`)
	f.frame(th, `{"type":"agent_end","isTerminal":true}`)
	got := o.snapshot().states
	last := got[len(got)-1]
	assert.Equal(t, StateError, last.State)
	assert.Equal(t, "", last.Reason, "orb 未失败时没有原因")
}

func TestObserverInputRequestedAndResolved(t *testing.T) {
	f := newFixture(t, Config{})
	th, _, o := readyThread(f)

	ask := func(id string) string {
		return `{"type":"extension_ui_request","id":"` + id + `","method":"select","title":"pick","options":["a","b"]}`
	}
	f.frame(th, ask("u1"))
	f.frame(th, ask("u2"))
	f.frame(th, ask("u1")) // 重复到达（orbd 重放）不再回调
	f.frame(th, `{"type":"extension_ui_request","id":"n1","method":"notify","message":"hi"}`)
	in := o.snapshot().inputs
	require.Len(t, in, 2)
	assert.Equal(t, "u1", in[0].RequestID)
	assert.Equal(t, "u2", in[1].RequestID)
	assert.False(t, in[0].Plan)
	assert.JSONEq(t, ask("u1"), string(in[0].Frame))

	// 作答
	require.NoError(t, f.s.UIResponse(f.ctx, th.id, json.RawMessage(`{"type":"extension_ui_response","id":"u1","value":"a"}`)))
	// cancel
	f.frame(th, `{"type":"extension_ui_request","id":"c","method":"cancel","targetId":"u2"}`)
	snap := o.snapshot()
	assert.Equal(t, []string{"u1", "u2"}, snap.resolved)
	assert.Contains(t, snap.seen, th.id, "ui.response 记为 Seen")

	// 计划提案：第一次回调，重复提案不回调；plan_approval 成功 → InputResolved("plan")
	f.frame(th, `{"type":"plan_proposal","title":"p"}`)
	f.frame(th, `{"type":"plan_proposal","title":"p2"}`)
	in = o.snapshot().inputs
	require.Len(t, in, 3)
	assert.True(t, in[2].Plan)
	assert.Equal(t, PlanRequestID, in[2].RequestID)
	var cmd string
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"plan_approval","approved":true}`), noReply))
	for _, s := range sent(th.agent) {
		if typ, _, _ := frameHeader(json.RawMessage(s)); typ == "plan_approval" {
			cmd = s
		}
	}
	require.NotEmpty(t, cmd)
	f.respond(th, cmd, true, "")
	assert.Equal(t, []string{"u1", "u2", PlanRequestID}, o.snapshot().resolved)

	// agent 退出：缓冲重置，仍待答的请求逐个 InputResolved
	f.frame(th, ask("u3"))
	f.frame(th, `{"type":"plan_proposal","title":"again"}`)
	raw, _ := json.Marshal(apigen.LinkCtlAgentStatusParams{ThreadID: th.id, Status: "exited"})
	f.s.onAgentStatus(f.ctx, th.orbID, raw)
	res := o.snapshot().resolved
	assert.ElementsMatch(t, []string{"u1", "u2", PlanRequestID, "u3", PlanRequestID}, res)
}

func TestObserverSeenOnFocusAndCommand(t *testing.T) {
	f := newFixture(t, Config{})
	th, _, o := readyThread(f)
	f.s.Focus(f.ctx, "c1", th.id)
	f.s.Focus(f.ctx, "c1", "")
	assert.Equal(t, []string{th.id}, o.snapshot().seen, "focus 空串不算")
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply))
	assert.Equal(t, []string{th.id, th.id}, o.snapshot().seen)
	// 只读查询不是用户交互
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"get_state"}`), noReply))
	assert.Len(t, o.snapshot().seen, 2)
}

func TestThreadPublicShares(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	got, err := f.s.Get(f.ctx, th.id)
	require.NoError(t, err)
	assert.Nil(t, got.PublicShares)

	ins := func() string {
		id := ids.New("shr")
		require.NoError(t, f.st.Q.InsertThreadShare(f.ctx, db.InsertThreadShareParams{ID: id, ThreadID: th.id, Slug: id, Mode: "live", CreatedAt: 1}))
		return id
	}
	a := ins()
	ins()
	got, err = f.s.Get(f.ctx, th.id)
	require.NoError(t, err)
	require.NotNil(t, got.PublicShares)
	assert.Equal(t, 2, *got.PublicShares)
	list, err := f.s.List(f.ctx, ListFilter{})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NotNil(t, list[0].PublicShares)
	assert.Equal(t, 2, *list[0].PublicShares)

	// Publish 推送带最新计数的 index.patch
	sink := &orbRecSink{}
	f.s.SetSink(sink)
	_, err = f.st.Q.RevokeThreadShare(f.ctx, db.RevokeThreadShareParams{Now: 2, ID: a})
	require.NoError(t, err)
	f.s.Publish(f.ctx, th.id)
	sink.mu.Lock()
	require.Len(t, sink.index, 1)
	assert.Equal(t, 1, *sink.index[0].PublicShares)
	sink.mu.Unlock()
}

// 回归（B5）：agent.gap 只是帧不连续，同一进程会在新流上重放仍待答的交互。gap 不得立即对待答请求发 InputResolved
// （否则通知被标为已处理、重放后又生成一条）；gap 后第一份真实 get_state 应答到达时（重放必然先于它），
// 只对没有重现的请求（断连期间已作答 / 取消）发 InputResolved，重现的仍可作答。
func TestObserverAgentGapDefersInputResolution(t *testing.T) {
	f := newFixture(t, Config{})
	th, a, o := readyThread(f)
	ask := func(id string) string {
		return `{"type":"extension_ui_request","id":"` + id + `","method":"select","title":"pick","options":["a","b"]}`
	}
	f.frame(th, ask("u1"))
	f.frame(th, ask("u2"))
	f.frame(th, `{"type":"plan_proposal","title":"p"}`)
	f.frame(th, `{"type":"plan_proposal","title":"p2"}`)
	sent(a)

	raw, _ := json.Marshal(apigen.LinkCtlAgentGapParams{ThreadID: th.id})
	f.s.onAgentGap(f.ctx, th.orbID, raw)
	assert.Empty(t, o.snapshot().resolved, "gap 不解决待答请求")

	// 新流上重放：u1 与计划提案仍待答；u2 在断连期间已作答
	f.frame(th, ask("u1"))
	f.frame(th, `{"type":"plan_proposal","title":"p2"}`)
	assert.Empty(t, o.snapshot().resolved)

	// gap 后的 state 刷新应答：核对差集
	var getState string
	for _, s := range sent(a) {
		if typ, _, _ := frameHeader(json.RawMessage(s)); typ == "get_state" {
			getState = s
		}
	}
	require.NotEmpty(t, getState, "gap 后刷新 state")
	f.respond(th, getState, true, `{"isStreaming":false,"isSettled":true}`)
	assert.Equal(t, []string{"u2"}, o.snapshot().resolved, "只解决没有重现的请求")

	// 重现的请求仍可作答
	require.NoError(t, f.s.UIResponse(f.ctx, th.id, json.RawMessage(`{"type":"extension_ui_response","id":"u1","value":"a"}`)))
	assert.Equal(t, []string{"u2", "u1"}, o.snapshot().resolved)
}

// B5：gap 后尚未核对时 agent 退出（实时缓冲真正丢弃），挂起的请求一并解决；gap / 退出时合成的失败应答不触发核对。
func TestObserverAgentGapThenExitResolvesSuspended(t *testing.T) {
	f := newFixture(t, Config{})
	th, a, o := readyThread(f)
	f.frame(th, `{"type":"extension_ui_request","id":"u1","method":"confirm","title":"ok?"}`)
	sent(a)
	raw, _ := json.Marshal(apigen.LinkCtlAgentGapParams{ThreadID: th.id})
	f.s.onAgentGap(f.ctx, th.orbID, raw)
	// 第二次 gap：上一次的 get_state 以合成失败结束，不得据空缓冲解决 u1
	f.s.onAgentGap(f.ctx, th.orbID, raw)
	assert.Empty(t, o.snapshot().resolved)

	st, _ := json.Marshal(apigen.LinkCtlAgentStatusParams{ThreadID: th.id, Status: "exited"})
	f.s.onAgentStatus(f.ctx, th.orbID, st)
	assert.Equal(t, []string{"u1"}, o.snapshot().resolved)
}
