package research

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

func internalOnly(in *apigen.ResearchSettings) { in.Web, in.Mine, in.Mcp = false, false, false }

func (f *fixture) internalCursor() *int64 {
	f.t.Helper()
	row, err := f.st.Q.GetResearchSettings(f.ctx)
	require.NoError(f.t, err)
	return row.InternalCursor
}

func TestInternalSkipsWithoutActivity(t *testing.T) {
	f := newFixture(t)
	f.enable(internalOnly)
	f.thread("T-asst", "assistant", f.clk.Now().Add(-time.Minute)) // 助手对话不算活动
	f.s.step(f.ctx)
	assert.Zero(t, f.host.deliveries())
	cur := f.internalCursor()
	require.NotNil(t, cur)
	assert.Equal(t, f.clk.Now().UnixMilli(), *cur, "游标推进到现在")
	f.clk.Add(time.Hour)
	f.thread("T-w", "work", f.clk.Now())
	f.s.step(f.ctx)
	assert.Zero(t, f.host.deliveries(), "距上次不到 2 小时")
}

// internalRun 让一个工作线程有新活动并发起内部运行。
func (f *fixture) internalRun() delivery {
	f.t.Helper()
	f.enable(internalOnly)
	f.thread("T-w"+f.clk.Now().Format("150405"), "work", f.clk.Now().Add(-time.Minute))
	f.s.step(f.ctx)
	require.Equal(f.t, PipelineInternal, f.s.CurrentPipeline(RolePrivate))
	return f.host.lastDelivery(f.t)
}

func TestInternalFindingsAreUnrestricted(t *testing.T) {
	f := newFixture(t)
	d := f.internalRun()
	started := f.clk.Now()
	f.clk.Add(3 * time.Minute)
	f.reply(d.runID, `{"findings":[{"title":"Conflicting API decision","summary":"A says v1, B says v2","threads":["T-a","T-b"]},{"title":"","summary":"dropped"}]}`)
	require.Len(t, f.host.findings, 1)
	fd := f.host.findings[0]
	assert.False(t, fd.Restricted)
	assert.Equal(t, PipelineInternal, fd.Pipeline)
	assert.Equal(t, "T-a", fd.ThreadID)
	assert.Equal(t, []string{"T-a", "T-b"}, fd.Threads)
	cur := f.internalCursor()
	require.NotNil(t, cur)
	assert.Equal(t, started.UnixMilli(), *cur, "游标推进到运行开始的时间")
	_, outcome, _ := f.run(d.runID)
	assert.Equal(t, "findings:1", outcome)
}

func TestInternalCapsFindings(t *testing.T) {
	f := newFixture(t)
	d := f.internalRun()
	var fs []map[string]any
	for i := range 5 {
		fs = append(fs, map[string]any{"title": "F" + string(rune('a'+i)), "summary": "s"})
	}
	b, _ := json.Marshal(map[string]any{"findings": fs})
	f.reply(d.runID, string(b))
	assert.Len(t, f.host.findings, internalMaxFindings)
}

func TestInternalPromptListsReported(t *testing.T) {
	f := newFixture(t)
	d := f.internalRun()
	assert.True(t, strings.HasPrefix(d.prompt, "[Oryn research task · internal]"))
	f.reply(d.runID, `{"findings":[{"title":"Stuck migration","summary":"waiting on X which merged","threads":["T-1"]}]}`)
	f.clk.Add(internalEvery + time.Minute)
	d2 := f.internalRun()
	assert.NotEqual(t, d.runID, d2.runID)
	assert.Contains(t, d2.prompt, "- Stuck migration")
}

func TestInternalFailureBacksOff(t *testing.T) {
	f := newFixture(t)
	d := f.internalRun()
	f.s.TurnEnded(threads.TurnEvent{Tag: d.runID, Status: threads.TurnError})
	f.s.step(f.ctx)
	assert.Equal(t, 1, f.host.deliveries(), "失败后冷却，不在下一个节拍重来")
	f.clk.Add(internalEvery + time.Minute)
	f.s.step(f.ctx)
	assert.Equal(t, 2, f.host.deliveries())
}
