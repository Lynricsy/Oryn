package orbs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

type gitVault struct{ hosts []string }

func (gitVault) Lookup(string) (string, string, bool)       { return "", "", false }
func (gitVault) SecretByID(string) (string, []string, bool) { return "", nil, false }
func (gitVault) GitToken(string) (string, bool)             { return "tok", true }
func (gitVault) EnvSecrets() (map[string]string, error)     { return map[string]string{}, nil }
func (v gitVault) GitHosts() ([]string, error)              { return v.hosts, nil }

// M2：有 https 项目仓库的 orb 只放行该仓库（insteadOf 也只覆盖它）；无项目（或非 https 仓库）任意仓库只读。
func TestPlanGitEgressPolicy(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	d := &DefaultSetup{Store: st, Vault: gitVault{hosts: []string{"gitlab.com", "github.com"}}}
	project := func(name, repo string) *string {
		id := ids.New("prj")
		require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: id, Name: name, RepoUrl: &repo, CreatedAt: 1, UpdatedAt: 1}))
		return &id
	}
	plan := func(projectID *string) SetupPlan {
		t.Helper()
		p, err := d.Plan(ctx, db.Orb{ID: "orb-x", ProjectID: projectID})
		require.NoError(t, err)
		return p
	}

	p := plan(project("gh", "https://github.com/Owner/Repo.git"))
	assert.Equal(t, []string{"github.com/Owner/Repo"}, p.Params.GitHosts)
	assert.Equal(t, map[string][]string{"github.com": {"owner/repo"}}, p.Egress.GitRepos)

	p = plan(project("sub", "https://gitlab.com/group/sub/repo"))
	assert.Equal(t, []string{"gitlab.com/group/sub/repo"}, p.Params.GitHosts, "多段路径整体作为仓库")
	assert.Equal(t, map[string][]string{"gitlab.com": {"group/sub/repo"}}, p.Egress.GitRepos)

	p = plan(project("notoken", "https://example.org/a/b"))
	assert.Empty(t, p.Params.GitHosts, "仓库主机没有令牌：不经代理")
	assert.Equal(t, map[string][]string{"example.org": {"a/b"}}, p.Egress.GitRepos, "其余仓库照样拒绝")

	for _, ro := range []*string{nil, project("ssh", "git@github.com:owner/repo.git"), project("dots", "https://github.com/owner/../x")} {
		p = plan(ro)
		assert.Equal(t, []string{"github.com", "gitlab.com"}, p.Params.GitHosts)
		assert.Empty(t, p.Egress.GitRepos, "只读放行任意仓库")
	}
}
