package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// newConvPrompt 是一组都没加载的全局助手对话的系统提示词开头：基础提示词、写作块指引与按需工具目录。
func newConvPrompt(t *testing.T, e *env, threadID string, web bool) string {
	t.Helper()
	row, err := e.st.Q.GetThread(e.ctx, threadID)
	require.NoError(t, err)
	_, avail := splitTools(ToolDefs(e.c.assistantToolSet(e.ctx, row, false)), nil)
	return SystemPrompt(nil, "", threadID, web) + "\n\n" + viewTools().WritingGuide + "\n\n" + toolGroupsPrompt(avail, nil)
}

// 新的助手对话只下发常驻工具与 load_tools，按需组列在目录里；加载后记进线程、agent 重启也带着，用法进系统提示词。
func TestLoadToolsGroups(t *testing.T) {
	e := newEnv(t, true)
	conv := e.assistant()

	p, err := e.c.AgentProfile(e.ctx, conv.ID)
	require.NoError(t, err)
	names := toolNames(p.Tools)
	for _, n := range []string{ToolListThreads, ToolReadThread, ToolCreateThread, ToolSendMessage, ToolArchiveThread, ToolListProjects, ToolOryn} {
		assert.Contains(t, names, n, "常驻")
	}
	for _, n := range []string{ToolCreateArtifact, ToolRenderUI, ToolComputerStatus, ToolSetSchedule, ToolGetStats, ToolSaveFile} {
		assert.NotContains(t, names, n, "按需")
	}
	assert.Equal(t, ToolLoadTools, names[len(names)-1])
	assert.Contains(t, p.SystemPrompt, onDemandHeader)
	assert.Contains(t, p.SystemPrompt, "- artifacts (create_artifact, read_artifact, update_artifact, list_artifacts, read_artifact_guide, screenshot_artifact): ")
	assert.NotContains(t, p.SystemPrompt, assistantArtifacts)

	res := e.call(conv, ToolLoadTools, `{"groups":["artifacts","schedules"]}`, true)
	require.False(t, res.IsError, res.Text)
	assert.Contains(t, res.Text, "## artifacts\nLoaded: create_artifact, ")
	assert.Contains(t, res.Text, assistantArtifacts)
	assert.Contains(t, res.Text, "## schedules\nLoaded: set_schedule, list_schedules.")
	assert.Equal(t, map[string]any{"loaded": []string{"artifacts", "schedules"}}, res.Details)

	reloaded, err := e.c.AgentProfile(e.ctx, conv.ID)
	require.NoError(t, err)
	assert.Equal(t, toolNames(res.HostTools), toolNames(reloaded.Tools), "agent 重启时带着已加载的组")
	assert.Contains(t, toolNames(reloaded.Tools), ToolCreateArtifact)
	assert.Contains(t, toolNames(reloaded.Tools), ToolSetSchedule)
	assert.NotContains(t, toolNames(reloaded.Tools), ToolRenderUI)
	assert.Contains(t, reloaded.SystemPrompt, assistantArtifacts)
	assert.NotContains(t, reloaded.SystemPrompt, "- artifacts (")
	assert.Contains(t, reloaded.SystemPrompt, "- views (")

	// 已加载的组再要一次：不报错、照样给整张表（orbd 复用旧初始化帧时据此补回）
	again := e.call(conv, ToolLoadTools, `{"groups":["artifacts"]}`, true)
	require.False(t, again.IsError, again.Text)
	assert.Contains(t, again.Text, "Already loaded")
	assert.Equal(t, toolNames(reloaded.Tools), toolNames(again.HostTools))

	bad := e.call(conv, ToolLoadTools, `{"groups":["nope"]}`, true)
	assert.True(t, bad.IsError)
	assert.Nil(t, bad.HostTools)

	sp := e.space("s", "")
	sprof, err := e.c.AgentProfile(e.ctx, sp.ThreadID)
	require.NoError(t, err)
	assert.NotContains(t, toolNames(sprof.Tools), ToolLoadTools, "Space 协调者不拆工具表")
	assert.Contains(t, toolNames(sprof.Tools), ToolSetSchedule)
	spRes := e.call(e.spaceChat(sp), ToolLoadTools, `{"groups":["artifacts"]}`, true)
	assert.True(t, spRes.IsError, "Space 协调者没有 load_tools")
}

// 主线每段从空上下文开始：每次启动清空已加载的组。
func TestLoadToolsMainResetsPerEpisode(t *testing.T) {
	e := newEnv(t, true)
	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	res := e.call(main, ToolLoadTools, `{"groups":["computer"]}`, true)
	require.False(t, res.IsError, res.Text)
	assert.Contains(t, toolNames(res.HostTools), ToolComputerStatus)

	p, err := e.c.AgentProfile(e.ctx, main.ID)
	require.NoError(t, err)
	assert.NotContains(t, toolNames(p.Tools), ToolComputerStatus)
	assert.Contains(t, p.SystemPrompt, "- computer (")
	row, err := e.st.Q.GetThread(e.ctx, main.ID)
	require.NoError(t, err)
	assert.Nil(t, row.ToolGroups)
}

// splitTools 保持常驻与已加载工具的原顺序、末尾加 load_tools；toolGroupsPrompt 先给已加载组的用法，再列目录。
func TestToolGroupsPromptOrder(t *testing.T) {
	all := []apigen.OmpFrame{{"name": ToolCreateThread}, {"name": ToolSaveFile}, {"name": ToolGetStats}}
	active, avail := splitTools(all, map[string]bool{"files": true})
	assert.Equal(t, []string{ToolCreateThread, ToolSaveFile, ToolLoadTools}, toolNames(active))
	got := toolGroupsPrompt(avail, map[string]bool{"files": true})
	assert.Equal(t, filesGuide+"\n\n"+onDemandHeader+"\n- stats (get_stats): usage, cost, tool and output statistics.", got)
	none, avail := splitTools([]apigen.OmpFrame{{"name": ToolCreateThread}}, nil)
	assert.Equal(t, []string{ToolCreateThread}, toolNames(none), "没有可用组时不加 load_tools")
	assert.Empty(t, toolGroupsPrompt(avail, nil))
}
