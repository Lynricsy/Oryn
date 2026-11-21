package orbs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
func (s stubGitHub) GitHubTokenRejected(context.Context, string, string)                       {}
func (s stubGitHub) Prepared(context.Context, string, string, string) egress.PrepareState {
	return egress.PrepareNone
}
func (s stubGitHub) ImplAuthorized(context.Context, string) bool                     { return false }
func (s stubGitHub) HeadSHA(context.Context, string, string, string) (string, error) { return "", nil }
func (s stubGitHub) DefaultBranch(context.Context, string, string) (string, error)   { return "", nil }
func (s stubGitHub) IsOrynHook(context.Context, string, int64) bool                  { return true }
func (s stubGitHub) ReleaseThread(context.Context, string) bool                      { return false }

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

// M15 5.4：setup 只按项目的 github_gates 填写门禁；推送、PR 门禁都关闭等于没有门禁。线程限制不进策略（出站代理现查）。
func TestPlanGitHubGates(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	gh := stubGitHub{ok: true, gs: GitHubSetup{Login: "oryn-bot", APIOrigin: "https://api.github.com", GitOrigin: "https://github.com"}}
	d := &DefaultSetup{Store: st, GitHub: gh}
	prj := ids.New("prj")
	repo := "https://github.com/acme/widgets"
	require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: prj, Name: prj, RepoUrl: &repo, CreatedAt: 1, UpdatedAt: 1}))
	gates := func(orb db.Orb) *egress.GateConfig {
		t.Helper()
		p, err := d.Plan(ctx, orb)
		require.NoError(t, err)
		require.NotNil(t, p.Egress.GitHub)
		return p.Egress.GitHub.Gates
	}
	setGates := func(raw string) {
		t.Helper()
		require.NoError(t, st.Q.SetProjectGithubGates(ctx, db.SetProjectGithubGatesParams{GithubGates: &raw, UpdatedAt: 2, ID: prj}))
	}
	orb := db.Orb{ID: "orb-x", ProjectID: &prj}
	assert.Nil(t, gates(orb), "项目没有门禁")
	assert.Nil(t, gates(db.Orb{ID: "orb-noproject"}), "无项目 orb 没有门禁")
	setGates(`{"push":false,"pr":false,"checkCmd":"make check"}`)
	assert.Nil(t, gates(orb), "两个门禁都关闭")
	setGates(`{"push":true,"pr":true,"prHeadings":["Summary"],"requireClosingRef":true}`)
	assert.Equal(t, &egress.GateConfig{Push: true, PR: true, PRHeadings: []string{"Summary"}, RequireClosingRef: true}, gates(orb))
}

// gateStore 建一个项目与一台属于它的 orb，返回 orb 与建线程的函数。
func gateStore(t *testing.T, st *store.Store) (prj string, orb db.Orb, newThread func() string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	prj = ids.New("prj")
	repo := "https://github.com/acme/widgets"
	require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: prj, Name: prj, RepoUrl: &repo, CreatedAt: 1, UpdatedAt: 1}))
	id := "orb-" + ids.New("x")[2:]
	require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: "small", Image: "img", State: "ready",
		TokenHash: ids.Token("h"), CreatedAt: 1, ProjectID: &prj}))
	orb, err := st.Q.GetOrb(ctx, id)
	require.NoError(t, err)
	newThread = func() string {
		t.Helper()
		tid := ids.New("thr")
		require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{ID: tid, ProjectID: &prj, OrbID: &orb.ID, Title: "t", TitleSource: "auto",
			State: "idle", SessionDir: "d", CreatedAt: 1, UpdatedAt: 1, Kind: "work"}))
		return tid
	}
	return prj, orb, newThread
}

