package share_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func (e *env) agentAvatar(name, img string) {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.UpsertAgentAvatar(e.ctx, db.UpsertAgentAvatarParams{Name: name, AvatarImage: &img, UpdatedAt: 1}))
}

func taskCall(args map[string]any) map[string]any {
	return map[string]any{"type": "message", "message": map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "toolCall", "id": "tc", "name": "task", "arguments": args}},
	}}
}

// 分享身份带上分支上 task 调用用到且设了头像的子 agent：没设头像的（reviewer）、没用到的（sonic）都不出现；
// 快照创建时固化，之后换头像不影响快照、实时跟随。
func TestShareIdentityAgents(t *testing.T) {
	e := newEnv(t)
	e.thread(tid, "Agents", nil)
	e.add(tid, "s1.jsonl", "u1", "", "message", msg("user", "go"))
	e.add(tid, "s1.jsonl", "a1", "u1", "message", taskCall(map[string]any{
		"tasks": []any{map[string]any{"agent": "scout", "task": "x"}, map[string]any{"agent": "reviewer", "task": "y"}},
	}))
	e.add(tid, "s1.jsonl", "a2", "a1", "message", taskCall(map[string]any{"task": "z"}))
	e.leaf(tid, "a2")
	e.agentAvatar("scout", "data:image/png;base64,SCOUT-v1")
	e.agentAvatar("task", "data:image/png;base64,TASK-v1")
	e.agentAvatar("sonic", "data:image/png;base64,SONIC-v1")

	snap, err := e.svc.Create(e.ctx, tid, apigen.ShareModeSnapshot, false)
	require.NoError(t, err)
	live, err := e.svc.Create(e.ctx, tid, apigen.ShareModeLive, false)
	require.NoError(t, err)

	sid, err := e.svc.PublicIdentity(e.ctx, snap.Slug)
	require.NoError(t, err)
	require.Len(t, sid.Agents, 2)
	assert.Equal(t, "scout", sid.Agents[0].Name)
	assert.Equal(t, "data:image/png;base64,SCOUT-v1", *sid.Agents[0].AvatarImage)
	assert.Equal(t, "task", sid.Agents[1].Name, "没写 agent 的任务算作 task")
	assert.NotContains(t, identityJSON(t, sid), "SONIC", "没用到的 agent 头像不出网")

	e.agentAvatar("scout", "data:image/png;base64,SCOUT-v2")
	sid2, err := e.svc.PublicIdentity(e.ctx, snap.Slug)
	require.NoError(t, err)
	assert.Equal(t, identityJSON(t, sid), identityJSON(t, sid2), "快照固化")
	lid, err := e.svc.PublicIdentity(e.ctx, live.Slug)
	require.NoError(t, err)
	require.NotEmpty(t, lid.Agents)
	assert.Equal(t, "data:image/png;base64,SCOUT-v2", *lid.Agents[0].AvatarImage)
}
