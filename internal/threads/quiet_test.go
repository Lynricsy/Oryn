package threads

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lastState 返回最后一次 StateChanged。
func lastState(t *testing.T, o *recObserver) StateEvent {
	t.Helper()
	got := o.snapshot().states
	require.NotEmpty(t, got)
	return got[len(got)-1]
}

// 自动化投递开启的回合里 MarkQuiet 生效：结束时不置未读，StateEvent 带 Quiet 与 Auto（M19 步骤 3.3）。
func TestQuietAutoTurnSkipsUnread(t *testing.T) {
	f := newFixture(t, Config{})
	th, a, o := readyThread(f)

	_, err := f.s.Deliver(f.ctx, th.id, AutoMessage{Text: "[Oryn automation results · automatic] 1 result(s)."}, nil)
	require.NoError(t, err)
	f.respond(th, recvFrame(t, a), true, "")
	f.frame(th, `{"type":"agent_start"}`)
	assert.True(t, f.s.MarkQuiet(f.ctx, th.id))
	f.frame(th, `{"type":"agent_end","isTerminal":true}`)

	ev := lastState(t, o)
	assert.Equal(t, StateIdle, ev.State)
	assert.True(t, ev.Quiet)
	assert.True(t, ev.Auto)
	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	assert.False(t, row.Unread, "沉默的回合不置未读")

	// 下一段的第一条消息让沉默失效：没有再次 MarkQuiet 的自动化回合照常置未读
	f.frame(th, `{"type":"session_settled"}`)
	_, err = f.s.Deliver(f.ctx, th.id, AutoMessage{Text: "again"}, nil)
	require.NoError(t, err)
	f.respond(th, recvFrame(t, a), true, "")
	f.frame(th, `{"type":"agent_end","isTerminal":true}`)
	ev = lastState(t, o)
	assert.False(t, ev.Quiet)
	assert.True(t, ev.Auto)
	row, err = f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	assert.True(t, row.Unread)
}

// 主人亲自发起（或并入）的回合里 MarkQuiet 无效。
func TestQuietIneffectiveInOwnerTurn(t *testing.T) {
	f := newFixture(t, Config{})
	th, a, o := readyThread(f)

	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply))
	f.respond(th, recvFrame(t, a), true, "")
	assert.False(t, f.s.MarkQuiet(f.ctx, th.id))
	f.frame(th, `{"type":"agent_end","isTerminal":true}`)
	ev := lastState(t, o)
	assert.False(t, ev.Quiet)
	assert.False(t, ev.Auto)
	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	assert.True(t, row.Unread)

	// 自动化消息开启、随后主人插话：这段活动不再全是自动化
	f.frame(th, `{"type":"session_settled"}`)
	_, err = f.s.Deliver(f.ctx, th.id, AutoMessage{Text: "tick"}, nil)
	require.NoError(t, err)
	f.respond(th, recvFrame(t, a), true, "")
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"wait","streamingBehavior":"steer"}`), noReply))
	f.respond(th, recvFrame(t, a), true, "")
	assert.False(t, f.s.MarkQuiet(f.ctx, th.id))
	f.frame(th, `{"type":"agent_end","isTerminal":true}`)
	assert.False(t, lastState(t, o).Auto)
}

// 带投递标签的首条提示词（定时、触发规则建的线程）算自动化来源：完成通知据此判断“交给助手”。
func TestQuietStartupTaggedPromptIsAuto(t *testing.T) {
	f := newFixture(t, Config{})
	th, a, o := readyThread(f)
	th.mu.Lock()
	f.s.queueStartupLocked(th, []json.RawMessage{json.RawMessage(`{"type":"prompt","message":"run","` + orynTagKey + `":"srn_1"}`)})
	th.mu.Unlock()
	f.respond(th, recvFrame(t, a), true, "")
	f.frame(th, `{"type":"agent_start"}`)
	f.frame(th, `{"type":"agent_end","isTerminal":true}`)
	assert.True(t, lastState(t, o).Auto)
}
