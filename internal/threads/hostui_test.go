package threads

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

const cfmFrame = `{"type":"extension_ui_request","id":"cfm_1","method":"confirm","title":"Delete thread?","message":"x","oryn":{"action":"delete_thread","targetLabel":"x"}}`

// hostUIThread 是一个就绪的助手对话（带 fakeAgent）、记录事件的 Observer 与收应答的 fakeCoord。
func hostUIThread(t *testing.T) (*fixture, *thread, *agentConn, *recObserver, *fakeCoord) {
	f := newFixture(t, Config{})
	coord := newFakeCoord()
	coord.answers = make(chan uiAnswer, 4)
	f.s.SetCoordinator(coord)
	th := f.coordThread(f.orb("ready"), KindAssistant, StateIdle)
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusReady)
	o := &recObserver{}
	f.s.AddObserver(o)
	return f, th, a, o, coord
}

// RequestUI：线程变 awaiting_input、触发 InputRequested；重复调用幂等；只接受 cfm_ 开头的 confirm 帧。
func TestRequestUIAwaitsInput(t *testing.T) {
	f, th, _, o, _ := hostUIThread(t)
	require.NoError(t, f.s.RequestUI(f.ctx, th.id, json.RawMessage(cfmFrame)))
	require.NoError(t, f.s.RequestUI(f.ctx, th.id, json.RawMessage(cfmFrame)), "同一 id 幂等")
	got, err := f.s.Get(f.ctx, th.id)
	require.NoError(t, err)
	assert.Equal(t, StateAwaitingInput, string(got.State))
	inputs := o.snapshot().inputs
	require.Len(t, inputs, 1)
	assert.Equal(t, "cfm_1", inputs[0].RequestID)
	assert.JSONEq(t, cfmFrame, string(inputs[0].Frame))

	for _, bad := range []string{
		`{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"t"}`,
		`{"type":"extension_ui_request","id":"cfm_2","method":"select","title":"t","options":["a","b"]}`,
		`{"type":"host_tool_call","id":"cfm_3"}`,
		`not json`,
	} {
		err := f.s.RequestUI(f.ctx, th.id, json.RawMessage(bad))
		var te *Error
		require.ErrorAs(t, err, &te, bad)
		assert.Equal(t, "invalid_frame", te.Code)
	}
}

// UIResponse 拦截 hostUI 的应答：不写 agent 流、交给 Coordinator.UIAnswered；重复应答回 ui_resolved。
func TestUIResponseHostUIBypassesAgent(t *testing.T) {
	f, th, a, o, coord := hostUIThread(t)
	require.NoError(t, f.s.RequestUI(f.ctx, th.id, json.RawMessage(cfmFrame)))
	sent(a) // 清掉 RequestUI 之前可能有的帧

	resp := json.RawMessage(`{"type":"extension_ui_response","id":"cfm_1","confirmed":true}`)
	require.NoError(t, f.s.UIResponse(f.ctx, th.id, resp))
	select {
	case ans := <-coord.answers:
		assert.Equal(t, th.id, ans.threadID)
		assert.Equal(t, "cfm_1", ans.id)
		assert.JSONEq(t, string(resp), string(ans.frame))
	case <-time.After(5 * time.Second):
		t.Fatal("UIAnswered 没有被调用")
	}
	assert.Empty(t, sent(a), "应答不写 agent 流")
	assert.Contains(t, o.snapshot().resolved, "cfm_1")
	got, err := f.s.Get(f.ctx, th.id)
	require.NoError(t, err)
	assert.NotEqual(t, StateAwaitingInput, string(got.State))

	assert.ErrorIs(t, f.s.UIResponse(f.ctx, th.id, resp), ErrUIResolved)
	assert.Empty(t, coord.answers, "重复应答不再交给协调层")
}

// 实时缓冲重置（agent 退出）或挂起（agent.gap）后 hostUI 仍待答，不发 InputResolved；agent 的请求照常解决。
// hostUI 不挡冻结闸门。WithdrawUI 撤回请求。
func TestRequestUISurvivesLiveReset(t *testing.T) {
	f, th, _, o, _ := hostUIThread(t)
	require.NoError(t, f.s.RequestUI(f.ctx, th.id, json.RawMessage(cfmFrame)))

	th.mu.Lock()
	assert.True(t, th.liveQuiescentLocked(), "只有 hostUI 时实时缓冲算静止")
	th.mu.Unlock()
	f.frame(th, `{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"Overwrite?"}`)
	th.mu.Lock()
	assert.False(t, th.liveQuiescentLocked(), "agent 的待答请求照旧挡冻结")
	f.s.resetLiveLocked(th)
	assert.True(t, th.live.HasUI("cfm_1"))
	assert.False(t, th.live.HasUI("ui-1"))
	th.mu.Unlock()
	resolved := o.snapshot().resolved
	assert.Contains(t, resolved, "ui-1")
	assert.NotContains(t, resolved, "cfm_1")

	th.mu.Lock()
	f.s.suspendLiveLocked(th)
	assert.True(t, th.live.HasUI("cfm_1"))
	assert.NotContains(t, th.gapUI, "cfm_1")
	th.mu.Unlock()

	// agent 退出（实时缓冲重置的另一条路径）
	f.s.onAgentStatus(f.ctx, th.orbID, json.RawMessage(`{"threadId":"`+th.id+`","status":"exited"}`))
	got, err := f.s.Get(f.ctx, th.id)
	require.NoError(t, err)
	assert.Equal(t, StateAwaitingInput, string(got.State))
	assert.NotContains(t, o.snapshot().resolved, "cfm_1")

	f.s.WithdrawUI(f.ctx, th.id, "cfm_1")
	assert.Contains(t, o.snapshot().resolved, "cfm_1")
	assert.Contains(t, f.sink.uiResolved, "cfm_1")
	th.mu.Lock()
	assert.False(t, th.live.HasUI("cfm_1"))
	assert.Empty(t, th.hostUI)
	th.mu.Unlock()
	assert.ErrorIs(t, f.s.UIResponse(f.ctx, th.id, json.RawMessage(`{"type":"extension_ui_response","id":"cfm_1","confirmed":true}`)), ErrUIResolved)
}
