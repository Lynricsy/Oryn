package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/github/githubtest"
)

func TestListRepos(t *testing.T) {
	e := newEnv(t)
	e.oauthApp()
	_, err := e.s.ListRepos(e.ctx, "")
	assert.Equal(t, "github_identity_required", codeOf(err))

	e.fake.AddUser(1, "owner", "")
	require.NoError(t, e.connect("user", "c1", "owner"))
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	member := map[string]string{"owner": "write"}
	e.fake.AddRepo(githubtest.Repo{ID: 1, FullName: "acme/widgets", PushedAt: base.Add(2 * time.Hour), Description: "w", Permissions: member})
	e.fake.AddRepo(githubtest.Repo{ID: 2, FullName: "acme/gadgets", PushedAt: base, Private: true, Permissions: member})
	e.fake.AddRepo(githubtest.Repo{ID: 3, FullName: "owner/tools", PushedAt: base.Add(time.Hour), DefaultBranch: "trunk"})
	e.fake.AddRepo(githubtest.Repo{ID: 4, FullName: "nobody/x"})

	repos, err := e.s.ListRepos(e.ctx, "")
	require.NoError(t, err)
	require.Len(t, repos, 3)
	assert.Equal(t, []string{"acme/widgets", "owner/tools", "acme/gadgets"}, []string{repos[0].FullName, repos[1].FullName, repos[2].FullName})
	assert.Equal(t, "w", *repos[0].Description)
	assert.Nil(t, repos[2].Description)
	assert.True(t, repos[2].Private)
	assert.Equal(t, "trunk", repos[1].DefaultBranch)
	assert.Equal(t, base.Add(2*time.Hour).UnixMilli(), *repos[0].PushedAt)

	calls := e.fake.CallsTo("GET", "/user/repos")
	require.Len(t, calls, 1)
	assert.Equal(t, "affiliation=owner,collaborator,organization_member&sort=pushed&per_page=100", calls[0].Query)
	n := len(calls)
	repos, err = e.s.ListRepos(e.ctx, "WID")
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "acme/widgets", repos[0].FullName)
	assert.Len(t, e.fake.CallsTo("GET", "/user/repos"), n, "60 秒内命中缓存")

	e.clock.Add(repoListTTL + time.Second)
	_, err = e.s.ListRepos(e.ctx, "")
	require.NoError(t, err)
	assert.Greater(t, len(e.fake.CallsTo("GET", "/user/repos")), n)
}
