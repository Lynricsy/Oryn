package coord

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 主线分段与近况（M19 步骤 3.2）。

// fakeAfter 记下排的定时器，测试手动触发。orig 保留每个回调本身（Stop 之后也在），用来模拟「Stop 时回调已经开始执行」。
type fakeAfter struct {
	mu    sync.Mutex
	fns   []func()
	orig  []func()
	durs  []time.Duration
	stops int
}

func (f *fakeAfter) AfterFunc(d time.Duration, fn func()) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := len(f.fns)
	f.fns = append(f.fns, fn)
	f.orig = append(f.orig, fn)
	f.durs = append(f.durs, d)
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fns[i] != nil {
			f.fns[i] = nil
			f.stops++
		}
	}
}

// fireLast 触发最后一个仍有效的定时器；没有时返回假。
func (f *fakeAfter) fireLast() bool {
	f.mu.Lock()
	var fn func()
	for i := len(f.fns) - 1; i >= 0; i-- {
		if f.fns[i] != nil {
			fn, f.fns[i] = f.fns[i], nil
			break
		}
	}
	f.mu.Unlock()
	if fn == nil {
		return false
	}
	fn()
	return true
}

// addMainMsg 往线程主会话写一条消息条目。
func addMainMsg(t *testing.T, e *env, threadID string, i int, ts int64, role string, content any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "message", "message": map[string]any{"role": role, "content": content}})
	require.NoError(t, err)
	_, err = e.st.Q.UpsertEntry(e.ctx, db.UpsertEntryParams{
		ThreadID: threadID, File: "s.jsonl", EntryID: fmt.Sprintf("m%03d", i), Type: "message", Ts: &ts, Payload: b,
	})
	require.NoError(t, err)
}

func TestMainInitFramesAndTools(t *testing.T) {
	e := newEnv(t, true)
	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	prof, err := e.c.AgentProfile(e.ctx, main.ID)
	require.NoError(t, err)
	names := map[string]bool{}
	for _, d := range prof.Tools {
		names[d["name"].(string)] = true
		if d["name"] == ToolUpdateNotes {
			assert.Equal(t, "essential", d["loadMode"])
		}
	}
	assert.True(t, names[ToolUpdateNotes], "主线有 update_main_notes")
	assert.Contains(t, prof.SystemPrompt, "## This is the main conversation")
	assert.Contains(t, prof.SystemPrompt, "## 主线近况")

	// 普通助手对话与 Space 都没有主线工具，也没有近况
	other := e.assistant()
	op, err := e.c.AgentProfile(e.ctx, other.ID)
	require.NoError(t, err)
	assert.NotContains(t, op.SystemPrompt, "主线近况")
	for _, defs := range [][]apigen.OmpFrame{op.Tools, ToolDefs(ToolSet{InSpace: true})} {
		for _, d := range defs {
			assert.NotEqual(t, ToolUpdateNotes, d["name"])
			assert.NotEqual(t, ToolStaySilent, d["name"])
		}
	}
	// 普通助手对话调用 update_main_notes 回 unknown_tool
	errText(t, e.call(other, ToolUpdateNotes, `{"text":"x"}`, true), "unknown_tool")
}

func TestMainNotes(t *testing.T) {
	e := newEnv(t, true)
	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	n, err := e.c.Notes(e.ctx)
	require.NoError(t, err)
	assert.Empty(t, n.Text)

	r := e.call(main, ToolUpdateNotes, `{"text":"主人周五要发版"}`, true)
	require.False(t, r.IsError, r.Text)
	n, err = e.c.Notes(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, "主人周五要发版", n.Text)
	require.NotNil(t, n.UpdatedAt)

	// 上限按 Unicode 字符：1500 个汉字可以，1501 个不行
	ok := strings.Repeat("字", mainNotesMax)
	_, err = e.c.PutNotes(e.ctx, ok)
	require.NoError(t, err)
	b, _ := json.Marshal(map[string]string{"text": ok + "多"})
	r = e.call(main, ToolUpdateNotes, string(b), true)
	errText(t, r, CodeInvalid)
	assert.Contains(t, r.Text, "compress")
	n, err = e.c.Notes(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, ok, n.Text, "超限时不改")

	brief, err := e.c.mainBrief(e.ctx, main.ID)
	require.NoError(t, err)
	assert.Contains(t, brief, ok)
}

