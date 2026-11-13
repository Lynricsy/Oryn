package threads

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// resumeOrb 模拟 orb 恢复到 ready、agent 在新流上就绪（不经 handleOrbEvent：它会异步 startAgent，干扰静止判定）。
func (f *fixture) resumeOrb(th *thread, orbID string, seq int64) {
	f.t.Helper()
	f.setOrbState(orbID, "ready")
	f.s.applyOrbState(orbID, "ready", seq)
	th.mu.Lock()
	f.s.onReadyLocked(f.ctx, th)
	th.mu.Unlock()
}

// answerAll 以成功应答回复 agent 流上的全部命令（get_state 回空闲状态），直到不再有新命令。
func (f *fixture) answerAll(th *thread, a *agentConn) []string {
	f.t.Helper()
	var types []string
	for {
		frames := sent(a)
		if len(frames) == 0 {
			return types
		}
		for _, fr := range frames {
			typ, _, ok := frameHeader(json.RawMessage(fr))
			require.True(f.t, ok, fr)
			types = append(types, typ)
			data := ""
			if typ == "get_state" {
				data = `{"isStreaming":false,"isSettled":true}`
			}
			f.respond(th, fr, true, data)
		}
	}
}

func noReply(json.RawMessage) {}

// 问题 1：冻结的 orb 上排队的非 prompt 命令（用户的 set_thinking_level、控制面的 get_state 刷新）下发并应答后，
// 线程必须重新静止。排队时清 settled 会让它永远为 false（不会有回合，也就没有 session_settled），orb 永不冻结。
func TestQueuedNonPromptCommandDoesNotBlockQuiesce(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("frozen")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusReady)

	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"set_thinking_level","level":"high"}`), noReply))
	f.s.refreshState(th) // 控制面内部的 get_state：同样排队
	assert.Empty(t, sent(a), "冻结中不写进 agent 流")
	select {
	case id := <-f.orbs.wakes:
		assert.Equal(t, orb, id, "用户命令唤醒 orb")
	case <-time.After(5 * time.Second):
		t.Fatal("用户命令没有唤醒 orb")
	}
	th.mu.Lock()
	assert.Len(t, th.queue, 2)
	assert.False(t, th.quiescentLocked(), "排队期间不静止")
	th.mu.Unlock()

	f.resumeOrb(th, orb, 100)
	types := f.answerAll(th, a)
	assert.Contains(t, types, "set_thinking_level")
	assert.Contains(t, types, "get_state")

	committed := false
	assert.True(t, f.s.TryQuiesce(orb, func() bool { committed = true; return true }), "应答之后线程静止，orb 可以再次冻结")
	assert.True(t, committed)
}

// 排队的 prompt 下发后、agent_start 到达前不得被判静止；回合结束（session_settled）后才静止。
func TestQueuedPromptNotQuiescentBeforeAgentStart(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("frozen")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusReady)

	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply))
	<-f.orbs.wakes
	f.resumeOrb(th, orb, 100)
	frames := sent(a)
	require.NotEmpty(t, frames)
	require.Contains(t, frames[0], `"prompt"`)
	f.respond(th, frames[0], true, `{"agentInvoked":true}`)
	for _, fr := range frames[1:] {
		f.respond(th, fr, true, `{"isStreaming":false,"isSettled":true}`)
	}
	assert.False(t, f.s.TryQuiesce(orb, func() bool { return true }), "prompt 已应答但 agent_start 未到：不静止")

	for _, fr := range []string{`{"type":"agent_start"}`, `{"type":"agent_end","isTerminal":true}`, `{"type":"session_settled"}`} {
		f.s.onFrame(f.ctx, th, orb, []byte(fr))
	}
	f.answerAll(th, a) // agent_end 触发的 get_state 刷新
	assert.True(t, f.s.TryQuiesce(orb, func() bool { return true }), "回合结束后静止")
}

// liveResetSink 统计 live.reset（web 客户端收到后会发 get_state 刷新）。
type liveResetSink struct {
	nopSink
	resets atomic.Int32
}

func (k *liveResetSink) LiveReset(string) { k.resets.Add(1) }

// 问题 2：手动停机先 shutdown，agent 退出 → live.reset → web 客户端自动发 get_state（以及各面板的只读查询）。
// 这些查询不是用户交互：orb 正在停机时直接拒绝，不排队、不唤醒——否则 orbs 在停机完成的同一刻把它唤醒。
// 真实的用户命令仍然唤醒。
func TestStopNotUndoneByClientRefresh(t *testing.T) {
	f := newFixture(t, Config{WakeOnFocus: true})
	sink := &liveResetSink{}
	f.s.SetSink(sink)
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	attach(th, fakeAgent(16), apigen.AgentStatusReady)

	f.setOrbState(orb, "stopping")
	f.s.handleOrbEvent(f.ctx, OrbEvent{OrbID: orb, Kind: OrbEventState, From: "ready", To: "stopping", Seq: 10})
	zero := 0
	raw, _ := json.Marshal(apigen.LinkCtlAgentStatusParams{ThreadID: th.id, Status: "exited", ExitCode: &zero, RestartsLeft: &zero})
	f.s.onAgentStatus(f.ctx, orb, raw)
	require.Equal(t, int32(1), sink.resets.Load(), "agent 退出广播 live.reset")

	for _, q := range []string{`{"type":"get_state"}`, `{"type":"get_available_commands"}`, `{"type":"terminal_list"}`, `{"type":"get_context_report"}`} {
		err := f.s.Command(f.ctx, th.id, json.RawMessage(q), noReply)
		var te *Error
		require.ErrorAs(t, err, &te, q)
		assert.Equal(t, "agent_unavailable", te.Code, q)
	}
	th.mu.Lock()
	assert.Empty(t, th.queue, "只读查询不排队")
	assert.Empty(t, th.pending)
	th.mu.Unlock()

	// 停机途中的用户命令仍然请求唤醒（orbs 在停机完成后执行）
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"wake up"}`), noReply))
	f.s.Close() // 等后台的唤醒全部执行完
	require.Len(t, f.orbs.wakes, 1, "只有用户命令请求了唤醒")
	assert.Equal(t, orb, <-f.orbs.wakes)
}

