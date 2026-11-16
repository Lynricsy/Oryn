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
	e.installApp()
	_, err := e.s.ListRepos(e.ctx, "")
	assert.Equal(t, "github_identity_required", codeOf(err))

	e.fake.AddUser(1, "owner", "")
	require.NoError(t, e.connect("user", "c1", "owner"))
	e.fake.AddInstallation(10, "acme")
	e.fake.AddInstallation(11, "other")
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	e.fake.AddRepo(githubtest.Repo{ID: 1, FullName: "acme/widgets", InstallationID: 10, PushedAt: base.Add(2 * time.Hour), Description: "w"})
	e.fake.AddRepo(githubtest.Repo{ID: 2, FullName: "acme/gadgets", InstallationID: 10, PushedAt: base, Private: true})
	e.fake.AddRepo(githubtest.Repo{ID: 3, FullName: "other/tools", InstallationID: 11, PushedAt: base.Add(time.Hour), DefaultBranch: "trunk"})
	e.fake.AddRepo(githubtest.Repo{ID: 4, FullName: "nobody/x"})

	repos, err := e.s.ListRepos(e.ctx, "")
	require.NoError(t, err)
	require.Len(t, repos, 3)
	assert.Equal(t, []string{"acme/widgets", "other/tools", "acme/gadgets"}, []string{repos[0].FullName, repos[1].FullName, repos[2].FullName})
	assert.Equal(t, "w", *repos[0].Description)
	assert.Nil(t, repos[2].Description)
	assert.True(t, repos[2].Private)
	assert.Equal(t, "trunk", repos[1].DefaultBranch)
	assert.Equal(t, base.Add(2*time.Hour).UnixMilli(), *repos[0].PushedAt)

	calls := len(e.fake.CallsTo("GET", "/user/installations*"))
	repos, err = e.s.ListRepos(e.ctx, "WID")
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "acme/widgets", repos[0].FullName)
	assert.Equal(t, calls, len(e.fake.CallsTo("GET", "/user/installations*")), "60 秒内命中缓存")

	e.clock.Add(repoListTTL + time.Second)
	_, err = e.s.ListRepos(e.ctx, "")
	require.NoError(t, err)
	assert.Greater(t, len(e.fake.CallsTo("GET", "/user/installations*")), calls)
}
