package threads

import (
	"encoding/json"
	"fmt"
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

// 工作线程里全由自动化投递的回合以 [SILENT] 收尾：按沉默结束（不置未读、StateEvent 带 Quiet），TurnEvent 也带 Quiet。
func TestSilentReplyQuietsAutoTurn(t *testing.T) {
	f := newFixture(t, Config{})
	th, a, o := readyThread(f)
	cmd := deliverTagged(t, f, th, a, AutoMessage{Text: "check", Tag: "srn_q"})
	f.respond(th, cmd, true, `{"agentInvoked":true}`)
	f.frame(th, `{"type":"agent_start"}`)
	f.frame(th, agentEnd(true, assistantMsg("Nothing new since last run.\n[SILENT]", "stop", "")))
	ev := lastState(t, o)
	assert.Equal(t, StateIdle, ev.State)
	assert.True(t, ev.Quiet)
	f.frame(th, fmt.Sprintf(`{"type":"prompt_result","id":%q,"status":"completed","agentInvoked":true}`, frameID(t, cmd)))
	require.Len(t, o.turnEvents(), 1)
	assert.True(t, o.turnEvents()[0].Quiet)
	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	assert.False(t, row.Unread)

	// 没有标签的自动化投递同样适用
	f.frame(th, `{"type":"session_settled"}`)
	_, err = f.s.Deliver(f.ctx, th.id, AutoMessage{Text: "tick"}, nil)
	require.NoError(t, err)
	f.respond(th, recvFrame(t, a), true, "")
	f.frame(th, agentEnd(true, assistantMsg("[silent]", "stop", "")))
	assert.True(t, lastState(t, o).Quiet)
}

// 主人发起的回合、出错的回合、标记夹在句中：照常结束。
func TestSilentReplyIgnored(t *testing.T) {
	f := newFixture(t, Config{})
	th, a, o := readyThread(f)
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply))
	f.respond(th, recvFrame(t, a), true, "")
	f.frame(th, agentEnd(true, assistantMsg("[SILENT]", "stop", "")))
	assert.False(t, lastState(t, o).Quiet, "主人发起的回合")

	f.frame(th, `{"type":"session_settled"}`)
	cmd := deliverTagged(t, f, th, a, AutoMessage{Text: "check", Tag: "srn_e"})
	f.respond(th, cmd, true, `{"agentInvoked":true}`)
	f.frame(th, agentEnd(true, assistantMsg("[SILENT]", "error", "rate limited")))
	assert.False(t, lastState(t, o).Quiet, "出错的回合")

	f.frame(th, `{"type":"session_settled"}`)
	cmd = deliverTagged(t, f, th, a, AutoMessage{Text: "check", Tag: "srn_m"})
	f.respond(th, cmd, true, `{"agentInvoked":true}`)
	f.frame(th, agentEnd(true, assistantMsg("I would say [SILENT] but proxy jp is down", "stop", "")))
	assert.False(t, lastState(t, o).Quiet, "句中的标记")
}

func TestSilentReply(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"[SILENT]", true},
		{"  [silent]\n", true},
		{"[SILENT]\nnothing changed", true},
		{"all good\n\n[SILENT]  ", true},
		{"", false},
		{"SILENT", false},
		{"all good [SILENT]", false},
		{"a\n[SILENT]\nb", false},
	} {
		assert.Equal(t, tc.want, silentReply(tc.text), "silentReply(%q)", tc.text)
	}
}