// M15 5.4（修订 v6）：线程限制从 orb 线程的任务现查：thread_issues.review_only 与规则（rule_id，缺省 trigger_id）的
// comment_only_reviews / impl_auth；没有唯一未归档工作线程或没有任务时为零值；读库失败返回错误。
func TestEgressThreadGates(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	prj, orb, newThread := gateStore(t, st)
	s := &Service{st: st}
	tg := func() egress.ThreadGates {
		t.Helper()
		v, err := egressResolver{s: s, orbID: orb.ID}.ThreadGates()
		require.NoError(t, err)
		return v
	}
	assert.Zero(t, tg(), "没有线程")
	thr := newThread()
	assert.Zero(t, tg(), "线程没有任务")
	rule := ids.New("trg")
	require.NoError(t, st.Q.InsertGithubTrigger(ctx, db.InsertGithubTriggerParams{ID: rule, ProjectID: prj, Name: "r", Event: "pull_request_opened",
		Prompt: "p", Enabled: true, CreatedAt: 1, UpdatedAt: 1}))
	require.NoError(t, st.Q.SetGithubTriggerOptions(ctx, db.SetGithubTriggerOptionsParams{Drivers: "anyone", Maintainers: "[]", ReviewerBots: "[]",
		Models: "[]", ImplAuth: true, CommentOnlyReviews: true, UpdatedAt: 2, ID: rule}))
	require.NoError(t, st.Q.UpsertThreadIssue(ctx, db.UpsertThreadIssueParams{RepoID: 42, Number: 5, ThreadID: thr, TriggerID: &rule, CreatedAt: 1}))
	assert.Equal(t, egress.ThreadGates{CommentOnlyReviews: true, ImplAuth: true}, tg(), "规则取自 trigger_id")
	require.NoError(t, st.Q.SetThreadIssueTask(ctx, db.SetThreadIssueTaskParams{RuleID: &rule, ReviewOnly: true, RepoID: 42, Number: 5}))
	assert.Equal(t, egress.ThreadGates{ReviewOnly: true, CommentOnlyReviews: true, ImplAuth: true}, tg())

	newThread()
	assert.Zero(t, tg(), "不止一个未归档工作线程：无法确定任务")

	require.NoError(t, st.Close())
	_, err = egressResolver{s: s, orbID: orb.ID}.ThreadGates()
	assert.Error(t, err, "读库失败返回错误（出站代理按最严处理）")
}

// M15 5.4（修订 v6）回归：orb 先完成 setup、之后才写入 review_only=1 的线程任务，下一次推送与 createPullRequest 就被拒，
// 不必等 orb 重新 setup。
func TestEgressReviewOnlyAfterSetup(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	_, orb, newThread := gateStore(t, st)
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer up.Close()
	gh := stubGitHub{ok: true, tok: "T0KEN", state: GitHubTokenOK, gs: GitHubSetup{Login: "oryn-bot", APIOrigin: up.URL, GitOrigin: up.URL}}
	s := &Service{st: st, github: gh, policies: map[string]egress.Policy{}}
	plan, err := (&DefaultSetup{Store: st, GitHub: gh}).Plan(ctx, orb)
	require.NoError(t, err)
	s.setPolicy(orb.ID, plan.Egress, s.policyGeneration()) // setup 完成：此时线程还没有任务
	px := httptest.NewServer(egress.NewHandler(orb.ID, egressResolver{s: s, orbID: orb.ID}))
	defer px.Close()

	pushBody := func() io.Reader {
		cmd := "1111111111111111111111111111111111111111 2222222222222222222222222222222222222222 refs/heads/feat\x00report-status"
		return strings.NewReader(fmt.Sprintf("%04x%s0000PACK", len(cmd)+4, cmd))
	}
	create := `{"query":"mutation { createPullRequest(input: {repositoryId: \"R_1\", baseRefName: \"main\", headRefName: \"feat\", title: \"t\"}) { clientMutationId } }"}`
	push := func() (int, string) {
		t.Helper()
		resp, err := http.Post(px.URL+"/git/github.com/acme/widgets.git/git-receive-pack", "application/x-git-receive-pack-request", pushBody())
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	createPR := func() (int, string) {
		t.Helper()
		resp, err := http.Post(px.URL+"/gh/api.github.com/graphql", "application/json", strings.NewReader(create))
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	thr := newThread()
	code, out := push()
	assert.Equal(t, http.StatusOK, code)
	assert.NotContains(t, out, "ng ")
	code, _ = createPR()
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, 2, calls, "任务写入之前照常转发")

	// 规则建线程之后才写入任务（review_only=1）；策略缓存不变
	require.NoError(t, st.Q.UpsertThreadIssue(ctx, db.UpsertThreadIssueParams{RepoID: 42, Number: 7, ThreadID: thr, CreatedAt: 2}))
	require.NoError(t, st.Q.SetThreadIssueTask(ctx, db.SetThreadIssueTaskParams{ReviewOnly: true, RepoID: 42, Number: 7}))
	code, out = push()
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, out, "ng refs/heads/feat oryn: 外部 PR 评审线程是只读工作区")
	code, out = createPR()
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, out, "oryn: 外部 PR 评审线程是只读工作区")
	assert.Equal(t, 2, calls, "被拒绝的写请求不触达上游")
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

	s := &Service{st: st, secrets: vault, github: gh, policies: map[string]egress.Policy{"orb-x": plan.Egress}}
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

