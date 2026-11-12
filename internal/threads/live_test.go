package threads

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestLiveMessageAndToolLifecycle(t *testing.T) {
	l := NewLive()
	assert.Equal(t, StateProvisioning, l.Status(false))
	l.AgentReady()
	assert.Equal(t, StateIdle, l.Status(true))

	l.ApplyFrame(raw(`{"type":"agent_start"}`))
	assert.Equal(t, StateWorking, l.Status(true))
	l.ApplyFrame(raw(`{"type":"message_start","message":{"role":"assistant","timestamp":100}}`))
	l.ApplyFrame(raw(`{"type":"message_update","message":{"role":"assistant"}}`))
	l.ApplyFrame(raw(`{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash"}`))
	l.ApplyFrame(raw(`{"type":"tool_execution_update","toolCallId":"c1"}`))
	l.ApplyFrame(raw(`{"type":"tool_execution_update","toolCallId":"c1","n":2}`))
	l.ApplyFrame(raw(`{"type":"subagent_progress","index":0,"parentToolCallId":"c1"}`))
	frames := l.LiveFrames()
	assert.Len(t, frames, 4, "2 条消息帧 + 工具 start + 最新 update")
	assert.JSONEq(t, `{"type":"tool_execution_update","toolCallId":"c1","n":2}`, string(frames[3]))
	assert.Len(t, l.Subagents(), 1)

	// 时间戳不匹配的条目不清除
	l.ApplyEntry("message", raw(`{"message":{"role":"assistant","timestamp":99}}`))
	assert.Len(t, l.LiveFrames(), 4)
	l.ApplyEntry("message", raw(`{"message":{"role":"assistant","timestamp":100}}`))
	assert.Len(t, l.LiveFrames(), 2, "消息被条目覆盖")
	l.ApplyEntry("message", raw(`{"message":{"role":"toolResult","toolCallId":"c1","timestamp":101}}`))
	assert.Empty(t, l.LiveFrames())
	assert.Empty(t, l.Subagents(), "父工具清除时子 agent 一并清除")

	l.ApplyFrame(raw(`{"type":"agent_end","isTerminal":false}`))
	assert.Equal(t, StateWorking, l.Status(true), "非终结 agent_end 不结束工作")
	l.ApplyFrame(raw(`{"type":"agent_end","isTerminal":true}`))
	assert.Equal(t, StateIdle, l.Status(true))
	l.SetState(raw(`{"hasPendingAsyncWork":true}`))
	assert.Equal(t, StateWorking, l.Status(true))
	l.SetState(raw(`{"hasPendingAsyncWork":false}`))

	l.ApplyFrame(raw(`{"type":"prompt_result","status":"error"}`))
	assert.Equal(t, StateError, l.Status(true))
	l.ApplyFrame(raw(`{"type":"agent_start"}`))
	l.ApplyFrame(raw(`{"type":"agent_end"}`))
	assert.Equal(t, StateIdle, l.Status(true), "新一轮开始清除错误")
}

func TestLivePendingUI(t *testing.T) {
	l := NewLive()
	l.AgentReady()
	l.ApplyFrame(raw(`{"type":"extension_ui_request","id":"u1","method":"select"}`))
	l.ApplyFrame(raw(`{"type":"extension_ui_request","id":"n1","method":"notify"}`))
	l.ApplyFrame(raw(`{"type":"extension_ui_request","id":"u2","method":"ask"}`))
	assert.Len(t, l.PendingUI(), 2)
	assert.Equal(t, StateAwaitingInput, l.Status(true))
	eff := l.ApplyFrame(raw(`{"type":"extension_ui_request","id":"c","method":"cancel","targetId":"u2"}`))
	assert.Equal(t, "u2", eff.UIResolved)
	assert.True(t, l.ResolveUI("u1"))
	assert.False(t, l.ResolveUI("u1"), "先到先得：第二次应答无效")
	assert.Equal(t, StateIdle, l.Status(true))

	l.ApplyFrame(raw(`{"type":"plan_proposal"}`))
	assert.Equal(t, StateAwaitingInput, l.Status(true))
	assert.False(t, l.Quiescent())
	l.PlanProposal = nil
	assert.True(t, l.Quiescent())
}

func TestRouteCommand(t *testing.T) {
	for frame, want := range map[string]string{
		`{"type":"prompt","message":"hi"}`: RoutePass,
		`{"type":"open_session"}`:          RouteManaged,
		`{"type":"login"}`:                 RouteManaged,
		`{"type":"fork_from"}`:             RouteUseOrynFork,
		`{"message":"x"}`:                  RouteInvalidFrame,
		`nope`:                             RouteInvalidFrame,
	} {
		got, _ := RouteCommand(raw(frame))
		assert.Equal(t, want, got, frame)
	}
}
