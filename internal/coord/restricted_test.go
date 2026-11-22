package coord

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/research"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// M26 步骤 1.4：研究发现进主线与受限的一段。

func restrictedFinding(title string) research.Finding {
	return research.Finding{RunID: "rsr_1", Pipeline: research.PipelineWeb, Title: title, Summary: "S1",
		Sources: []apigen.ResearchSource{{URL: "https://example.com/a", Title: "A"}}, ThreadID: "T-src", Restricted: true}
}

func countCalls(calls []string, method string) int {
	n := 0
	for _, c := range calls {
		if c == method {
			n++
		}
	}
	return n
}

// waitSent 反复触发最后一个定时器（没有就直接 flushInbox），直到主线收到一条收件消息。
func waitSent(t *testing.T, e *env, fa *fakeAfter, sent chan string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !fa.fireLast() {
			e.c.flushInbox(e.ctx)
		}
		select {
		case text := <-sent:
			return text
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("主线没有收到收件消息")
	return ""
}

// 受限收件只投给 orbd 报回 main_restricted 的主线 agent：完整表的 agent 先停、以受限变体重启后才投；该回合结束后
// 主线恢复完整工具表。
func TestRestrictedDeliversOnlyToRestrictedAgent(t *testing.T) {
	e := newEnv(t, false)
	fa := &fakeAfter{}
	e.c.cfg.AfterFunc = fa.AfterFunc
	sent := make(chan string, 8)
	e.c.sentInbox = func(_, text string) { sent <- text }
	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	require.NoError(t, e.th.KeepWarm(e.ctx, main.ID))
	require.Eventually(t, func() bool {
		v, known := e.th.AgentVariant(e.ctx, main.ID)
		return known && v == ""
	}, 10*time.Second, 20*time.Millisecond, "主线以完整表起来")

	e.c.PostFinding(e.ctx, restrictedFinding("T1"))
	require.True(t, fa.fireLast(), "合并定时器")
	select {
	case got := <-sent:
		t.Fatalf("完整表的主线不得收到受限收件：%s", got)
	default:
	}
	assert.Equal(t, 1, e.pendingInbox())

	text := waitSent(t, e, fa, sent)
	assert.True(t, strings.HasPrefix(text, researchHeader+" 1 finding(s).\n"), text)
	starts := e.orbs.Starts(main.ID)
	require.GreaterOrEqual(t, len(starts), 2)
	last := starts[len(starts)-1]
	require.NotNil(t, last.Variant)
	assert.Equal(t, apigen.AgentVariantMainRestricted, *last.Variant)
	assert.Nil(t, last.Web)
	assert.Nil(t, last.McpToken)
	assert.GreaterOrEqual(t, countCalls(e.orbs.Calls(), "agent.stop"), 1, "完整表的 agent 先停")
	for _, s := range starts[:len(starts)-1] {
		assert.Nil(t, s.Variant, "受限变体只在最后一次启动")
	}
	assert.Zero(t, e.pendingInbox())

	stops := countCalls(e.orbs.Calls(), "agent.stop")
	require.Eventually(t, func() bool {
		prof, err := e.c.AgentProfile(e.ctx, main.ID)
		return err == nil && prof.Variant == "" && countCalls(e.orbs.Calls(), "agent.stop") > stops
	}, 15*time.Second, 50*time.Millisecond, "回合结束后恢复完整表，受限的 agent 在静止后停掉")
	prof, err := e.c.AgentProfile(e.ctx, main.ID)
	require.NoError(t, err)
	assert.Contains(t, toolNames(prof.Tools), ToolCreateThread)
	assert.True(t, prof.Web)
}

// 完整表的 agent 停不下来（agent.stop 卡住）时变体未知：反复重试也不投递；停下后才以受限变体投递。
func TestRestrictedWaitsForStop(t *testing.T) {
	e := newEnv(t, false)
	fa := &fakeAfter{}
	e.c.cfg.AfterFunc = fa.AfterFunc
	sent := make(chan string, 8)
	e.c.sentInbox = func(_, text string) { sent <- text }
	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	require.NoError(t, e.th.KeepWarm(e.ctx, main.ID))
	require.Eventually(t, func() bool {
		_, known := e.th.AgentVariant(e.ctx, main.ID)
		return known
	}, 10*time.Second, 20*time.Millisecond)

	arrived, release := e.orbs.GateCall("agent.stop")
	e.c.PostFinding(e.ctx, restrictedFinding("T1"))
	require.True(t, fa.fireLast())
	<-arrived
	for range 5 {
		if !fa.fireLast() {
			e.c.flushInbox(e.ctx)
		}
		select {
		case got := <-sent:
			t.Fatalf("停止在途时不得投递：%s", got)
		case <-time.After(50 * time.Millisecond):
		}
	}
	assert.Equal(t, 1, e.pendingInbox())
	release()
	text := waitSent(t, e, fa, sent)
	assert.True(t, strings.HasPrefix(text, researchHeader), text)
	starts := e.orbs.Starts(main.ID)
	require.NotNil(t, starts[len(starts)-1].Variant)
}

func TestRestrictedRejectsWriteTools(t *testing.T) {
	e := newEnv(t, true)
	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	call := func(name string) threads.HostToolResult {
		return e.c.HostToolCall(e.ctx, threads.HostToolCall{ThreadID: main.ID, Kind: threads.KindAssistant,
			Variant: research.VariantMainRestricted, CallID: "h", ToolCallID: "tc", Name: name, Args: json.RawMessage(`{}`)})
	}
	errText(t, call(ToolCreateThread), CodeNotAvailable)
	errText(t, call(ToolOryn), CodeNotAvailable)
	assert.False(t, call(ToolListThreads).IsError)

	// 受限的 Profile：只读工具加 stay_silent，不联网、没有 MCP 凭据
	e.c.mu.Lock()
	e.c.restrict.want = true
	e.c.mu.Unlock()
	prof, err := e.c.AgentProfile(e.ctx, main.ID)
	require.NoError(t, err)
	assert.Equal(t, research.VariantMainRestricted, prof.Variant)
	assert.False(t, prof.Web)
	assert.Empty(t, prof.MCPToken)
	for _, n := range toolNames(prof.Tools) {
		assert.True(t, restrictedMainTools[n], n)
	}
	assert.Contains(t, toolNames(prof.Tools), ToolStaySilent)
	assert.Contains(t, prof.SystemPrompt, "## Limited turn")
	assert.Contains(t, prof.SystemPrompt, "Web access is turned off")
}

// 受限投递的回合结束后，主线下一次启动取完整的 Profile。
func TestRestrictedEndsAfterTaggedTurn(t *testing.T) {
	e := newEnv(t, true)
	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	e.c.mu.Lock()
	e.c.restrict.want = true
	e.c.restrict.inflight = map[string]*restrictedSend{"k1": {since: e.clk.Now()}}
	e.c.mu.Unlock()
	e.c.TurnEnded(threads.TurnEvent{ThreadID: main.ID, Tag: "inr_other", Status: threads.TurnCompleted})
	prof, err := e.c.AgentProfile(e.ctx, main.ID)
	require.NoError(t, err)
	assert.Equal(t, research.VariantMainRestricted, prof.Variant, "别的标签不结束")

	e.c.TurnEnded(threads.TurnEvent{ThreadID: main.ID, Tag: inboxTagPrefix + "k1", Status: threads.TurnCompleted})
	prof, err = e.c.AgentProfile(e.ctx, main.ID)
	require.NoError(t, err)
	assert.Empty(t, prof.Variant)
	assert.Contains(t, toolNames(prof.Tools), ToolCreateThread)
}

// 保险：受限消息一直排着（agent 起不来），超过 mainRestrictMax 后撤回、收件回到待投，占位清空。
func TestRestrictedValveCancelsQueued(t *testing.T) {
	e := newEnv(t, true)
	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	e.c.PostFinding(e.ctx, restrictedFinding("T1"))
	rows, err := e.st.Q.ListPendingAssistantInbox(e.ctx, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	cancel, err := e.th.Deliver(e.ctx, main.ID, threads.AutoMessage{Text: "[Oryn research findings · automatic] 1 finding(s).", Tag: inboxTagPrefix + "k1"}, nil)
	require.NoError(t, err)
	e.c.markInbox(e.ctx, rows)
	e.c.mu.Lock()
	e.c.restrict.want = true
	e.c.restrict.inflight = map[string]*restrictedSend{"k1": {cancel: cancel, rows: []string{rows[0].ID}, since: e.clk.Now()}}
	e.c.mu.Unlock()
	require.Zero(t, e.pendingInbox())

	e.clk.Add(mainRestrictMax + time.Minute)
	e.c.retryInbox(e.ctx)
	assert.Equal(t, 1, e.pendingInbox(), "撤回后回到待投")
	e.c.flushInbox(e.ctx) // 合并定时器到点
	e.c.mu.Lock()
	assert.Empty(t, e.c.restrict.inflight)
	e.c.mu.Unlock()
	prof, err := e.c.AgentProfile(e.ctx, main.ID)
	require.NoError(t, err)
	assert.Equal(t, research.VariantMainRestricted, prof.Variant, "又有待投的受限收件：仍取受限 Profile")
}

// 主线 agent 起不来时，排在受限收件之后的定时结果照常投递，受限收件留在待投。
func TestRestrictedDoesNotBlockAutomation(t *testing.T) {
	e, fa, sent := inboxEnv(t)
	id := e.workRow()
	e.c.PostFinding(e.ctx, restrictedFinding("T1"))
	e.c.PostToMain(e.ctx, schedule.HandOff{Kind: "schedule", SourceID: "sch_1", SourceName: "nightly", ThreadID: id, Outcome: "completed", Text: "ok"})
	require.True(t, fa.fireLast())
	text := <-sent
	assert.True(t, strings.HasPrefix(text, inboxStatusHeader+" 1 result(s).\n"), text)
	rows, err := e.st.Q.ListPendingAssistantInbox(e.ctx, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "research", rows[0].Kind)
	assert.EqualValues(t, 1, rows[0].Restricted)
}

// 定时结果与不受限的研究发现分成两条自动消息，头行不同；研究消息的 JSON 键名是 title、summary、threadId。
func TestResearchInboxBatchesSeparately(t *testing.T) {
	e, fa, sent := inboxEnv(t)
	id := e.workRow()
	e.c.PostToMain(e.ctx, schedule.HandOff{Kind: "schedule", SourceID: "sch_1", SourceName: "nightly", ThreadID: id, Outcome: "completed", Text: "ok"})
	e.c.PostFinding(e.ctx, research.Finding{RunID: "rsr_2", Pipeline: research.PipelineInternal, Title: "Conflict", Summary: "A vs B",
		ThreadID: id, Threads: []string{id}})
	require.True(t, fa.fireLast())
	first := <-sent
	assert.True(t, strings.HasPrefix(first, inboxStatusHeader+" 1 result(s).\n"), first)
	require.True(t, fa.fireLast(), "还有剩余：再排一次")
	second := <-sent
	assert.True(t, strings.HasPrefix(second, researchHeader+" 1 finding(s).\n"), second)
	start, end := strings.Index(second, "{"), strings.LastIndex(second, "}")
	var v struct {
		Findings []map[string]any `json:"findings"`
	}
	require.NoError(t, json.Unmarshal([]byte(second[start:end+1]), &v))
	require.Len(t, v.Findings, 1)
	keys := make([]string, 0, len(v.Findings[0]))
	for k := range v.Findings[0] {
		keys = append(keys, k)
	}
	for _, k := range []string{"title", "summary", "threadId"} {
		assert.True(t, slices.Contains(keys, k), k)
	}
	assert.Zero(t, e.pendingInbox())
}
