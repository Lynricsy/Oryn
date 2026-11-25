package threads

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 计划模式的临时模型 B 与思考级别不得经 syncModelLocked 写进线程行（DB）；
// 重启时 initFrames 总是把常驻基线 A（模型 + 思考级别）放进 open_session，由 Core 按计划态决定立即应用还是改作进入前基线，
// 不单独发会改掉计划思考级别的 set_thinking_level。
func TestPlanModeDoesNotOverwriteThreadModel(t *testing.T) {
	f := newFixture(t, Config{})
	th := f.thread(f.orb("ready"), StateIdle)

	// 1. 非计划态同步常驻基线 A（sonnet / medium）
	th.mu.Lock()
	f.s.syncModelLocked(th, json.RawMessage(`{
		"model":{"provider":"anthropic","id":"claude-sonnet-4-5"},
		"thinkingLevel":"medium",
		"planModeEnabled":false
	}`))
	th.mu.Unlock()

	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	require.NotNil(t, row.Model)
	require.NotNil(t, row.ThinkingLevel)
	assert.Equal(t, "anthropic/claude-sonnet-4-5", *row.Model)
	assert.Equal(t, "medium", *row.ThinkingLevel)

	// 2. 进入计划模式后 get_state 报临时计划模型 B（opus / high）→ 线程行与 DB 仍保持常驻基线 A
	th.mu.Lock()
	f.s.syncModelLocked(th, json.RawMessage(`{
		"model":{"provider":"anthropic","id":"claude-opus-4-6"},
		"thinkingLevel":"high",
		"planModeEnabled":true
	}`))
	inPlanFrames := initFrames(th, nil, true)
	th.mu.Unlock()

	row, err = f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	require.NotNil(t, row.Model)
	require.NotNil(t, row.ThinkingLevel)
	assert.Equal(t, "anthropic/claude-sonnet-4-5", *row.Model, "计划态临时模型不得写入线程行")
	assert.Equal(t, "medium", *row.ThinkingLevel, "计划态临时思考级别不得写入线程行")

	// 3. 计划态下重启：open_session 仍带常驻基线 A，交给 Core 改作进入前基线；没有单独的 set_thinking_level
	require.NotEmpty(t, inPlanFrames)
	assert.Equal(t, "open_session", inPlanFrames[0]["type"])
	assert.Equal(t, "anthropic", inPlanFrames[0]["provider"])
	assert.Equal(t, "claude-sonnet-4-5", inPlanFrames[0]["modelId"])
	assert.Equal(t, "medium", inPlanFrames[0]["thinkingLevel"])
	assert.NotContains(t, frameTypes(inPlanFrames), "set_thinking_level", "计划态重启不得发 set_thinking_level 覆盖计划思考级别")

	// 4. 退出计划模式后，get_state 报 planModeEnabled=false → 恢复正常同步与重启初始化帧
	th.mu.Lock()
	f.s.syncModelLocked(th, json.RawMessage(`{
		"model":{"provider":"anthropic","id":"claude-sonnet-4-5"},
		"thinkingLevel":"low",
		"planModeEnabled":false
	}`))
	normalFrames := initFrames(th, nil, true)
	th.mu.Unlock()

	row, err = f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	require.NotNil(t, row.ThinkingLevel)
	assert.Equal(t, "low", *row.ThinkingLevel, "退出计划模式后恢复正常同步")
	assert.Equal(t, "anthropic", normalFrames[0]["provider"])
	assert.Equal(t, "claude-sonnet-4-5", normalFrames[0]["modelId"])
	assert.Equal(t, "low", normalFrames[0]["thinkingLevel"])
	assert.NotContains(t, frameTypes(normalFrames), "set_thinking_level")
}

// Core 19.2.0 auto 思考模式：get_state 同时报 configuredThinkingLevel="auto" 与当前生效的 thinkingLevel="high" 时，
// 线程行应落库 "auto" 而非 "high"，确保 orb 重启后 open_session 保留 auto 而不是把会话覆写成固定档位。
func TestSyncModelPreservesAutoThinkingLevel(t *testing.T) {
	f := newFixture(t, Config{})
	th := f.thread(f.orb("ready"), StateIdle)

	th.mu.Lock()
	f.s.syncModelLocked(th, json.RawMessage(`{
		"model":{"provider":"anthropic","id":"claude-sonnet-4-5"},
		"thinkingLevel":"high",
		"configuredThinkingLevel":"auto",
		"planModeEnabled":false
	}`))
	frames := initFrames(th, nil, true)
	th.mu.Unlock()

	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	require.NotNil(t, row.ThinkingLevel)
	assert.Equal(t, "auto", *row.ThinkingLevel)
	require.NotEmpty(t, frames)
	assert.Equal(t, "auto", frames[0]["thinkingLevel"])

	// 兼容 thinkingConfigured 字段名
	th.mu.Lock()
	f.s.syncModelLocked(th, json.RawMessage(`{
		"model":{"provider":"anthropic","id":"claude-sonnet-4-5"},
		"thinkingLevel":"medium",
		"thinkingConfigured":"high",
		"planModeEnabled":false
	}`))
	frames = initFrames(th, nil, true)
	th.mu.Unlock()

	row, err = f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	require.NotNil(t, row.ThinkingLevel)
	assert.Equal(t, "high", *row.ThinkingLevel)
	assert.Equal(t, "high", frames[0]["thinkingLevel"])
}
