package threads

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	l.ApplyFrame(raw(`{"type":"subagent_progress","payload":{"index":0,"parentToolCallId":"c1","progress":{"index":0,"id":"A"}}}`))
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
	l.ApplyFrame(raw(`{"type":"extension_ui_request","id":"u3","method":"askDialog"}`))
	l.ApplyFrame(raw(`{"type":"extension_ui_request","id":"u4","method":"form"}`))
	assert.Len(t, l.PendingUI(), 4)
	assert.Equal(t, StateAwaitingInput, l.Status(true))
	eff := l.ApplyFrame(raw(`{"type":"extension_ui_request","id":"c","method":"cancel","targetId":"u2"}`))
	assert.Equal(t, "u2", eff.UIResolved)
	assert.True(t, l.ResolveUI("u1"))
	assert.False(t, l.ResolveUI("u1"), "先到先得：第二次应答无效")
	assert.True(t, l.ResolveUI("u3"))
	assert.True(t, l.ResolveUI("u4"))
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

// Core 的子 agent 帧把字段放在 payload 里；嵌套派发的子 agent 的 index 也从 0 编号，按 id 存才不会互相覆盖。
func TestLiveSubagentsKeyedByID(t *testing.T) {
	l := NewLive()
	l.ApplyFrame(raw(`{"type":"subagent_lifecycle","payload":{"id":"A","index":0,"status":"started","parentToolCallId":"c1"}}`))
	l.ApplyFrame(raw(`{"type":"subagent_lifecycle","payload":{"id":"B","index":1,"status":"started","parentToolCallId":"c1"}}`))
	// A 再派发的子 agent：index 同为 0，task 调用 c9 在 A 的会话里
	l.ApplyFrame(raw(`{"type":"subagent_lifecycle","payload":{"id":"A.X","index":0,"status":"started","parentToolCallId":"c9","parentSubagentId":"A"}}`))
	l.ApplyFrame(raw(`{"type":"subagent_progress","payload":{"index":0,"parentToolCallId":"c1","progress":{"index":0,"id":"A","status":"running"}}}`))
	l.ApplyFrame(raw(`{"type":"subagent_progress","payload":{"index":0,"parentToolCallId":"c9","parentSubagentId":"A","progress":{"index":0,"id":"A.X","status":"running"}}}`))
	// 不在 payload 里、也没有 id 的帧不入缓冲
	l.ApplyFrame(raw(`{"type":"subagent_progress","index":0}`))

	subs := l.Subagents()
	require.Len(t, subs, 3, "嵌套的 A.X 与根上同号的 A 各占一项")
	ids := make([]string, len(subs))
	for i, f := range subs {
		var v struct {
			Payload struct {
				ID       string `json:"id"`
				Progress struct {
					ID string `json:"id"`
				} `json:"progress"`
			} `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(f, &v))
		ids[i] = v.Payload.ID + v.Payload.Progress.ID
	}
	assert.Equal(t, []string{"A", "B", "A.X"}, ids, "按首次出现顺序，取各自最新帧")

	// 主会话里 c1 的结果到达：清掉 c1 派发的 A、B，以及 A 再派发的 A.X
	l.ApplyEntry("message", raw(`{"message":{"role":"toolResult","toolCallId":"c1","timestamp":1}}`))
	assert.Empty(t, l.Subagents())
}

// 下一条消息预测只保留最新一帧的 choices；撤回帧、新回合与 live.reset 都清掉，快照里便不会出现过期预测。
func TestLiveComposerPrediction(t *testing.T) {
	l := NewLive()
	l.AgentReady()
	l.ApplyFrame(raw(`{"type":"oryn_composer_prediction","choices":["run the tests","ship it"]}`))
	assert.Equal(t, []string{"run the tests", "ship it"}, l.ComposerPrediction)
	assert.Equal(t, StateIdle, l.Status(true), "预测不改变线程状态")

	l.ApplyFrame(raw(`{"type":"oryn_composer_prediction","choices":[]}`))
	assert.Empty(t, l.ComposerPrediction, "空 choices 是撤回")

	l.ApplyFrame(raw(`{"type":"oryn_composer_prediction","choices":["ship it"]}`))
	l.ApplyFrame(raw(`{"type":"agent_start"}`))
	assert.Empty(t, l.ComposerPrediction, "新回合开始即失效，不等 Core 的撤回帧")

	l.ApplyFrame(raw(`{"type":"oryn_composer_prediction","choices":["ship it"]}`))
	l.Reset()
	assert.Empty(t, l.ComposerPrediction)
}