func TestMainBrief(t *testing.T) {
	e := newEnv(t, true)
	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	brief, err := e.c.mainBrief(e.ctx, main.ID)
	require.NoError(t, err)
	assert.Contains(t, brief, "### 备忘\n\n（空）")
	assert.Contains(t, brief, "### 最近的对话\n\n（还没有）")

	base := time.Date(2026, 11, 14, 2, 0, 0, 0, time.UTC).UnixMilli()
	i := 0
	add := func(role string, content any) {
		i++
		addMainMsg(t, e, main.ID, i, base+int64(i)*60_000, role, content)
	}
	// 回合 1：主人提问，助手中间一句、最后一句（只取最后一句）
	add("user", "帮我看看 CI")
	add("assistant", []any{map[string]any{"type": "text", "text": "我先查一下"}, map[string]any{"type": "toolCall", "id": "c1", "name": "read_thread", "arguments": map[string]any{}}})
	add("assistant", "CI 挂在 lint")
	// 回合 2：自动来料，助手沉默（整回合不取）
	add("user", "[Oryn automation results · automatic] 1 result(s).\n{}")
	add("assistant", []any{map[string]any{"type": "text", "text": "不值得说"}, map[string]any{"type": "toolCall", "id": "c2", "name": ToolStaySilent, "arguments": map[string]any{"reason": "routine"}}})
	// 回合 3：自动来料，助手说出口（来料本身不取）
	add("user", "[Oryn automation results · automatic] 1 result(s).\n{}")
	add("assistant", "夜间构建失败了")
	// 回合 4：主人的长消息被截到 600 字
	add("user", strings.Repeat("长", 700))

	brief, err = e.c.mainBrief(e.ctx, main.ID)
	require.NoError(t, err)
	assert.Contains(t, brief, "[11-14 02:01] 主人：帮我看看 CI")
	assert.Contains(t, brief, "[11-14 02:03] 你：CI 挂在 lint")
	assert.NotContains(t, brief, "我先查一下")
	assert.NotContains(t, brief, "不值得说")
	assert.NotContains(t, brief, "[Oryn")
	assert.Contains(t, brief, "你：夜间构建失败了")
	assert.Contains(t, brief, "主人："+strings.Repeat("长", mainBriefEach))
	assert.NotContains(t, brief, strings.Repeat("长", mainBriefEach+1))
	assert.Less(t, strings.Index(brief, "帮我看看 CI"), strings.Index(brief, "夜间构建失败了"))

	// 条数上限 12，合计 6000 字：再写 20 条 590 字的主人消息，只留最新的、且合计不超过预算
	for k := 0; k < 20; k++ {
		add("user", fmt.Sprintf("%02d", k)+strings.Repeat("x", 588))
	}
	brief, err = e.c.mainBrief(e.ctx, main.ID)
	require.NoError(t, err)
	got := strings.Count(brief, "主人：")
	assert.Equal(t, 10, got, "每条 590 字，6000 字预算只放得下 10 条")
	assert.Contains(t, brief, "主人：19")
	assert.NotContains(t, brief, "帮我看看 CI")
}

func TestMainEpisodeEnds(t *testing.T) {
	e := newEnv(t, true)
	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	fa := &fakeAfter{}
	e.c.cfg.AfterFunc = fa.AfterFunc
	ended := make(chan string, 4)
	e.c.afterEpisode = func(id, reason string) { ended <- id + " " + reason }
	turn := func() {
		e.c.StateChanged(threads.StateEvent{ThreadID: main.ID, Prev: threads.StateIdle, State: threads.StateWorking})
		e.c.StateChanged(threads.StateEvent{ThreadID: main.ID, Prev: threads.StateWorking, State: threads.StateIdle})
	}

	// 静止 10 分钟：一个回合后排定时器，下一回合开始时取消，到期时结束一段
	turn()
	require.Equal(t, []time.Duration{mainIdleStop}, fa.durs)
	turn()
	assert.Equal(t, 1, fa.stops, "进入 working 取消上一回合的定时器")
	require.True(t, fa.fireLast())
	assert.Equal(t, main.ID+" idle", <-ended)

	// 一段内满 8 个回合：第 8 个回合结束时直接结束一段（计数已在上面清零）
	for k := 0; k < mainEpisodeTurns-1; k++ {
		turn()
	}
	select {
	case got := <-ended:
		t.Fatalf("回合未满就结束：%s", got)
	default:
	}
	turn()
	select {
	case got := <-ended:
		assert.Equal(t, main.ID+" turns", got)
	case <-time.After(5 * time.Second):
		t.Fatal("满 8 回合没有结束一段")
	}

	// 普通助手对话不计
	other := e.assistant()
	n := len(fa.durs)
	e.c.StateChanged(threads.StateEvent{ThreadID: other.ID, Prev: threads.StateWorking, State: threads.StateIdle})
	assert.Len(t, fa.durs, n)
}

// 旧定时器的回调在 Stop 时已经开始执行（time.AfterFunc 的 Stop 拦不住）：它在新一段的定时器排下之后才拿到锁，
// 必须什么都不做——不清计数、不结束一段；新定时器照常生效。
func TestMainEpisodeStaleTimer(t *testing.T) {
	e := newEnv(t, true)
	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	fa := &fakeAfter{}
	e.c.cfg.AfterFunc = fa.AfterFunc
	ended := make(chan string, 4)
	e.c.afterEpisode = func(id, reason string) { ended <- id + " " + reason }
	turn := func() {
		e.c.StateChanged(threads.StateEvent{ThreadID: main.ID, Prev: threads.StateIdle, State: threads.StateWorking})
		e.c.StateChanged(threads.StateEvent{ThreadID: main.ID, Prev: threads.StateWorking, State: threads.StateIdle})
	}
	turn()
	require.Len(t, fa.orig, 1)
	stale := fa.orig[0]
	turn() // 进入 working 取消旧定时器，回到 idle 排新定时器
	require.Len(t, fa.orig, 2)

	stale()
	select {
	case got := <-ended:
		t.Fatalf("旧回调结束了一段：%s", got)
	default:
	}
	e.c.mu.Lock()
	turns, hasTimer := e.c.mainEp.turns, e.c.mainEp.stop != nil
	e.c.mu.Unlock()
	assert.Equal(t, 2, turns, "旧回调不清计数")
	assert.True(t, hasTimer, "新定时器仍在")

	require.True(t, fa.fireLast())
	assert.Equal(t, main.ID+" idle", <-ended)
	// 进入 working 后才执行的旧回调同样无效
	turn()
	stale2 := fa.orig[len(fa.orig)-1]
	e.c.StateChanged(threads.StateEvent{ThreadID: main.ID, Prev: threads.StateIdle, State: threads.StateWorking})
	stale2()
	select {
	case got := <-ended:
		t.Fatalf("working 中的旧回调结束了一段：%s", got)
	default:
	}
	e.c.mu.Lock()
	assert.Equal(t, 1, e.c.mainEp.turns)
	e.c.mu.Unlock()
}
