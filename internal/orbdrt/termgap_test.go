package orbdrt

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func termFrame(seq int) []byte {
	return fmt.Appendf(nil, `{"type":"terminal_output","terminalId":"term-1","data":"x","seq":%d}`, seq)
}

func TestIsTerminalOutput(t *testing.T) {
	assert.True(t, isTerminalOutput(termFrame(1)))
	assert.False(t, isTerminalOutput([]byte(`{"type":"terminal_opened","terminal":{}}`)))
	assert.False(t, isTerminalOutput([]byte(`{"type":"notice","message":"{\"type\":\"terminal_output\""}`)))
	assert.False(t, isTerminalOutput([]byte(`{"terminalId":"t","type":"terminal_output"}`)), "前缀不符只退化为不丢弃")
}

// 积压 ≥8MiB：终端输出被丢弃（记 termGap），其它帧照常入队；回落到 ≤1MiB 时 takeTermGap 恰好返回一次 true。
func TestOutboxDropsTerminalOutputUnderBacklog(t *testing.T) {
	var o outbox
	o.push(outItem{data: termFrame(1), droppable: true})
	assert.Len(t, o.items, 1, "积压不足时终端输出照常入队")
	assert.False(t, o.takeTermGap(termLowWater))

	mb := make([]byte, 1<<20)
	for range 9 {
		o.push(outItem{data: mb})
	}
	o.push(outItem{data: termFrame(2), droppable: true})
	assert.Len(t, o.items, 10, "≥8MiB 时终端输出被丢弃")
	notice := []byte(`{"type":"notice","message":"kept"}`)
	o.push(outItem{data: notice})
	require.Len(t, o.items, 11, "非终端帧不受影响")
	assert.Equal(t, notice, o.items[10].data)
	_, gap := o.pendingGap()
	assert.False(t, gap, "丢弃终端输出不是 agent.gap")

	popped := 0
	for {
		h, ok := o.head()
		require.True(t, ok)
		o.pop(h)
		popped++
		if o.takeTermGap(termLowWater) {
			break
		}
		require.Less(t, popped, 11)
	}
	assert.LessOrEqual(t, o.size, termLowWater)
	assert.Equal(t, 10, popped, "回落到 ≤1MiB（只剩 notice）时才报")
	assert.False(t, o.takeTermGap(termLowWater), "只报一次")

	// 发送失败后恢复标记，下次再报。
	o.markTermGap()
	assert.True(t, o.takeTermGap(termLowWater))
}

// 经真实 link：控制面读得慢时，转发器积压到 8MiB 后丢弃终端输出、照常送达其它帧，积压排空后发一次 terminal.gap。
func TestTerminalGapAfterBacklogDrains(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.ctlNow(t)
	const thread = "T-termgap"
	th := rt.thread(thread, filepath.Join(rt.home, ".oryn", "threads", thread))
	pad := strings.Repeat("p", 1<<20)
	items := make([]outItem, 0, 10)
	for i := range 10 {
		items = append(items, outItem{data: fmt.Appendf(nil, `{"type":"notice","level":"info","message":"n-%d %s"}`, i, pad)})
	}
	th.out.pushAll(items) // 一次排入：转发器已在运行，逐条排入时它可能先排空队列并关流（见 pushAll）
	th.wake()
	st := waitAgentStream(t, cp) // 控制面先不读：转发器卡在第一帧（yamux 窗口 256KiB）
	for i := range 5 {
		th.out.push(outItem{data: termFrame(i), droppable: true})
	}
	th.out.push(outItem{data: []byte(`{"type":"notice","level":"info","message":"marker"}`)})
	before := cp.notifCount()

	fr := readFrames(st)
	frames := fr.frameWith(t, "marker", func(f json.RawMessage) bool { return strings.Contains(string(f), `"marker"`) })
	assert.Len(t, frames, 11)
	for _, f := range frames {
		assert.NotEqual(t, "terminal_output", frameType(f))
	}
	idx, raw := cp.waitNotif(t, before, string(apigen.MethodTerminalGap), nil)
	var p apigen.LinkCtlTerminalGapParams
	require.NoError(t, json.Unmarshal(raw, &p))
	assert.Equal(t, thread, p.ThreadID)

	// 积压排空后终端输出照常送达，且不再重复报 gap（队列排空后转发器关流，新帧走新流）。
	th.out.push(outItem{data: termFrame(99), droppable: true})
	th.wake()
	fr = readFrames(waitAgentStream(t, cp))
	f := fr.frameWith(t, "terminal_output", func(f json.RawMessage) bool { return frameType(f) == "terminal_output" })
	assert.Contains(t, string(f[len(f)-1]), `"seq":99`)
	cp.mu.Lock()
	n := 0
	for _, nt := range cp.notifs[idx:] {
		if nt.method == string(apigen.MethodTerminalGap) {
			n++
		}
	}
	cp.mu.Unlock()
	assert.Equal(t, 1, n)
}

