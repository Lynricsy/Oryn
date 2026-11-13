package threads

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// indexSink 记录 index.patch（IndexUpsert）。
type indexSink struct {
	nopSink
	mu  sync.Mutex
	ups []apigen.Thread
}

func (r *indexSink) IndexUpsert(t apigen.Thread) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ups = append(r.ups, t)
}

func (r *indexSink) snapshot() []apigen.Thread {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]apigen.Thread(nil), r.ups...)
}

func listActivity(t *testing.T, f *fixture, id string) *string {
	t.Helper()
	all, err := f.s.List(f.ctx, ListFilter{})
	require.NoError(t, err)
	for _, th := range all {
		if th.ID == id {
			return th.Activity
		}
	}
	t.Fatalf("线程 %s 不在列表里", id)
	return nil
}

func TestToolActivity(t *testing.T) {
	long := strings.Repeat("字", 200)
	cases := []struct {
		name, args, intent, want string
	}{
		{"read", `{"path":"a.ts","i":"读 a.ts"}`, "", "读 a.ts"},
		{"read", `{"path":"a.ts"}`, "看\n  入口", "看 入口"},
		{"read", `{"path":"web/src/a.ts"}`, "", "web/src/a.ts"},
		{"grep", `{"pattern":"afterSeq","path":"web"}`, "", "afterSeq · web"},
		{"bash", `{"command":"bun run test\n-- socket"}`, "", "bun run test -- socket"},
		{"web_search", `{"query":"oklch"}`, "", "oklch"},
		{"todo", `{}`, "", "todo"},
		{"x", `not json`, "", "x"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, toolActivity(c.name, json.RawMessage(c.args), c.intent), "%s %s", c.name, c.args)
	}
	got := toolActivity("read", nil, long)
	assert.Equal(t, maxActivityRunes, len([]rune(got)))
	assert.Equal(t, "…", string([]rune(got)[maxActivityRunes-1:]))

	assert.Empty(t, frameActivity("message_update", []byte(`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","delta":"{\"i\":\"读"}}`)),
		"流式增量里的参数不完整，不取")
	assert.Equal(t, "读 socket.ts", frameActivity("message_update",
		[]byte(`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":1,"toolCall":{"type":"toolCall","id":"c1","name":"read","arguments":{"path":"s.ts","i":"读 socket.ts"}}}}`)))
	assert.Equal(t, "跑测试", frameActivity("tool_execution_start",
		[]byte(`{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"x"},"intent":"跑测试"}`)))
	assert.Empty(t, frameActivity("agent_start", []byte(`{"type":"agent_start"}`)))
}

// working 时记录最近一次工具调用的意图并推送（同值不重复推送），REST 列表同样带出；离开 working 清空。
func TestThreadActivityLifecycle(t *testing.T) {
	f := newFixture(t, Config{})
	sink := &indexSink{}
	f.s.SetSink(sink)
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	attach(th, fakeAgent(8), apigen.AgentStatusReady)
	frame := func(s string) { f.s.onFrame(f.ctx, th, orb, []byte(s)) }

	// idle 时的工具帧不产生活动
	frame(`{"type":"tool_execution_start","toolCallId":"c0","toolName":"read","args":{"path":"x"},"intent":"空闲时"}`)
	assert.Nil(t, listActivity(t, f, th.id))

	frame(`{"type":"agent_start"}`)
	require.Equal(t, StateWorking, th.state)
	n := len(sink.snapshot())

	frame(`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":0,"toolCall":{"type":"toolCall","id":"c1","name":"read","arguments":{"path":"s.ts","i":"读 socket.ts 定位重连逻辑"}}}}`)
	ups := sink.snapshot()
	require.Len(t, ups, n+1)
	require.NotNil(t, ups[n].Activity)
	assert.Equal(t, "读 socket.ts 定位重连逻辑", *ups[n].Activity)

	// 同一次调用的 tool_execution_start 给出同一意图：不再推送
	frame(`{"type":"tool_execution_start","toolCallId":"c1","toolName":"read","args":{"path":"s.ts"},"intent":"读 socket.ts 定位重连逻辑"}`)
	assert.Len(t, sink.snapshot(), n+1)
	// 文本增量不影响活动
	frame(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"x"}}`)
	assert.Len(t, sink.snapshot(), n+1)

	frame(`{"type":"tool_execution_start","toolCallId":"c2","toolName":"bash","args":{"command":"bun run test"}}`)
	ups = sink.snapshot()
	require.Len(t, ups, n+2)
	assert.Equal(t, "bun run test", *ups[n+1].Activity)
	act := listActivity(t, f, th.id)
	require.NotNil(t, act)
	assert.Equal(t, "bun run test", *act)
	got, err := f.s.Get(f.ctx, th.id)
	require.NoError(t, err)
	require.NotNil(t, got.Activity)

	frame(`{"type":"agent_end"}`)
	require.NotEqual(t, StateWorking, th.state)
	ups = sink.snapshot()
	assert.Nil(t, ups[len(ups)-1].Activity, "离开 working 的 index.patch 不带活动")
	assert.Nil(t, listActivity(t, f, th.id))
}

// 自动标题带卡片图标：项目尚无图标时采用，已有图标（含先前采用的）不覆盖。
func TestAutoTitleAdoptsProjectIcon(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	mkProject := func(icon *string) string {
		id := ids.New("prj")
		require.NoError(t, f.st.Q.CreateProject(f.ctx, db.CreateProjectParams{ID: id, Name: id, Icon: icon, CreatedAt: 1, UpdatedAt: 1}))
		return id
	}
	mkThread := func(prj string) *thread {
		id := ids.Thread()
		require.NoError(t, f.st.Q.CreateThread(f.ctx, db.CreateThreadParams{ID: id, ProjectID: &prj, OrbID: &orb, TitleSource: "auto", State: StateIdle, SessionDir: sessionDirOf(id), CreatedAt: 1, UpdatedAt: 1}))
		th, err := f.s.get(f.ctx, id)
		require.NoError(t, err)
		return th
	}
	title := func(th *thread, s string) {
		b, _ := json.Marshal(map[string]any{"type": "session_info_update", "title": s})
		f.s.onFrame(f.ctx, th, orb, b)
	}
	icon := func(prj string) *string {
		p, err := f.st.Q.GetProject(f.ctx, prj)
		require.NoError(t, err)
		return p.Icon
	}

	empty := mkProject(nil)
	th := mkThread(empty)
	title(th, "普通标题")
	assert.Nil(t, icon(empty), "不是卡片格式：不采用")
	title(th, "🧪 FLAKY: 修复不稳定的重连测试")
	require.NotNil(t, icon(empty))
	assert.Equal(t, "🧪", *icon(empty))
	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	assert.Equal(t, "🧪 FLAKY: 修复不稳定的重连测试", row.Title, "标题照原样保存")
	title(th, "\U000F0668 WS: 重构重连")
	assert.Equal(t, "🧪", *icon(empty), "只填一次")

	manual := "\uf121"
	picked := mkProject(&manual)
	title(mkThread(picked), "🧪 FLAKY: 另一个")
	assert.Equal(t, manual, *icon(picked), "不覆盖手选")
}
