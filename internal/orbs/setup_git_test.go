package orbs

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func (s stubGitHub) OrbSetup(context.Context, db.Orb) (GitHubSetup, bool) {
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

// M15 2.1：身份槽位可用的工作 orb（有没有项目、项目在哪个主机都一样）下发 gh 登录名（含 uploads）、/gh/ 路由与整个
// github.com 主机的 insteadOf，不需要 PAT；runner 与协调宿主不下发。
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
	planOrb := func(d *DefaultSetup, orb db.Orb) SetupPlan {
		t.Helper()
		p, err := d.Plan(ctx, orb)
		require.NoError(t, err)
		return p
	}
	plan := func(d *DefaultSetup, projectID *string) SetupPlan {
		t.Helper()
		return planOrb(d, db.Orb{ID: "orb-x", ProjectID: projectID})
	}
	assertGh := func(p SetupPlan) {
		t.Helper()
		require.NotNil(t, p.Params.Gh)
		assert.Equal(t, "oryn-bot", p.Params.Gh.Login)
		require.NotNil(t, p.Params.Gh.Uploads)
		assert.True(t, *p.Params.Gh.Uploads)
		require.NotNil(t, p.Egress.GitHub)
		assert.Equal(t, "https://api.github.com", p.Egress.GitHub.APIOrigin)
	}

	// 没有 Vault（也就没有任何 PAT）
	d := &DefaultSetup{Store: st, GitHub: gh}
	p := plan(d, project("https://github.com/Owner/Repo.git", nil))
	assertGh(p)
	assert.Equal(t, []string{"github.com"}, p.Params.GitHosts, "整个 github.com 主机经出站代理")
	assert.Equal(t, map[string][]string{"github.com": {"owner/repo"}}, p.Egress.GitRepos, "项目仓库照旧写入")
	require.NotNil(t, p.Params.GitIdentity)
	assert.Equal(t, bot, *p.Params.GitIdentity, "项目与所有者都没设身份：用 GitHub 账号推出的身份")

	// 无项目的 orb 同样下发
	p = plan(d, nil)
	assertGh(p)
	assert.Equal(t, []string{"github.com"}, p.Params.GitHosts)
	assert.Empty(t, p.Egress.GitRepos)
	assert.Equal(t, bot, *p.Params.GitIdentity)

	// 无项目 + PAT：每个有令牌的主机整主机覆盖，github.com 不重复
	p = plan(&DefaultSetup{Store: st, GitHub: gh, Vault: gitVault{hosts: []string{"gitlab.com", "GitHub.com"}}}, nil)
	assertGh(p)
	assert.Equal(t, []string{"github.com", "gitlab.com"}, p.Params.GitHosts)

	// 非 github.com 项目仓库也下发；项目仓库的条目与 GitRepos 不变
	vd := &DefaultSetup{Store: st, GitHub: gh, Vault: gitVault{hosts: []string{"gitlab.com"}}}
	p = plan(vd, project("https://gitlab.com/a/b", nil))
	assertGh(p)
	assert.Equal(t, []string{"github.com", "gitlab.com/a/b"}, p.Params.GitHosts)
	assert.Equal(t, map[string][]string{"gitlab.com": {"a/b"}}, p.Egress.GitRepos)

	// runner 与协调宿主不下发
	for _, orb := range []db.Orb{{ID: "orb-r", Kind: KindRunner}, {ID: "orb-c", Coordinator: true}} {
		p = planOrb(d, orb)
		assert.Nil(t, p.Params.Gh, orb.ID)
		assert.Nil(t, p.Egress.GitHub, orb.ID)
		assert.Empty(t, p.Params.GitHosts, orb.ID)
	}

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

	// 不归管：与没有 GitHub 集成时一致
	p = plan(&DefaultSetup{Store: st, GitHub: stubGitHub{}}, withIdent)
	assert.Nil(t, p.Params.Gh)
	assert.Nil(t, p.Egress.GitHub)
	assert.Empty(t, p.Params.GitHosts)
	p = plan(&DefaultSetup{Store: st, GitHub: stubGitHub{}, Vault: gitVault{hosts: []string{"github.com"}}}, withIdent)
	assert.Equal(t, []string{"github.com/Owner/Repo"}, p.Params.GitHosts, "回落 PAT 时只覆盖项目仓库")

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

// M15 2.1：有 github.com 项目的 orb 经出站代理可读写任意 github.com 仓库，但不能读其它 PAT 主机上的非项目仓库。
func TestPlanGitHubEgress(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(up.Close)
	repo, id := "https://github.com/Owner/Repo", ids.New("prj")
	require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: id, Name: id, RepoUrl: &repo, CreatedAt: 1, UpdatedAt: 1}))
	gh := stubGitHub{ok: true, tok: "oauth", state: GitHubTokenOK,
		gs: GitHubSetup{Login: "owner", APIOrigin: up.URL, GitOrigin: up.URL}}
	vault := gitVault{hosts: []string{"gitlab.com", "github.com"}}
	d := &DefaultSetup{Store: st, GitHub: gh, Vault: vault}
	plan, err := d.Plan(ctx, db.Orb{ID: "orb-x", ProjectID: &id})
	require.NoError(t, err)
	assert.Equal(t, []string{"github.com"}, plan.Params.GitHosts, "其它 PAT 主机不覆盖")

	s := &Service{secrets: vault, github: gh, policies: map[string]egress.Policy{"orb-x": plan.Egress}}
	srv := httptest.NewServer(egress.NewHandler("orb-x", egressResolver{s: s, orbID: "orb-x"}))
	t.Cleanup(srv.Close)
	for path, code := range map[string]int{
		"/git/github.com/someone/else/info/refs?service=git-upload-pack":  http.StatusOK,
		"/git/github.com/someone/else/info/refs?service=git-receive-pack": http.StatusOK,
		"/git/gitlab.com/other/repo/info/refs?service=git-upload-pack":    http.StatusForbidden,
	} {
		r, err := http.Get(srv.URL + path)
		require.NoError(t, err)
		_ = r.Body.Close()
		assert.Equal(t, code, r.StatusCode, path)
	}
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