// 端到端（fakeomp）：洪泛期间控制面不读，orbd 只丢终端输出；终端命令的应答都送达，
// 快照 seq 覆盖全部输出，gap 之后的新输出与快照首尾相接。
func TestTerminalFloodThroughAgent(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.ctlNow(t)
	const thread = "T-flood"
	startAgent(t, cp, thread, map[string]string{"FAKEOMP_DELAY_MS": "0"})
	st := waitAgentStream(t, cp)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread, "ready"))
	th := rt.lookupThread(thread)
	require.NotNil(t, th)

	const n = 12 << 20
	sendCmd(t, st, `{"type":"terminal_open","id":"open1","cols":80,"rows":24}`)
	sendCmd(t, st, `{"type":"terminal_input","id":"in1","terminalId":"term-fake-1","data":"flood 12582912\r"}`)
	sendCmd(t, st, `{"type":"terminal_snapshot","id":"snap1","terminalId":"term-fake-1"}`)
	// fakeomp 同步完成洪泛，快照应答一定在全部洪泛输出之后；等它进入缓冲再开始读。
	require.Eventually(t, func() bool {
		th.out.mu.Lock()
		defer th.out.mu.Unlock()
		for _, it := range th.out.items {
			if strings.Contains(string(it.data[:min(len(it.data), 200)]), `"id":"snap1"`) {
				return true
			}
		}
		return false
	}, 20*time.Second, 20*time.Millisecond, "snapshot response not buffered: %s", outboxSummary(th))
	th.out.mu.Lock()
	assert.True(t, th.out.termGap, "积压超过 8MiB 时丢弃过终端输出")
	th.out.mu.Unlock()
	before := cp.notifCount()

	fr := readFrames(st)
	frames := fr.frameWith(t, "snapshot response", responseID("snap1"))
	var got int
	resps := map[string]bool{}
	for _, f := range frames {
		switch frameType(f) {
		case "terminal_output":
			var o struct{ Data string }
			require.NoError(t, json.Unmarshal(f, &o))
			got += len(o.Data)
		case "response":
			var r struct {
				ID      string
				Success bool
			}
			require.NoError(t, json.Unmarshal(f, &r))
			resps[r.ID] = r.Success
		}
	}
	assert.Equal(t, map[string]bool{"open1": true, "in1": true, "snap1": true}, resps, "终端命令的应答一个不少")
	assert.Less(t, got, n, "部分终端输出被丢弃")
	var snap struct {
		Data struct {
			Seq int64
		}
	}
	require.NoError(t, json.Unmarshal(frames[len(frames)-1], &snap))
	total := int64(len("fake$ ") + len("flood 12582912\r\n") + n + len("\r\nfake$ "))
	assert.Equal(t, total, snap.Data.Seq, "快照 seq 覆盖全部输出")
	cp.waitNotif(t, before, string(apigen.MethodTerminalGap), func(raw json.RawMessage) bool {
		return strings.Contains(string(raw), thread)
	})

	sendCmd(t, st, `{"type":"terminal_input","id":"in2","terminalId":"term-fake-1","data":"a"}`)
	out := fr.frameWith(t, "echo", func(f json.RawMessage) bool { return frameType(f) == "terminal_output" })
	var o struct {
		Data string
		Seq  int64
	}
	require.NoError(t, json.Unmarshal(out[len(out)-1], &o))
	assert.Equal(t, "a", o.Data)
	assert.Equal(t, total+1, o.Seq, "快照之后的输出首尾相接")
}

func outboxSummary(th *thread) string {
	th.out.mu.Lock()
	defer th.out.mu.Unlock()
	counts := map[string]int{}
	for _, it := range th.out.items {
		counts[frameType(it.data)]++
	}
	return fmt.Sprintf("size=%d termGap=%v items=%v", th.out.size, th.out.termGap, counts)
}
