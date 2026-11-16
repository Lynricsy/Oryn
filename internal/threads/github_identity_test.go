package threads

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// stubAutomation 是 GitHub 桩：ResolveAutomationSlot 恒返回 slot 与 note。
type stubAutomation struct{ slot, note string }

func (a stubAutomation) ResolveAutomationSlot(context.Context, bool) (string, string, error) {
	return a.slot, a.note, nil
}

// M14 3.1：新 orb 的 GitHub 身份——自动化入口按自动化身份，其余为 user。
func TestCreateGithubIdentityAutomation(t *testing.T) {
	f := newFixture(t, Config{})
	f.s.SetGitHub(stubAutomation{slot: "agent"})
	_, err := f.s.Create(f.ctx, CreateParams{Prompt: "auto", Automation: true})
	require.NoError(t, err)
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "manual"})
	require.NoError(t, err)
	f.orbs.mu.Lock()
	defer f.orbs.mu.Unlock()
	require.Len(t, f.orbs.creates, 2)
	assert.Equal(t, "agent", f.orbs.creates[0].GithubIdentity)
	assert.Equal(t, "user", f.orbs.creates[1].GithubIdentity)
}

// M14 3.1：orb 内派生的子线程沿用父线程 orb 的身份，即使不带 Automation。
func TestCreateGithubIdentityInheritsParent(t *testing.T) {
	f := newFixture(t, Config{})
	f.s.SetGitHub(stubAutomation{slot: "user"})
	parentOrb := f.orb("ready")
	ident := "agent"
	prov, err := f.st.Q.GetOrb(f.ctx, parentOrb)
	require.NoError(t, err)
	agentOrb := ids.Orb()
	require.NoError(t, f.st.Q.CreateOrb(f.ctx, db.CreateOrbParams{ID: agentOrb, ProviderID: prov.ProviderID, Type: "medium", Image: "test",
		State: "ready", StateChangedAt: 1, TokenHash: ids.Hash("oot_" + agentOrb), CreatedAt: 1, GithubIdentity: &ident}))
	parent := f.thread(agentOrb, StateIdle)

	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "child", ParentThreadID: parent.id})
	require.NoError(t, err)
	f.orbs.mu.Lock()
	defer f.orbs.mu.Unlock()
	require.Len(t, f.orbs.creates, 1)
	assert.Equal(t, "agent", f.orbs.creates[0].GithubIdentity)
}

// M15 1.6：Agent 账号不可用、全自动线程回退到我的账号时，回退提示追加在首条提示词末尾（首行不变）。
func TestCreateGithubIdentityFallbackNote(t *testing.T) {
	f := newFixture(t, Config{})
	f.s.SetGitHub(stubAutomation{slot: "user", note: "[fallback]"})
	th, err := f.s.Create(f.ctx, CreateParams{Prompt: "[first line]\nauto", Automation: true})
	require.NoError(t, err)
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "manual"})
	require.NoError(t, err)
	f.orbs.mu.Lock()
	require.Len(t, f.orbs.creates, 2)
	assert.Equal(t, "user", f.orbs.creates[0].GithubIdentity)
	f.orbs.mu.Unlock()
	row, err := f.st.Q.GetThread(f.ctx, th.ID)
	require.NoError(t, err)
	assert.Contains(t, string(row.StartupFrames), `"message":"[first line]\nauto\n\n[fallback]"`)
}