// github.com 的 git 令牌：由 GitHub 集成下发凭据时用身份槽位的 OAuth 令牌，不可用时不回落；不归集成管（runner 等）时回落到 PAT。
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
	assert.Equal(t, "tok", tok, "不归 GitHub 集成管时回落到密钥库的 PAT")
	_, ok = r(stubGitHub{state: GitHubTokenUnmanaged}).GitHubToken()
	assert.False(t, ok)

	tok, ok = r(stubGitHub{tok: "scoped", state: GitHubTokenOK}).GitToken("gitlab.com")
	assert.True(t, ok)
	assert.Equal(t, "tok", tok, "其它主机不经 GitHub 集成")
}

// M24 阶段 ③：托管项目的 orb 从 <public_url>/git/<id>.git 克隆（不设 Branch），该地址经出站代理在本地服务（不看令牌）。
func TestPlanHostedRepo(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	base := "main"
	hosted := ids.New("prj")
	require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: hosted, Name: "h", Hosted: true, BaseBranch: &base, ShallowClone: true, CreatedAt: 1, UpdatedAt: 1}))
	repo := "https://gitlab.com/a/b"
	plain := ids.New("prj")
	require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: plain, Name: "p", RepoUrl: &repo, CreatedAt: 1, UpdatedAt: 1}))
	plan := func(publicURL, prj string) SetupPlan {
		t.Helper()
		d := &DefaultSetup{Store: st, Vault: gitVault{hosts: []string{"gitlab.com"}}, PublicURL: publicURL}
		p, err := d.Plan(ctx, db.Orb{ID: "orb-x", ProjectID: &prj})
		require.NoError(t, err)
		return p
	}
	lower := strings.ToLower(hosted)

	p := plan("https://oryn.example.com", hosted)
	require.NotNil(t, p.Params.Repo)
	assert.Equal(t, "https://oryn.example.com/git/"+hosted+".git", p.Params.Repo.URL)
	assert.Nil(t, p.Params.Repo.Branch, "服务端 HEAD 就是基线；空仓库配 --branch 会克隆失败")
	assert.True(t, p.Params.Repo.Shallow)
	assert.Equal(t, []string{"gitlab.com", "oryn.example.com/git/" + hosted + ".git"}, p.Params.GitHosts,
		"托管仓库主机没有令牌也经代理；其它主机不收窄")
	assert.Equal(t, &egress.HostedGitRoute{Host: "oryn.example.com", Repo: "git/" + lower, ProjectID: hosted}, p.Egress.HostedGit)
	assert.Empty(t, p.Egress.GitRepos, "托管项目没有 repoUrl：其它主机只读")

	p = plan("http://localhost:8080", hosted)
	assert.Contains(t, p.Params.GitHosts, "localhost:8080/git/"+hosted+".git")
	assert.Equal(t, &egress.HostedGitRoute{Host: "localhost:8080", Repo: "git/" + lower, ProjectID: hosted}, p.Egress.HostedGit)

	p = plan("https://H.example/oryn/", hosted)
	assert.Equal(t, "https://h.example/oryn/git/"+hosted+".git", p.Params.Repo.URL, "主机小写，与 insteadOf 条目一致")
	assert.Contains(t, p.Params.GitHosts, "h.example/oryn/git/"+hosted+".git")
	assert.Equal(t, &egress.HostedGitRoute{Host: "h.example", Repo: "oryn/git/" + lower, ProjectID: hosted}, p.Egress.HostedGit)

	p = plan("", hosted)
	assert.Nil(t, p.Params.Repo, "没有 public_url 时不下发托管仓库")
	assert.Nil(t, p.Egress.HostedGit)

	p = plan("https://oryn.example.com", plain)
	assert.Nil(t, p.Egress.HostedGit, "非托管项目没有托管路由")
	assert.Equal(t, repo, p.Params.Repo.URL)
}

func TestHostedGitEntry(t *testing.T) {
	e, r, ok := hostedGitEntry("https://Oryn.Example.com:8443/git/prj_A.git", "prj_A")
	require.True(t, ok)
	assert.Equal(t, "oryn.example.com:8443/git/prj_A.git", e)
	assert.Equal(t, egress.HostedGitRoute{Host: "oryn.example.com:8443", Repo: "git/prj_a", ProjectID: "prj_A"}, r)
	for _, bad := range []string{
		"ftp://h/git/p.git", "https://u:p@h/git/p.git", "https://h/git/p.git?x=1", "https://h/git/p.git#f",
		"https://h/a b/git/p.git", "https://h/x\"y/git/p.git", "https://h_x/git/p.git", "https://h/p.git",
	} {
		_, _, ok := hostedGitEntry(bad, "p")
		assert.False(t, ok, bad)
	}
}
