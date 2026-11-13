package threads

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 回归（f96ea0a）：新建线程时 orb 仍在 provisioning 各阶段、agent 尚未启动，Web 打开线程页发的 terminal_list 等只读查询
// 曾被立即拒绝（agent_unavailable），终端面板因此永远没初始化。现在它们排队、不唤醒，agent 就绪后下发并应答。
func TestPassiveQueryQueuedDuringProvisioning(t *testing.T) {
	for _, st := range []string{"requested", "spawning", "connecting", "setting_up", "resuming"} {
		t.Run(st, func(t *testing.T) {
			f := newFixture(t, Config{WakeOnFocus: true})
			orb := f.orb(st)
			th := f.thread(orb, StateProvisioning)

			var got json.RawMessage
			require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"terminal_list"}`), func(r json.RawMessage) { got = r }),
				"agent 会自行就绪：排队而不是拒绝")
			require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"get_state"}`), noReply))
			th.mu.Lock()
			assert.Len(t, th.queue, 2)
			th.mu.Unlock()

			// agent 启动并就绪：排队的查询按序下发
			a := fakeAgent(16)
			attach(th, a, apigen.AgentStatusReady)
			f.resumeOrb(th, orb, 100)
			types := f.answerAll(th, a)
			assert.Equal(t, []string{"terminal_list", "get_state", "get_state"}, types, "客户端的两条查询 + 控制面的 state 刷新")
			assert.True(t, strings.Contains(string(got), `"success":true`), string(got))

			f.s.Close()
			assert.Empty(t, f.orbs.wakes, "只读查询从不唤醒")
		})
	}
}

// 新线程的 orb 已就绪、startAgent 尚未取得线程锁的窗口里（scheduleStart 已把 agentStatus 标为 starting），查询同样排队。
func TestPassiveQueryQueuedWhileAgentScheduled(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateProvisioning)
	th.mu.Lock()
	th.agentStatus = apigen.AgentStatusStarting
	th.mu.Unlock()
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"terminal_list"}`), noReply))
	th.mu.Lock()
	assert.Len(t, th.queue, 1)
	th.mu.Unlock()
}

// 负向：agent 确定不会自行就绪时立即拒绝，且不排队、不唤醒。
func TestPassiveQueryRejectedWhenAgentWontStart(t *testing.T) {
	cases := map[string]func(f *fixture, th *thread){
		"orb frozen":   func(f *fixture, th *thread) { f.setOrbState(th.orbID, "frozen") },
		"orb stopped":  func(f *fixture, th *thread) { f.setOrbState(th.orbID, "stopped") },
		"orb failed":   func(f *fixture, th *thread) { f.setOrbState(th.orbID, "failed") },
		"orb deleting": func(f *fixture, th *thread) { f.setOrbState(th.orbID, "deleting") },
		"restart exhausted": func(_ *fixture, th *thread) {
			th.mu.Lock()
			th.agentStatus = apigen.AgentStatusExited
			th.live.RestartExhausted()
			th.mu.Unlock()
		},
		"thread failed": func(_ *fixture, th *thread) {
			th.mu.Lock()
			th.failed = true
			th.mu.Unlock()
		},
		"ready orb, nobody starting the agent": func(*fixture, *thread) {},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, Config{})
			orb := f.orb("ready")
			th := f.thread(orb, StateIdle)
			setup(f, th)
			err := f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"terminal_list"}`), noReply)
			var te *Error
			require.ErrorAs(t, err, &te)
			assert.Equal(t, "agent_unavailable", te.Code)
			th.mu.Lock()
			assert.Empty(t, th.queue)
			assert.Empty(t, th.pending)
			th.mu.Unlock()
			f.s.Close()
			assert.Empty(t, f.orbs.wakes)
		})
	}
}

// provisioning 期间排队的查询在 orb 随后失败时以 agent_unavailable 结束（不滞留在队列里）；用户命令留下等重试。
func TestQueuedPassiveDroppedWhenOrbFails(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("spawning")
	th := f.thread(orb, StateProvisioning)
	var got json.RawMessage
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"terminal_list"}`), func(r json.RawMessage) { got = r }))
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"set_thinking_level","level":"low"}`), noReply))

	f.setOrbState(orb, "failed")
	f.s.handleOrbEvent(f.ctx, OrbEvent{OrbID: orb, Kind: OrbEventState, From: "spawning", To: "failed", Seq: 50})
	assert.Contains(t, string(got), `"code":"agent_unavailable"`)
	assert.Contains(t, string(got), `"success":false`)
	th.mu.Lock()
	require.Len(t, th.queue, 1)
	typ, _, _ := frameHeader(th.queue[0])
	assert.Equal(t, "set_thinking_level", typ)
	th.mu.Unlock()
}

// 回归（B6）：orb 已 ready，但 agent 最终没能启动（agent.start 失败 → markFailed，或启动中的 agent 退出且重启耗尽）：
// provisioning 期间排队的只读查询须以 agent_unavailable 结束，不得滞留在队列 / pending 里阻止静止；用户命令留下等重试。
func TestQueuedPassiveDroppedWhenAgentNeverStarts(t *testing.T) {
	cases := map[string]func(f *fixture, th *thread){
		"agent.start failed": func(f *fixture, th *thread) { f.s.markFailed(f.ctx, th) },
		"restart exhausted": func(f *fixture, th *thread) {
			raw, _ := json.Marshal(apigen.LinkCtlAgentStatusParams{ThreadID: th.id, Status: "exited", RestartsLeft: new(0)})
			f.s.onAgentStatus(f.ctx, th.orbID, raw)
		},
	}
	for name, fail := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, Config{})
			orb := f.orb("spawning")
			th := f.thread(orb, StateProvisioning)
			var got json.RawMessage
			require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"terminal_list"}`), func(r json.RawMessage) { got = r }))
			require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"get_state"}`), noReply))

			// orb 就绪（不经 handleOrbEvent：它会异步 startAgent），agent 正在启动
			f.setOrbState(orb, "ready")
			f.s.applyOrbState(orb, "ready", 10)
			th.mu.Lock()
			th.agentStatus = apigen.AgentStatusStarting
			th.mu.Unlock()

			fail(f, th)
			assert.Contains(t, string(got), `"code":"agent_unavailable"`)
			assert.Contains(t, string(got), `"success":false`)
			th.mu.Lock()
			assert.Empty(t, th.queue)
			assert.Empty(t, th.pending)
			assert.True(t, th.quiescentLocked(), "已拒绝的查询不再阻止静止")
			th.mu.Unlock()
			f.s.Close()
			assert.Empty(t, f.orbs.wakes)
		})
	}
}

// B6 负向：agent 失败只清只读查询，排队的用户命令保留（等用户重试）。
func TestAgentStartFailureKeepsUserCommands(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("spawning")
	th := f.thread(orb, StateProvisioning)
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"terminal_list"}`), noReply))
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"set_thinking_level","level":"low"}`), noReply))
	f.setOrbState(orb, "ready")
	f.s.applyOrbState(orb, "ready", 10)
	f.s.markFailed(f.ctx, th)
	th.mu.Lock()
	require.Len(t, th.queue, 1)
	typ, _, _ := frameHeader(th.queue[0])
	assert.Equal(t, "set_thinking_level", typ)
	th.mu.Unlock()
}
