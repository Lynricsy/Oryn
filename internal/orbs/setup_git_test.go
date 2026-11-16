package orbs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
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

// stubGitHub 是测试用的 orbs.GitHub：setup 恒返回 gs / ok，令牌恒返回 tok / state。
type stubGitHub struct {
	gs    GitHubSetup
	ok    bool
	tok   string
	state GitHubTokenState
}

func (s stubGitHub) OrbSetup(context.Context, db.Orb, db.Project) (GitHubSetup, bool) {
	return s.gs, s.ok
}
func (s stubGitHub) OrbToken(context.Context, string) (string, GitHubTokenState) {
	return s.tok, s.state
}
func (s stubGitHub) Call(context.Context, string, string, apigen.LinkCtlGithubCallParams) (apigen.LinkCtlGithubCallResult, error) {
	return apigen.LinkCtlGithubCallResult{}, nil
}
func (s stubGitHub) PrepareReport(context.Context, string, apigen.LinkCtlGithubPrepareReportParams) error {
	return nil
}
func (s stubGitHub) GitHubWriteBegin(context.Context, string, egress.GitHubWrite) string       { return "" }
func (s stubGitHub) GitHubWriteEnd(context.Context, string, egress.GitHubWrite, int, []string) {}

// M14 3.3：项目归 GitHub App 管时，不配 PAT 也给 github.com 项目仓库下发 insteadOf、gh 登录名与 /gh/ 路由。
func TestPlanGitHub(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	project := func(repo string, mod func(*db.CreateProjectParams)) *string {
		id := ids.New("prj")
		p := db.CreateProjectParams{ID: id, Name: id, RepoUrl: &repo, CreatedAt: 1, UpdatedAt: 1}
		if mod != nil {
			mod(&p)
		}
		require.NoError(t, st.Q.CreateProject(ctx, p))
		return &id
	}
	bot := apigen.GitIdentity{Name: "oryn-bot", Email: "7+oryn-bot@users.noreply.github.com"}
	gh := stubGitHub{ok: true, gs: GitHubSetup{Login: "oryn-bot", GitIdentity: bot, APIOrigin: "https://api.github.com", GitOrigin: "https://github.com"}}
	plan := func(d *DefaultSetup, projectID *string) SetupPlan {
		t.Helper()
		p, err := d.Plan(ctx, db.Orb{ID: "orb-x", ProjectID: projectID})
		require.NoError(t, err)
		return p
	}

	// 没有 Vault（也就没有任何 PAT）
	d := &DefaultSetup{Store: st, GitHub: gh}
	p := plan(d, project("https://github.com/Owner/Repo.git", nil))
	require.NotNil(t, p.Params.Gh)
	assert.Equal(t, "oryn-bot", p.Params.Gh.Login)
	assert.Equal(t, []string{"github.com/Owner/Repo"}, p.Params.GitHosts)
	require.NotNil(t, p.Egress.GitHub)
	assert.Equal(t, "https://api.github.com", p.Egress.GitHub.APIOrigin)
	assert.Equal(t, map[string][]string{"github.com": {"owner/repo"}}, p.Egress.GitRepos)
	require.NotNil(t, p.Params.GitIdentity)
	assert.Equal(t, bot, *p.Params.GitIdentity, "项目与所有者都没设身份：用 GitHub 账号推出的身份")

	// user 身份：项目身份优先
	name, email := "Proj", "proj@example.com"
	withIdent := project("https://github.com/Owner/Repo", func(c *db.CreateProjectParams) {
		c.GitAuthorName, c.GitAuthorEmail = &name, &email
	})
	p = plan(d, withIdent)
	assert.Equal(t, apigen.GitIdentity{Name: name, Email: email}, *p.Params.GitIdentity)

	// agent 身份：覆盖项目身份
	agent := gh
	agent.gs.Agent = true
	p = plan(&DefaultSetup{Store: st, GitHub: agent}, withIdent)
	assert.Equal(t, bot, *p.Params.GitIdentity)

	// 不归 App 管：与没有 GitHub 集成时一致
	p = plan(&DefaultSetup{Store: st, GitHub: stubGitHub{}}, withIdent)
	assert.Nil(t, p.Params.Gh)
	assert.Nil(t, p.Egress.GitHub)
	assert.Empty(t, p.Params.GitHosts)

	// 非 github.com 仓库不问 GitHub 集成
	p = plan(d, project("https://gitlab.com/a/b", nil))
	assert.Nil(t, p.Params.Gh)
	assert.Empty(t, p.Params.GitHosts)

	// 改名前的地址随 setup 下发
	moved := project("https://github.com/acme/gadgets", nil)
	repo := "https://github.com/acme/gadgets"
	require.NoError(t, st.Q.SetProjectRepoURL(ctx, db.SetProjectRepoURLParams{RepoUrl: &repo,
		GithubPrevUrls: `["https://github.com/acme/widgets"]`, UpdatedAt: 2, ID: *moved}))
	p = plan(d, moved)
	require.NotNil(t, p.Params.Repo)
	assert.Equal(t, []string{"https://github.com/acme/widgets"}, p.Params.Repo.PreviousUrls)
	p = plan(d, withIdent)
	assert.Nil(t, p.Params.Repo.PreviousUrls)
}

// M14 3.3：github.com 的 git 令牌——归 App 管用收窄令牌，不可用时不回落，不归 App 管回落到 PAT。
func TestEgressResolverGitHubToken(t *testing.T) {
	r := func(g stubGitHub) egressResolver {
		return egressResolver{s: &Service{secrets: gitVault{}, github: g}, orbID: "orb-x"}
	}
	tok, ok := r(stubGitHub{tok: "scoped", state: GitHubTokenOK}).GitToken("github.com")
	assert.True(t, ok)
	assert.Equal(t, "scoped", tok)
	tok, ok = r(stubGitHub{tok: "scoped", state: GitHubTokenOK}).GitHubToken()
	assert.True(t, ok)
	assert.Equal(t, "scoped", tok)

	_, ok = r(stubGitHub{state: GitHubTokenUnavailable}).GitToken("GitHub.com")
	assert.False(t, ok, "不可用时不得回落到 PAT")
	_, ok = r(stubGitHub{state: GitHubTokenUnavailable}).GitHubToken()
	assert.False(t, ok)

	tok, ok = r(stubGitHub{state: GitHubTokenUnmanaged}).GitToken("github.com")
	assert.True(t, ok)
	assert.Equal(t, "tok", tok, "不归 App 管时回落到密钥库的 PAT")
	_, ok = r(stubGitHub{state: GitHubTokenUnmanaged}).GitHubToken()
	assert.False(t, ok)

	tok, ok = r(stubGitHub{tok: "scoped", state: GitHubTokenOK}).GitToken("gitlab.com")
	assert.True(t, ok)
	assert.Equal(t, "tok", tok, "其它主机不经 GitHub 集成")
}
