package threads

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

type stubAutomation string

func (a stubAutomation) AutomationIdentity(context.Context) string { return string(a) }

// M14 3.1：新 orb 的 GitHub 身份——自动化入口按自动化身份，其余为 user。
func TestCreateGithubIdentityAutomation(t *testing.T) {
	f := newFixture(t, Config{})
	f.s.SetGitHub(stubAutomation("agent"))
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
	f.s.SetGitHub(stubAutomation("user"))
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
