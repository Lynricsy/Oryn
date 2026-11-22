package research

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

func mcpOnly(tools map[string][]string) func(*apigen.ResearchSettings) {
	return func(in *apigen.ResearchSettings) {
		in.Web, in.Mine, in.Internal, in.Mcp = false, false, false, true
		in.McpTools = tools
	}
}

func (f *fixture) mcpCursor() *int64 {
	f.t.Helper()
	row, err := f.st.Q.GetResearchSettings(f.ctx)
	require.NoError(f.t, err)
	return row.McpCursor
}

func TestMcpNotDueWithoutTools(t *testing.T) {
	f := newFixture(t)
	f.enable(mcpOnly(map[string][]string{}))
	f.s.step(f.ctx)
	assert.Zero(t, f.host.deliveries())
	f.enable(func(in *apigen.ResearchSettings) {
		mcpOnly(map[string][]string{"mail": {"list_messages"}})(in)
		in.Mcp = false
	})
	f.s.step(f.ctx)
	assert.Zero(t, f.host.deliveries(), "MCP 检查关着")
}

func TestMcpFindingsAreRestricted(t *testing.T) {
	f := newFixture(t)
	f.enable(mcpOnly(map[string][]string{"mail": {"list_messages", "read_message"}}))
	f.s.step(f.ctx)
	require.Equal(t, PipelineMCP, f.s.CurrentPipeline(RolePrivate))
	d := f.host.lastDelivery(t)
	assert.True(t, strings.HasPrefix(d.prompt, "[Oryn research task · mcp]"))
	assert.Contains(t, d.prompt, "- mail: list_messages, read_message")
	assert.Contains(t, d.prompt, "since: "+f.clk.Now().Add(-mcpSince).UTC().Format(time.RFC3339), "第一次从 24 小时前看起")
	f.reply(d.runID, `{"findings":[{"title":"Invoice due Friday","summary":"Pay the hosting invoice","source":"mail"}]}`)
	require.Len(t, f.host.findings, 1)
	fd := f.host.findings[0]
	assert.True(t, fd.Restricted)
	assert.Equal(t, PipelineMCP, fd.Pipeline)
	assert.Equal(t, "Source: mail\nPay the hosting invoice", fd.Summary)
}

func TestMcpCursorAdvancesOnSuccessOnly(t *testing.T) {
	f := newFixture(t)
	f.enable(mcpOnly(map[string][]string{"mail": {"list_messages"}}))
	f.s.step(f.ctx)
	d := f.host.lastDelivery(t)
	f.s.TurnEnded(threads.TurnEvent{Tag: d.runID, Status: threads.TurnAborted})
	f.s.step(f.ctx)
	assert.Nil(t, f.mcpCursor(), "失败不推进")
	assert.Equal(t, 1, f.host.deliveries(), "失败后冷却")

	f.clk.Add(mcpEvery + time.Minute)
	f.s.step(f.ctx)
	d = f.host.lastDelivery(t)
	started := f.clk.Now()
	f.clk.Add(time.Minute)
	f.reply(d.runID, `{"findings":[]}`)
	cur := f.mcpCursor()
	require.NotNil(t, cur)
	assert.Equal(t, started.UnixMilli(), *cur)
	_, outcome, _ := f.run(d.runID)
	assert.Equal(t, "nothing", outcome)
}

// 所选服务器在研究员里没就绪：不投递提示词、运行失败、游标不动、停掉研究员，冷却一个周期后再试。
func TestMcpNotReadySkipsDelivery(t *testing.T) {
	f := newFixture(t)
	f.enable(mcpOnly(map[string][]string{"mail": {"list_messages"}, "cal": {"list_events"}}))
	f.host.prepareErr = errors.New("MCP servers not ready: mail")
	f.s.step(f.ctx)
	assert.Zero(t, f.host.deliveries())
	require.Equal(t, [][]string{{"cal", "mail"}}, f.host.prepared)
	assert.Equal(t, []string{RolePrivate}, f.host.stops)
	runs, err := f.st.Q.ListResearchRuns(f.ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "failed", runs[0].Status)
	assert.Contains(t, deref(runs[0].Error), "not ready")
	assert.Nil(t, f.mcpCursor())
	assert.Empty(t, f.s.CurrentPipeline(RolePrivate))

	f.host.prepareErr = nil
	f.s.step(f.ctx)
	assert.Len(t, f.host.prepared, 1, "失败后冷却")
	f.clk.Add(mcpEvery + time.Minute)
	f.s.step(f.ctx)
	assert.Equal(t, 1, f.host.deliveries())
}

// 研究员报了用不了的服务器：不发发现、运行失败、游标不动。
func TestMcpUnavailableKeepsCursor(t *testing.T) {
	f := newFixture(t)
	f.enable(mcpOnly(map[string][]string{"mail": {"list_messages"}}))
	f.s.step(f.ctx)
	d := f.host.lastDelivery(t)
	assert.Contains(t, d.prompt, `"unavailable"`)
	f.reply(d.runID, `{"findings":[{"title":"x","summary":"y","source":"mail"}],"unavailable":["mail"]}`)
	assert.Empty(t, f.host.findings)
	assert.Nil(t, f.mcpCursor())
	status, _, errMsg := f.run(d.runID)
	assert.Equal(t, "failed", status)
	assert.Contains(t, errMsg, "unavailable: mail")
}

// MCP 运行结束后紧跟一条到期的内部运行：研究员上一代 agent 停稳之前不发起（不能落进带 MCP 凭据的旧进程）。
func TestMcpThenInternalUsesFreshAgent(t *testing.T) {
	f := newFixture(t)
	f.enable(func(in *apigen.ResearchSettings) {
		mcpOnly(map[string][]string{"mail": {"list_messages"}})(in)
		in.Internal = true
	})
	f.thread("T-w", "work", f.clk.Now().Add(-time.Minute))
	f.s.step(f.ctx)
	require.Equal(t, PipelineMCP, f.s.CurrentPipeline(RolePrivate), "MCP 排在内部关联之前")
	d := f.host.lastDelivery(t)

	f.host.mu.Lock()
	f.host.warm[RolePrivate] = true // StopResearcher 之后 agent 还没停稳
	f.host.mu.Unlock()
	f.reply(d.runID, `{"findings":[]}`)
	assert.Equal(t, 1, f.host.deliveries(), "上一代没停稳：不发起")
	assert.Empty(t, f.s.CurrentPipeline(RolePrivate))
	f.host.mu.Lock()
	f.host.warm[RolePrivate] = false
	f.host.mu.Unlock()
	f.s.step(f.ctx)
	assert.Equal(t, 2, f.host.deliveries())
	assert.Equal(t, PipelineInternal, f.s.CurrentPipeline(RolePrivate))
}
