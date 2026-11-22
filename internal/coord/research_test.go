package coord

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/research"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// fakeResearch 是测试用的研究服务：按角色给出当前管线。
type fakeResearch struct {
	mu       sync.Mutex
	pipeline map[string]string
	topics   []research.TopicInput
	err      error
}

func (f *fakeResearch) CurrentPipeline(role string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pipeline[role]
}

func (f *fakeResearch) set(role, pipeline string) {
	f.mu.Lock()
	f.pipeline[role] = pipeline
	f.mu.Unlock()
}

func (f *fakeResearch) CreateTopic(_ context.Context, in research.TopicInput) (apigen.ResearchTopic, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return apigen.ResearchTopic{}, f.err
	}
	f.topics = append(f.topics, in)
	return apigen.ResearchTopic{ID: "rtp_test", Title: in.Title}, nil
}

func researchEnv(t *testing.T, hold bool) (*env, *fakeResearch) {
	t.Helper()
	e := newEnv(t, hold, func(c *Config) { c.MCPToken, c.ResearchMCPToken = "mcp-assistant", "mcp-research" })
	fr := &fakeResearch{pipeline: map[string]string{}}
	e.c.SetResearch(fr, fr)
	return e, fr
}

func TestResearcherProfile(t *testing.T) {
	e, fr := researchEnv(t, true)
	webID, err := e.c.EnsureResearcher(e.ctx, research.RoleWeb)
	require.NoError(t, err)
	privID, err := e.c.EnsureResearcher(e.ctx, research.RolePrivate)
	require.NoError(t, err)
	row := e.row(privID)
	assert.EqualValues(t, 1, row.Hidden)
	assert.Equal(t, "private", deref(row.Research))

	prof, err := e.c.AgentProfile(e.ctx, webID)
	require.NoError(t, err)
	assert.Equal(t, research.VariantResearchWeb, prof.Variant)
	assert.Empty(t, prof.Tools)
	assert.False(t, prof.Web)
	assert.Empty(t, prof.MCPToken)
	assert.Equal(t, researchWebPrompt, prof.SystemPrompt)

	for _, pipeline := range []string{"", research.PipelineMine} {
		fr.set(research.RolePrivate, pipeline)
		prof, err = e.c.AgentProfile(e.ctx, privID)
		require.NoError(t, err)
		assert.Equal(t, research.VariantResearchPrivate, prof.Variant)
		assert.Empty(t, prof.Tools, "挖掘运行没有工具")
		assert.Empty(t, prof.MCPToken)
	}

	fr.set(research.RolePrivate, research.PipelineInternal)
	prof, err = e.c.AgentProfile(e.ctx, privID)
	require.NoError(t, err)
	// 助手记忆关着：没有三个记忆工具
	names := toolNames(prof.Tools)
	sort.Strings(names)
	assert.Equal(t, []string{ToolGetStats, ToolListProjects, ToolListSchedules, ToolListThreads, ToolReadThread}, names)
	for _, d := range prof.Tools {
		assert.Equal(t, "essential", d["loadMode"])
	}
	assert.Empty(t, prof.MCPToken)

	fr.set(research.RolePrivate, research.PipelineMCP)
	prof, err = e.c.AgentProfile(e.ctx, privID)
	require.NoError(t, err)
	assert.Len(t, prof.Tools, 5)
	assert.Equal(t, "mcp-research", prof.MCPToken, "只有研究凭据")
}

func TestResearcherToolGate(t *testing.T) {
	e, fr := researchEnv(t, true)
	privID, err := e.c.EnsureResearcher(e.ctx, research.RolePrivate)
	require.NoError(t, err)
	webID, err := e.c.EnsureResearcher(e.ctx, research.RoleWeb)
	require.NoError(t, err)
	call := func(id, role, name string) threads.HostToolResult {
		return e.c.HostToolCall(e.ctx, threads.HostToolCall{ThreadID: id, Kind: threads.KindAssistant, Research: role,
			CallID: "h", ToolCallID: "tc", Name: name, Args: json.RawMessage(`{}`)})
	}
	fr.set(research.RolePrivate, research.PipelineInternal)
	errText(t, call(privID, research.RolePrivate, ToolCreateThread), "unknown_tool")
	errText(t, call(privID, research.RolePrivate, ToolStaySilent), "unknown_tool")
	assert.False(t, call(privID, research.RolePrivate, ToolListThreads).IsError)

	fr.set(research.RolePrivate, research.PipelineMine)
	errText(t, call(privID, research.RolePrivate, ToolListThreads), "unknown_tool")
	errText(t, call(webID, research.RoleWeb, ToolListThreads), "unknown_tool")
}