// orb 在恢复途中、agent 会自行就绪：只读查询排队（不唤醒），就绪后下发；agent 不在运行时立即拒绝。
func TestPassiveQueryQueuedWhileResuming(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("waking")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusStarting)

	var got json.RawMessage
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"get_state"}`), func(r json.RawMessage) { got = r }))
	assert.Empty(t, sent(a))
	th.mu.Lock()
	th.agentStatus = apigen.AgentStatusReady
	th.mu.Unlock()
	f.resumeOrb(th, orb, 100)
	types := f.answerAll(th, a)
	assert.Equal(t, []string{"get_state", "get_state"}, types, "客户端的查询与控制面的刷新")
	assert.True(t, strings.Contains(string(got), `"success":true`), string(got))

	// agent 已退出、不再重启：查询不会有人处理，立即拒绝
	th.mu.Lock()
	th.agentStatus = apigen.AgentStatusExited
	th.mu.Unlock()
	err := f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"get_session_stats"}`), noReply)
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "agent_unavailable", te.Code)

	f.s.Close()
	assert.Empty(t, f.orbs.wakes, "只读查询从不唤醒")
}

// 停机 / 冻结途中 agent 上报就绪：队列里只有只读查询时不唤醒，有用户命令时唤醒。
func TestReadyDuringStopWakesOnlyForUserCommands(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("stopping")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusReady)
	th.mu.Lock()
	th.queue = append(th.queue, []byte(`{"id":"q1","type":"get_state"}`))
	f.s.onReadyLocked(f.ctx, th)
	th.mu.Unlock()
	assert.Empty(t, sent(a))

	th.mu.Lock()
	th.queue = append(th.queue, []byte(`{"id":"q2","type":"set_thinking_level","level":"low"}`))
	f.s.onReadyLocked(f.ctx, th)
	th.mu.Unlock()
	f.s.Close()
	require.Len(t, f.orbs.wakes, 1, "只有用户命令请求了唤醒")
}
