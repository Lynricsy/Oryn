package threads

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 计划模式的临时模型 B 与思考级别不得经 syncModelLocked 写进线程行（DB）；
// 重启时 initFrames 也不得把临时模型 B（或常驻基线 A）作为显式模型覆盖正在进行的计划会话。
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

	// 3. 计划态下重启：initFrames 不带显式 provider/modelId 与 set_thinking_level，由 Core 从会话日志恢复计划态
	require.NotEmpty(t, inPlanFrames)
	assert.Equal(t, "open_session", inPlanFrames[0]["type"])
	assert.Nil(t, inPlanFrames[0]["provider"], "计划态重启不得传显式 provider 覆盖计划模型")
	assert.Nil(t, inPlanFrames[0]["modelId"], "计划态重启不得传显式 modelId 覆盖计划模型")
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
	assert.Contains(t, frameTypes(normalFrames), "set_thinking_level")
}
