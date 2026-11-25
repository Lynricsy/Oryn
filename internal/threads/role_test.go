package threads

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 带 role 新建线程：首批命令依次为 oryn_select_model{role, startup, thinkingLevel} → prompt，agent 就绪后按此顺序下发；
// 思考档位在同一帧里，不单独发 set_thinking_level（计划模式下它会改掉计划思考级别）。
func TestCreateWithRoleSelectsBeforePrompt(t *testing.T) {
	f := newFixture(t, Config{})
	out, err := f.s.Create(f.ctx, CreateParams{Prompt: "role hello", Role: " Cyrene ", ThinkingLevel: "high"})
	require.NoError(t, err)

	th, err := f.s.get(f.ctx, out.ID)
	require.NoError(t, err)
	a := fakeAgent(8)
	attach(th, a, "ready")
	th.mu.Lock()
	f.s.onReadyLocked(f.ctx, th)
	th.mu.Unlock()
	frames := afterMark(t, sent(a))
	require.GreaterOrEqual(t, len(frames), 2)
	assert.Contains(t, frames[0], `"oryn_select_model"`)
	assert.Contains(t, frames[0], `"role":"Cyrene"`, "role 名去掉首尾空白")
	assert.Contains(t, frames[0], `"startup":true`)
	assert.Contains(t, frames[0], `"thinkingLevel":"high"`)
	assert.Contains(t, frames[1], `"role hello"`)
	for _, f := range frames {
		assert.NotContains(t, f, `"set_thinking_level"`)
	}
}

// 不带 role 时首批命令与以前一致：只有 prompt。
func TestCreateWithoutRoleUnchanged(t *testing.T) {
	f := newFixture(t, Config{})
	out, err := f.s.Create(f.ctx, CreateParams{Prompt: "plain", ThinkingLevel: "high"})
	require.NoError(t, err)
	row, err := f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	assert.NotContains(t, string(row.StartupFrames), "oryn_select_model")
	assert.NotContains(t, string(row.StartupFrames), "set_thinking_level", "思考档位照旧走初始化帧")
}

func TestCreateRejectsBadRole(t *testing.T) {
	f := newFixture(t, Config{})
	for _, role := range []string{strings.Repeat("r", maxRoleLen+1), "a\nb"} {
		_, err := f.s.Create(f.ctx, CreateParams{Prompt: "x", Role: role})
		require.Error(t, err, role)
		assert.Contains(t, err.Error(), "invalid role")
	}
}

// 按 subagent 配置启动的工作线程：AgentRole / AppendSystemPrompt / AllowedTools 落库，并在启动后原样恢复。
func TestCreatePersistsAgentRoleConfig(t *testing.T) {
	f := newFixture(t, Config{})
	out, err := f.s.Create(f.ctx, CreateParams{
		Prompt:             "inspect repo",
		Role:               "smol",
		ThinkingLevel:      "medium",
		AgentRole:          "scout",
		AppendSystemPrompt: "# Agent Role: scout\nStay concise.",
		AllowedTools:       []string{" read ", "grep", "read", ""},
	})
	require.NoError(t, err)

	row, err := f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	require.NotNil(t, row.AgentRole)
	assert.Equal(t, "scout", *row.AgentRole)
	require.NotNil(t, row.AppendSystemPrompt)
	assert.Equal(t, "# Agent Role: scout\nStay concise.", *row.AppendSystemPrompt)
	require.NotNil(t, row.AllowedTools)
	assert.JSONEq(t, `["read","grep"]`, *row.AllowedTools)

	// 模拟重启后从库重新加载内存线程对象
	f.s.forget(out.ID)
	reloaded, err := f.s.get(f.ctx, out.ID)
	require.NoError(t, err)
	assert.Equal(t, "scout", reloaded.agentRole)
	assert.Equal(t, "# Agent Role: scout\nStay concise.", reloaded.appendSystemPrompt)
	assert.Equal(t, []string{"read", "grep"}, reloaded.allowedTools)
}
