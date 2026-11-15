package threads

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// lastSent 返回 agent 流上最后一条类型为 typ 的命令帧（没有为空）。
func lastSent(frames []string, typ string) string {
	var out string
	for _, s := range frames {
		if got, _, _ := frameHeader(json.RawMessage(s)); got == typ {
			out = s
		}
	}
	return out
}

// asyncPending 让线程进入「回合已结束、后台任务仍在跑」：agent_end 后的 get_state 报 hasPendingAsyncWork。
func asyncPending(t *testing.T, f *fixture, th *thread, a *agentConn) {
	t.Helper()
	f.frame(th, `{"type":"agent_start"}`)
	f.frame(th, `{"type":"agent_end","messages":[],"isTerminal":true}`)
	getState := lastSent(sent(a), "get_state")
	require.NotEmpty(t, getState, "agent_end 后刷新 state")
	f.respond(th, getState, true, `{"isStreaming":false,"isSettled":false,"hasPendingAsyncWork":true}`)
	th.mu.Lock()
	defer th.mu.Unlock()
	require.Equal(t, StateWorking, f.s.status(th), "后台任务在跑时线程为 working")
}

// M12.1 走查问题 2：停掉后台任务后线程永远停在 working。Core 取消后不发 state 帧，只在这段活动静止时发
// session_settled；控制面须据此复位，并刷新 state 让快照跟上。
func TestCancelledAsyncJobSettlesToIdle(t *testing.T) {
	f := newFixture(t, Config{})
	th, a, _ := readyThread(f)
	asyncPending(t, f, th, a)

	f.frame(th, `{"type":"session_settled"}`)
	th.mu.Lock()
	assert.Equal(t, StateIdle, f.s.status(th), "session_settled 即复位后台任务标志")
	th.mu.Unlock()

	getState := lastSent(sent(a), "get_state")
	require.NotEmpty(t, getState, "session_settled 时补发 get_state")
	f.respond(th, getState, true, `{"isStreaming":false,"isSettled":true,"hasPendingAsyncWork":false}`)
	th.mu.Lock()
	defer th.mu.Unlock()
	assert.Equal(t, StateIdle, f.s.status(th))
	assert.JSONEq(t, `{"isStreaming":false,"isSettled":true,"hasPendingAsyncWork":false}`, string(th.live.State), "快照里的 state 已刷新")
	assert.True(t, th.quiescentLocked(), "刷新应答后线程静止")
}

// oryn_cancel_job 成功应答后主动刷新 state：取消在 Core 里同步生效，不等 session_settled 也能回到 idle。
func TestCancelJobResponseRefreshesState(t *testing.T) {
	f := newFixture(t, Config{})
	th, a, _ := readyThread(f)
	asyncPending(t, f, th, a)

	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"oryn_cancel_job","jobId":"bash-1"}`), nil))
	frames := sent(a)
	cancel := lastSent(frames, "oryn_cancel_job")
	require.NotEmpty(t, cancel)
	require.Empty(t, lastSent(frames, "get_state"), "应答前不刷新")
	f.respond(th, cancel, true, `{"cancelled":true}`)

	getState := lastSent(sent(a), "get_state")
	require.NotEmpty(t, getState, "取消成功后刷新 state")
	f.respond(th, getState, true, `{"isStreaming":false,"isSettled":true,"hasPendingAsyncWork":false}`)
	th.mu.Lock()
	defer th.mu.Unlock()
	assert.Equal(t, StateIdle, f.s.status(th))
}

// 没有后台任务时 session_settled 不多发查询（不给每个回合加一次 get_state），线程照常静止。
func TestSettledWithoutAsyncWorkDoesNotQuery(t *testing.T) {
	f := newFixture(t, Config{})
	th, a, _ := readyThread(f)
	f.frame(th, `{"type":"agent_start"}`)
	f.frame(th, `{"type":"agent_end","messages":[],"isTerminal":true}`)
	getState := lastSent(sent(a), "get_state")
	require.NotEmpty(t, getState)
	f.respond(th, getState, true, `{"isStreaming":false,"isSettled":true,"hasPendingAsyncWork":false}`)

	f.frame(th, `{"type":"session_settled"}`)
	assert.Empty(t, lastSent(sent(a), "get_state"))
	th.mu.Lock()
	defer th.mu.Unlock()
	assert.True(t, th.quiescentLocked())
}

// M12.1 走查问题 3：setWidget / setStatus 按 key 保留最新值进快照，空内容即删除；live.reset（agent 退出）时清空。
func TestSnapshotKeepsDisplayUI(t *testing.T) {
	f := newFixture(t, Config{})
	th, _, _ := readyThread(f)
	frames := []string{
		`{"type":"extension_ui_request","id":"1","method":"setWidget","widgetKey":"omfg","widgetLines":["rule v1"]}`,
		`{"type":"extension_ui_request","id":"2","method":"setStatus","statusKey":"omfg","statusText":"drafting"}`,
		`{"type":"extension_ui_request","id":"3","method":"setStatus","statusKey":"gone","statusText":"x"}`,
		`{"type":"extension_ui_request","id":"4","method":"setWidget","widgetKey":"omfg","widgetLines":["rule v2"],"widgetPlacement":"belowEditor"}`,
		`{"type":"extension_ui_request","id":"5","method":"setStatus","statusKey":"gone"}`,
		`{"type":"extension_ui_request","id":"6","method":"setWidget","widgetKey":"tmp","widgetLines":["t"]}`,
		`{"type":"extension_ui_request","id":"7","method":"setWidget","widgetKey":"tmp","widgetLines":[]}`,
		`{"type":"extension_ui_request","id":"8","method":"notify","message":"hi"}`,
	}
	for _, fr := range frames {
		f.frame(th, fr)
	}
	snapshot := func() []string {
		var snap Snapshot
		require.NoError(t, f.s.Subscribe(f.ctx, th.id, func(s Snapshot) { snap = s }))
		assert.Empty(t, snap.Agent.PendingUI, "展示类帧不算待答")
		out := []string{}
		for _, fr := range snap.Agent.DisplayUI {
			out = append(out, string(fr))
		}
		return out
	}
	assert.Equal(t, []string{frames[1], frames[3]}, snapshot(), "每个 key 最新的非空帧，按到达顺序")
	th.mu.Lock()
	assert.Equal(t, StateIdle, f.s.status(th), "展示类帧不影响线程状态")
	th.mu.Unlock()

	raw, _ := json.Marshal(apigen.LinkCtlAgentStatusParams{ThreadID: th.id, Status: "exited"})
	f.s.onAgentStatus(f.ctx, th.orbID, raw)
	assert.Empty(t, snapshot(), "agent 退出（live.reset）后清空")
}
