package realtime_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// termOut 是 terminal_output 帧的字段。
type termOut struct {
	TerminalID string `json:"terminalId"`
	Data       string `json:"data"`
	Seq        int64  `json:"seq"`
}

func (m msg) termOutput() (termOut, bool) {
	if m.Type != "frame" || m.frameType() != "terminal_output" {
		return termOut{}, false
	}
	var o termOut
	_ = json.Unmarshal(m.Frame, &o)
	return o, true
}

// call 发一条 cmd 并等待其应答（期间的其它消息被丢弃），返回应答的 data。
func (c *client) call(threadID, id string, frame any) json.RawMessage {
	c.t.Helper()
	c.send(map[string]any{"type": "cmd", "threadId": threadID, "id": id, "frame": frame})
	m := c.next(func(m msg) bool { return (m.Type == "res" || m.Type == "error") && m.ID == id })
	require.Equal(c.t, "res", m.Type, "cmd %s failed: %s", id, m.raw)
	var r struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   string          `json:"error"`
	}
	require.NoError(c.t, json.Unmarshal(m.Response, &r))
	require.True(c.t, r.Success, "cmd %s: %s", id, r.Error)
	return r.Data
}

func (c *client) subscribe(threadID string) {
	c.t.Helper()
	c.send(map[string]any{"type": "sub", "threadId": threadID, "afterSeq": 0})
	c.next(func(m msg) bool { return m.Type == "thread.snapshot" && m.ThreadID == threadID })
}

// M3 §2.3：terminal_output 只发给 term.watch 的连接；其它终端帧与 term.resized 发给全部订阅者。
// 待发超过 TermLag 时丢弃终端输出并发 term.lagged，连接不断开，之后经 terminal_snapshot 重同步；
// 中途开启观看的连接从快照 seq 起收到的帧首尾相接。
func TestTerminalWatchLagAndResync(t *testing.T) {
	e := newEnv(t)
	e.hub.TermLag = 4 << 10
	ctx := context.Background()
	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "hi"})
	require.NoError(t, err)
	e.waitState(th.ID, threads.StateIdle)

	// 未订阅就 term.watch → not_subscribed
	x := e.dial("dev_x")
	x.send(map[string]any{"type": "term.watch", "threadId": th.ID, "on": true})
	assert.Equal(t, "not_subscribed", x.next(func(m msg) bool { return m.Type == "error" }).Code)

	a, b := e.dial("dev_a"), e.dial("dev_b")
	a.subscribe(th.ID)
	b.subscribe(th.ID)
	a.send(map[string]any{"type": "term.watch", "threadId": th.ID, "on": true})

	var info struct {
		TerminalID string `json:"terminalId"`
	}
	require.NoError(t, json.Unmarshal(a.call(th.ID, "open", map[string]any{"type": "terminal_open", "cols": 80, "rows": 24}), &info))
	require.NotEmpty(t, info.TerminalID)
	a.call(th.ID, "rz", map[string]any{"type": "terminal_resize", "terminalId": info.TerminalID, "cols": 132, "rows": 43})

	// b 没开观看：收得到 terminal_opened 与 term.resized（后者在提示符输出之后才产生），收不到 terminal_output
	got := b.collect(func(m msg) bool { return m.Type == "term.resized" })
	var sawOpened bool
	for _, m := range got {
		_, isOut := m.termOutput()
		assert.False(t, isOut, "unwatched connection received terminal_output: %s", m.raw)
		sawOpened = sawOpened || (m.Type == "frame" && m.frameType() == "terminal_opened")
	}
	assert.True(t, sawOpened)
	rz := got[len(got)-1]
	assert.Equal(t, info.TerminalID, rz.TerminalID)
	assert.Equal(t, []int{132, 43}, []int{rz.Cols, rz.Rows})

	// 洪泛：每帧 32KiB > TermLag，a 收到 term.lagged 而不是被断开
	a.send(map[string]any{"type": "cmd", "threadId": th.ID, "id": "flood", "frame": map[string]any{"type": "terminal_input", "terminalId": info.TerminalID, "data": "flood 300000\r"}})
	a.next(func(m msg) bool { return m.Type == "term.lagged" && m.ThreadID == th.ID })

	// 快照在洪泛结束后才返回（替身持终端锁整段输出），seq 覆盖全部输出
	var snap struct {
		Data string `json:"data"`
		Seq  int64  `json:"seq"`
	}
	require.NoError(t, json.Unmarshal(a.call(th.ID, "snap-a", map[string]any{"type": "terminal_snapshot", "terminalId": info.TerminalID}), &snap))
	assert.Equal(t, int64(len("fake$ ")+len("flood 300000\r\n")+300000+len("\r\nfake$ ")), snap.Seq)
	assert.True(t, strings.HasSuffix(snap.Data, "x\r\nfake$ "))

	// b 中途开启观看：先 watch，再快照，之后的帧与快照首尾相接
	b.send(map[string]any{"type": "term.watch", "threadId": th.ID, "on": true})
	require.NoError(t, json.Unmarshal(b.call(th.ID, "snap-b", map[string]any{"type": "terminal_snapshot", "terminalId": info.TerminalID}), &snap))
	// 屏障：收到 pong 说明写循环已写完（并退回预算）此前的快照应答，b 的待发不会让后续小帧被丢弃
	b.send(map[string]any{"type": "ping"})
	b.next(func(m msg) bool { return m.Type == "pong" })
	a.call(th.ID, "type", map[string]any{"type": "terminal_input", "terminalId": info.TerminalID, "data": "hello\r"})
	want := "hello\r\nfake$ "
	last := snap.Seq
	var tail strings.Builder
	b.next(func(m msg) bool {
		o, ok := m.termOutput()
		if !ok || o.Seq <= snap.Seq {
			return false
		}
		start := o.Seq - int64(len(o.Data))
		require.Equal(t, last, start, "gap or overlap after snapshot")
		last = o.Seq
		tail.WriteString(o.Data)
		return tail.Len() >= len(want)
	})
	assert.Equal(t, want, tail.String())

	// orbd 的 terminal.gap：所有观看终端的连接收到 term.lagged
	require.NoError(t, e.orbs.OrbdCtl(*th.OrbID).Notify(string(apigen.MethodTerminalGap), apigen.LinkCtlTerminalGapParams{ThreadID: th.ID}))
	b.next(func(m msg) bool { return m.Type == "term.lagged" && m.ThreadID == th.ID })
}
