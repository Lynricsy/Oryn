package coord

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M10 3.9：get_stats 的四种 kind、project / space 按名称或 id、Space 协调者的强制筛选与 orbs 被拒。
func TestGetStats(t *testing.T) {
	e := newEnv(t, true)
	proj := e.project("Alpha")
	sp := e.space("S", proj)
	chat := e.spaceChat(sp)
	asst := e.assistant()
	x := e.newOrb()
	in := e.threadOn(x, "", proj, sp.ID)
	out := e.threadOn(x, "", "", "")
	ts := e.clk.Now().Add(-time.Hour).UnixMilli()
	for i, r := range []struct {
		thread string
		cost   float64
	}{{in.ID, 1.0}, {out.ID, 2.0}} {
		_, err := e.st.Q.InsertUsageRecord(e.ctx, db.InsertUsageRecordParams{ThreadID: &r.thread, EntrySeq: int64(i + 1), Ts: ts, Provider: "p", Model: "m", CostUsd: r.cost})
		require.NoError(t, err)
	}
	_, err := e.st.Q.InsertToolCall(e.ctx, db.InsertToolCallParams{ThreadID: &in.ID, EntrySeq: 9, Ts: ts, Tool: "bash", IsError: true})
	require.NoError(t, err)

	cost := func(r map[string]any) float64 { return r["total"].(map[string]any)["costUsd"].(float64) }
	errText := func(args string, conv ...string) string {
		c := asst
		if len(conv) > 0 {
			c = chat
		}
		r := e.call(c, ToolGetStats, args, true)
		require.True(t, r.IsError, r.Text)
		return r.Text
	}

	// 全局助手：缺省 usage / groupBy=model / 近 7 天
	r := details(t, e.call(asst, ToolGetStats, `{}`, true))
	assert.Equal(t, "model", r["groupBy"])
	assert.InDelta(t, 3.0, cost(r), 1e-9)
	for _, ref := range []string{"Alpha", proj} {
		r = details(t, e.call(asst, ToolGetStats, `{"groupBy":"thread","project":"`+ref+`"}`, true))
		assert.InDelta(t, 1.0, cost(r), 1e-9, ref)
	}
	for _, ref := range []string{"S", sp.ID} {
		r = details(t, e.call(asst, ToolGetStats, `{"kind":"usage","space":"`+ref+`"}`, true))
		assert.InDelta(t, 1.0, cost(r), 1e-9, ref)
	}
	r = details(t, e.call(asst, ToolGetStats, `{"project":"none"}`, true))
	assert.InDelta(t, 2.0, cost(r), 1e-9)
	r = details(t, e.call(asst, ToolGetStats, `{"groupBy":"thread","limit":100}`, true))
	assert.Len(t, r["groups"], 2, "limit 超过 30 按上限，不报错")

	r = details(t, e.call(asst, ToolGetStats, `{"kind":"tools"}`, true))
	assert.EqualValues(t, 1, r["total"].(map[string]any)["errors"])
	r = details(t, e.call(asst, ToolGetStats, `{"kind":"orbs"}`, true))
	assert.Equal(t, "type", r["groupBy"])
	r = details(t, e.call(asst, ToolGetStats, `{"kind":"output","groupBy":"project"}`, true))
	assert.Equal(t, "project", r["groupBy"])

	assert.True(t, strings.HasPrefix(errText(`{"project":"Nope"}`), CodeNotFound), "未知项目")
	assert.True(t, strings.HasPrefix(errText(`{"space":"Nope"}`), CodeNotFound))
	assert.True(t, strings.HasPrefix(errText(`{"kind":"bogus"}`), CodeInvalid))
	assert.True(t, strings.HasPrefix(errText(`{"groupBy":"bogus"}`), CodeInvalid))
	assert.True(t, strings.HasPrefix(errText(`{"tz":"Mars/Phobos"}`), "invalid_timezone"))

	// Space 协调者：space 强制为本 Space（传入的 space 被忽略），orbs 被拒
	r = details(t, e.call(chat, ToolGetStats, `{"space":"none"}`, true))
	assert.InDelta(t, 1.0, cost(r), 1e-9)
	r = details(t, e.call(chat, ToolGetStats, `{"kind":"output","groupBy":"space"}`, true))
	groups := r["groups"].([]any)
	for _, g := range groups {
		assert.Equal(t, sp.ID, g.(map[string]any)["key"])
	}
	assert.True(t, strings.HasPrefix(errText(`{"kind":"orbs"}`, "space"), CodeForbidden))

	// 工具定义：Space 协调者看不到 orbs
	for _, tc := range []struct {
		inSpace bool
		kinds   []string
	}{{false, []string{"usage", "tools", "orbs", "output"}}, {true, []string{"usage", "tools", "output"}}} {
		for _, d := range ToolDefs(tc.inSpace) {
			if d["name"] != ToolGetStats {
				continue
			}
			props := d["parameters"].(obj)["properties"].(obj)
			assert.Equal(t, tc.kinds, props["kind"].(obj)["enum"])
			assert.Nil(t, d["parameters"].(obj)["required"], "全部参数可选")
		}
	}
}
