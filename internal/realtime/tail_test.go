package realtime_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M19：带 tail 的订阅只补发最后 tail 条并在快照里给出 windowStart；不带 tail 的订阅照旧从头补发。
func TestSubscribeTail(t *testing.T) {
	e := newEnv(t)
	const id = "T-tail"
	e.seedThread(id, 999) // 999 条主会话消息 + 1 条子 agent 条目 = 1000 条

	a := e.dial("dev_a")
	a.send(map[string]any{"type": "sub", "threadId": id, "afterSeq": 0, "tail": 100})
	snap := a.next(func(m msg) bool { return m.Type == "thread.snapshot" })
	require.EqualValues(t, 1000, snap.LastSeq)
	assert.EqualValues(t, 900, snap.WindowStart)
	next := int64(901)
	a.next(func(m msg) bool {
		if m.Type != "entries" {
			return false
		}
		for _, en := range m.Entries {
			require.Equal(t, next, en.Seq)
			next++
		}
		return m.LastSeq == 1000
	})
	assert.EqualValues(t, 1001, next, "只收到最后 100 条")

	// 已有条目的续传（afterSeq > 0）不受 tail 影响
	c := e.dial("dev_c")
	c.send(map[string]any{"type": "sub", "threadId": id, "afterSeq": 990, "tail": 100})
	cSnap := c.next(func(m msg) bool { return m.Type == "thread.snapshot" })
	assert.Zero(t, cSnap.WindowStart)
	assert.EqualValues(t, 991, c.next(func(m msg) bool { return m.Type == "entries" }).Entries[0].Seq)

	// 不带 tail：没有窗口，从 seq 1 补发
	b := e.dial("dev_b")
	b.send(map[string]any{"type": "sub", "threadId": id, "afterSeq": 0})
	bSnap := b.next(func(m msg) bool { return m.Type == "thread.snapshot" })
	assert.Zero(t, bSnap.WindowStart)
	assert.NotContains(t, string(bSnap.raw), "windowStart")
	assert.EqualValues(t, 1, b.next(func(m msg) bool { return m.Type == "entries" }).Entries[0].Seq)
}