func TestEnsureResearcherOnce(t *testing.T) {
	e, _ := researchEnv(t, true)
	var wg sync.WaitGroup
	got := make([]string, 4)
	for i := range got {
		wg.Go(func() {
			id, err := e.c.EnsureResearcher(e.ctx, research.RoleWeb)
			assert.NoError(t, err)
			got[i] = id
		})
	}
	wg.Wait()
	for _, id := range got {
		assert.Equal(t, got[0], id)
	}
	ids, err := e.c.ResearcherThreads(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{got[0]}, ids)
	_, err = e.c.EnsureResearcher(e.ctx, "other")
	require.Error(t, err)
}

func TestStopResearcher(t *testing.T) {
	e, _ := researchEnv(t, false)
	assert.True(t, e.c.ResearcherCold(e.ctx, research.RoleWeb), "对话不存在也算冷")
	require.NoError(t, e.c.DeliverResearch(e.ctx, research.RoleWeb, "rsr_1", "[Oryn research task] hello"))
	assert.False(t, e.c.ResearcherCold(e.ctx, research.RoleWeb))
	id, err := e.c.EnsureResearcher(e.ctx, research.RoleWeb)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, known := e.th.AgentVariant(e.ctx, id)
		return known
	}, 10*time.Second, 20*time.Millisecond, "研究员 agent 起来")
	v, _ := e.th.AgentVariant(e.ctx, id)
	assert.Equal(t, research.VariantResearchWeb, v)
	e.c.StopResearcher(e.ctx, research.RoleWeb)
	require.Eventually(t, func() bool { return e.c.ResearcherCold(e.ctx, research.RoleWeb) }, 10*time.Second, 20*time.Millisecond)
}

// 摘录：主人消息与每个回合最后的助手文本；自动化消息及其回复不取；afterSeq 之前的不取。
func TestResearchExcerpt(t *testing.T) {
	e, _ := researchEnv(t, true)
	conv := e.assistant()
	p := e.entry(conv.ID, "", "user", "old question", "", "", 1)
	p = e.entry(conv.ID, p, "assistant", "old answer", "stop", "", 2)
	first, err := e.c.Excerpt(e.ctx, conv.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, "主人：old question\n\n助手：old answer", first.Text)
	assert.Equal(t, 1, first.OwnerMessages)

	p = e.entry(conv.ID, p, "user", "When does Node 24 become LTS?", "", "", 3)
	p = e.entry(conv.ID, p, "assistant", "thinking…", "toolUse", "", 4)
	p = e.entry(conv.ID, p, "assistant", "October 2025, I believe.", "stop", "", 5)
	p = e.entry(conv.ID, p, "user", "[Oryn automation results · automatic] 1 result(s).", "", "", 6)
	e.entry(conv.ID, p, "assistant", "AUTOMATION REPLY", "stop", "", 7)
	ex, err := e.c.Excerpt(e.ctx, conv.ID, first.LastSeq)
	require.NoError(t, err)
	assert.Equal(t, "主人：When does Node 24 become LTS?\n\n助手：October 2025, I believe.", ex.Text)
	assert.Equal(t, 1, ex.OwnerMessages)
	assert.Greater(t, ex.LastSeq, first.LastSeq)
	none, err := e.c.Excerpt(e.ctx, conv.ID, ex.LastSeq)
	require.NoError(t, err)
	assert.Empty(t, none.Text)
	assert.Zero(t, none.OwnerMessages)
}
