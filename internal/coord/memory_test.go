package coord

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOwnerMemory 记录被要求生成记忆段的对话。
type fakeOwnerMemory struct {
	brief string
	calls []string
}

func (f *fakeOwnerMemory) Brief(_ context.Context, threadID string) (string, error) {
	f.calls = append(f.calls, threadID)
	return f.brief, nil
}

// TestProfileOwnerMemory:助手记忆关闭（记忆段为空）时提示词与改动前逐字相同；开启时记忆段接在末尾；Space 不注入。
func TestProfileOwnerMemory(t *testing.T) {
	e := newEnv(t, true)
	conv := e.assistant()
	before, err := e.c.AgentProfile(e.ctx, conv.ID)
	require.NoError(t, err)
	assert.Equal(t, SystemPrompt(nil, "", conv.ID), before.SystemPrompt)

	mem := &fakeOwnerMemory{}
	e.c.SetOwnerMemory(mem)
	off, err := e.c.AgentProfile(e.ctx, conv.ID)
	require.NoError(t, err)
	assert.Equal(t, before.SystemPrompt, off.SystemPrompt, "没有记忆段时逐字不变")

	mem.brief = "Memory（记忆）：\n## 关于主人\n主人住在新加坡"
	on, err := e.c.AgentProfile(e.ctx, conv.ID)
	require.NoError(t, err)
	assert.Equal(t, before.SystemPrompt+"\n\n"+mem.brief, on.SystemPrompt)

	sp := e.space("s", "")
	spProf, err := e.c.AgentProfile(e.ctx, sp.ThreadID)
	require.NoError(t, err)
	assert.False(t, strings.Contains(spProf.SystemPrompt, "Memory（记忆）"))
	assert.Equal(t, []string{conv.ID, conv.ID}, mem.calls, "Space 协调线程不生成记忆段")
}
