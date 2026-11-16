package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orb 建一台 orb（provider 按需建一次）。
func (e *testEnv) orb(projectID, identity, kind string) db.Orb {
	e.t.Helper()
	if _, err := e.st.Q.GetProvider(e.ctx, "prov_1"); err != nil {
		require.NoError(e.t, e.st.Q.CreateProvider(e.ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	}
	id := "orb-" + ids.New("x")[2:]
	p := db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: "small", Image: "img", State: "ready", TokenHash: ids.Token("h"), CreatedAt: 1}
	if projectID != "" {
		p.ProjectID = &projectID
	}
	if identity != "" {
		p.GithubIdentity = &identity
	}
	if kind != "" {
		p.Kind = &kind
	}
	require.NoError(e.t, e.st.Q.CreateOrb(e.ctx, p))
	o, err := e.st.Q.GetOrb(e.ctx, id)
	require.NoError(e.t, err)
	return o
}

// boundProject 建一个绑定到 Acme/Widgets 的项目，并连接我的账号。
func (e *testEnv) boundProject() string {
	e.t.Helper()
	e.oauthApp()
	e.widgets()
	e.fake.AddUser(1, "owner", "The Owner")
	e.fake.AddUser(7, "oryn-bot", "")
	require.NoError(e.t, e.connect("user", "c1", "owner"))
	p := e.project("https://github.com/acme/widgets")
	require.NoError(e.t, e.s.BindProject(e.ctx, p))
	return p
}

// OAuth App 的令牌不能收窄：orb 令牌就是身份槽位的令牌（只由出站代理注入）。
func TestOrbToken(t *testing.T) {
	e := newEnv(t)
	p := e.boundProject()
	o := e.orb(p, "", "")

	tok, st := e.s.OrbToken(e.ctx, o.ID)
	require.Equal(t, orbs.GitHubTokenOK, st)
	want, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	assert.Equal(t, want, tok)

	require.NoError(t, e.connect("agent", "c2", "oryn-bot"))
	tok, st = e.s.OrbToken(e.ctx, e.orb(p, "agent", "").ID)
	require.Equal(t, orbs.GitHubTokenOK, st)
	want, err = e.s.tokenFor(e.ctx, slotAgent)
	require.NoError(t, err)
	assert.Equal(t, want, tok)
}

// M15 2.1：归管与项目无关，只看 orb 种类与身份槽位。
func TestOrbTokenStates(t *testing.T) {
	e := newEnv(t)
	p := e.boundProject()

	unbound := e.project("https://example.com/a/b")
	_, st := e.s.OrbToken(e.ctx, e.orb(unbound, "", "").ID)
	assert.Equal(t, orbs.GitHubTokenOK, st, "未绑定项目同样下发")

	_, st = e.s.OrbToken(e.ctx, e.orb("", "", "").ID)
	assert.Equal(t, orbs.GitHubTokenOK, st, "无项目同样下发")

	_, st = e.s.OrbToken(e.ctx, e.orb(p, "agent", "").ID)
	assert.Equal(t, orbs.GitHubTokenUnavailable, st, "Agent 未连接")
	_, st = e.s.OrbToken(e.ctx, e.orb("", "agent", "").ID)
	assert.Equal(t, orbs.GitHubTokenUnavailable, st, "无项目、Agent 未连接")

	_, st = e.s.OrbToken(e.ctx, e.orb(p, "", orbs.KindRunner).ID)
	assert.Equal(t, orbs.GitHubTokenUnmanaged, st, "runner")

	// 我的账号需要重新授权：不归管（可回落 PAT）。
	require.NoError(t, e.st.Q.SetGithubIdentityStatus(e.ctx, db.SetGithubIdentityStatusParams{Status: "reauth", UpdatedAt: 1, Slot: "user"}))
	_, st = e.s.OrbToken(e.ctx, e.orb(p, "", "").ID)
	assert.Equal(t, orbs.GitHubTokenUnmanaged, st)
}

func TestOrbSetup(t *testing.T) {
	e := newEnv(t)
	p := e.boundProject()
	require.NoError(t, e.connect("agent", "c2", "oryn-bot"))

	gs, ok := e.s.OrbSetup(e.ctx, e.orb(p, "agent", ""))
	require.True(t, ok)
	assert.Equal(t, "oryn-bot", gs.Login)
	assert.True(t, gs.Agent)
	assert.Equal(t, "oryn-bot", gs.GitIdentity.Name)
	assert.Equal(t, "7+oryn-bot@users.noreply.github.com", gs.GitIdentity.Email)
	assert.Equal(t, e.fake.URL(), gs.APIOrigin)
	assert.Equal(t, e.fake.URL(), gs.GitOrigin)

	gs, ok = e.s.OrbSetup(e.ctx, e.orb(p, "", ""))
	require.True(t, ok)
	assert.False(t, gs.Agent)
	assert.Equal(t, "The Owner", gs.GitIdentity.Name)
	assert.Equal(t, "1+owner@users.noreply.github.com", gs.GitIdentity.Email)

	// 无项目的 orb 按自己的身份槽位下发
	gs, ok = e.s.OrbSetup(e.ctx, e.orb("", "", ""))
	require.True(t, ok)
	assert.Equal(t, "owner", gs.Login)
	gs, ok = e.s.OrbSetup(e.ctx, e.orb("", "agent", ""))
	require.True(t, ok)
	assert.Equal(t, "oryn-bot", gs.Login)

	coord := e.orb(p, "", "")
	coord.Coordinator = true
	_, ok = e.s.OrbSetup(e.ctx, coord)
	assert.False(t, ok, "协调宿主不下发 GitHub 凭据")
	_, ok = e.s.OrbSetup(e.ctx, e.orb("", "", orbs.KindRunner))
	assert.False(t, ok, "runner 不下发 GitHub 凭据")
}
