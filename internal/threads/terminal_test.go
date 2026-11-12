package threads

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store"
)

// termSink 记录终端相关的 Sink 回调与普通帧。
type termSink struct {
	nopSink
	mu      sync.Mutex
	out     []string
	frames  []string
	lagged  []string
	resized []string
}

func (k *termSink) TermOutput(id string, f json.RawMessage) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.out = append(k.out, id+" "+string(f))
}

func (k *termSink) Frame(_ string, f json.RawMessage) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.frames = append(k.frames, string(f))
}

func (k *termSink) TermLagged(id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.lagged = append(k.lagged, id)
}

func (k *termSink) TermResized(id, term string, cols, rows int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.resized = append(k.resized, fmt.Sprintf("%s %s %dx%d", id, term, cols, rows))
}

func (k *termSink) snapshot() (out, frames, lagged, resized []string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.out...), append([]string(nil), k.frames...),
		append([]string(nil), k.lagged...), append([]string(nil), k.resized...)
}

func frameTypes(fs []apigen.OmpFrame) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f["type"].(string))
	}
	return out
}

// terminal_configure 必须在 set_subagent_subscription 之后、set_thinking_level 之前，且开启两项能力；
// 漏发会让 pty:true 的命令落不到终端面板。
func TestInitFramesConfigureTerminal(t *testing.T) {
	f := newFixture(t, Config{})
	th := f.thread(f.orb("ready"), StateIdle)

	fs := initFrames(th)
	assert.Equal(t, []string{"open_session", "set_event_filter", "set_ask_dialog", "set_subagent_subscription", "terminal_configure"}, frameTypes(fs))
	assert.Equal(t, apigen.OmpFrame{"type": "terminal_configure", "interactiveCommands": true, "agentTools": true}, fs[4])

	level := "high"
	th.thinking = &level
	fs = initFrames(th)
	assert.Equal(t, []string{"open_session", "set_event_filter", "set_ask_dialog", "set_subagent_subscription", "terminal_configure", "set_thinking_level"}, frameTypes(fs))
}

// terminal_output 只走 TermOutput：不进普通帧广播、不进实时缓冲、不算 agent 活动（否则开着 dev server 的 orb 永不冻结）。
func TestTerminalOutputHotPath(t *testing.T) {
	f := newFixture(t, Config{})
	k := &termSink{}
	f.s.SetSink(k)
	orbID := f.orb("ready")
	th := f.thread(orbID, StateIdle)

	line := `{"type":"terminal_output","terminalId":"term-1","data":"hello","seq":5}`
	f.s.onFrame(f.ctx, th, orbID, []byte(line))

	out, frames, _, _ := k.snapshot()
	assert.Equal(t, []string{th.id + " " + line}, out)
	assert.Empty(t, frames)
	f.s.mu.Lock()
	_, touched := f.s.touched[orbID]
	f.s.mu.Unlock()
	assert.False(t, touched, "terminal output must not count as agent activity")
	th.mu.Lock()
	assert.True(t, th.live.Quiescent())
	th.mu.Unlock()

	// 其它终端帧照常经 Frame 广播
	opened := `{"type":"terminal_opened","terminal":{"id":"term-1"}}`
	f.s.onFrame(f.ctx, th, orbID, []byte(opened))
	out, frames, _, _ = k.snapshot()
	assert.Len(t, out, 1)
	assert.Equal(t, []string{opened}, frames)
}

// terminal_resize 入队后广播 term.resized（含发起方）；命令本身照常下发给 agent。
func TestTerminalResizeBroadcast(t *testing.T) {
	f := newFixture(t, Config{})
	k := &termSink{}
	f.s.SetSink(k)
	orbID := f.orb("ready")
	th := f.thread(orbID, StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)

	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"terminal_resize","terminalId":"term-1","cols":120,"rows":40}`), func(json.RawMessage) {}))
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"terminal_input","terminalId":"term-1","data":"ls\r"}`), func(json.RawMessage) {}))

	_, _, _, resized := k.snapshot()
	assert.Equal(t, []string{th.id + " term-1 120x40"}, resized)
	got := sent(a)
	require.Len(t, got, 2)
	assert.Contains(t, got[0], `"terminal_resize"`)
	assert.Contains(t, got[1], `"terminal_input"`)
}

// terminal.gap 只对本 orb 的线程转成 TermLagged。
func TestTerminalGapNotify(t *testing.T) {
	f := newFixture(t, Config{})
	k := &termSink{}
	f.s.SetSink(k)
	orbID := f.orb("ready")
	th := f.thread(orbID, StateIdle)
	other := f.orb("ready")

	params, _ := json.Marshal(apigen.LinkCtlTerminalGapParams{ThreadID: th.id})
	f.s.onTerminalGap(f.ctx, other, params)
	f.s.onTerminalGap(f.ctx, orbID, params)

	_, _, lagged, _ := k.snapshot()
	assert.Equal(t, []string{th.id}, lagged)
}

// countingOrbs 统计 TouchUser 调用次数。
type countingOrbs struct {
	nopOrbs
	users atomic.Int32
}

func (o *countingOrbs) TouchUser(context.Context, string) { o.users.Add(1) }

// 每次终端按键都是一条 cmd：touchUser 按 orb 节流，避免每键一条 UPDATE。
func TestTouchUserThrottled(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	o := &countingOrbs{}
	s := New(Config{DataDir: t.TempDir()}, st, o)
	for range 50 {
		s.touchUser("orb-a")
	}
	s.touchUser("orb-b")
	s.Close() // 等待后台写入完成
	assert.Equal(t, int32(2), o.users.Load())
}
