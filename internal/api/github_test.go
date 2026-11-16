package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/github"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// newGitHubEnv 同 newH2Env，另挂以假 GitHub 为上游的 GitHub 服务。
func newGitHubEnv(t *testing.T) (*h2Env, *githubtest.Server, *github.Service) {
	t.Helper()
	return newGitHubEnvWith(t, nil)
}

// newGitHubEnvWith 同 newGitHubEnv，GitHub 服务的线程能力取 th（nil = 不建线程）。
func newGitHubEnvWith(t *testing.T, th github.Threads) (*h2Env, *githubtest.Server, *github.Service) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	home, err := agentcfg.NewHome(dir)
	require.NoError(t, err)
	projectHomes, err := agentcfg.NewProjectHomes(dir)
	require.NoError(t, err)
	svc, err := auth.NewService(st, dir, h2PublicURL)
	require.NoError(t, err)
	fake := githubtest.New()
	t.Cleanup(fake.Close)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = h2PublicURL, dir
	cfg.GitHub = config.GitHub{APIURL: fake.URL(), WebURL: fake.URL()}
	gh := github.New(github.Config{PublicURL: h2PublicURL, APIURL: fake.URL(), WebURL: fake.URL(), Box: box}, st, th, nil, nil)
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	e := &h2Env{t: t, st: st, dataDir: dir, vault: secrets.NewVault(st, box), tickets: auth.NewTickets(), svc: svc}
	s.Auth = &api.AuthDeps{Service: svc, Tickets: e.tickets, Vault: e.vault, Home: home, ProjectHomes: projectHomes}
	s.GitHub = &api.GitHubDeps{Service: gh}
	e.srv = httptest.NewServer(api.NewRouter(s))
	t.Cleanup(e.srv.Close)
	return e, fake, gh
}

// getNoRedirect 发匿名 GET，不跟随重定向。
func getNoRedirect(t *testing.T, raw string) *http.Response {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(raw)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestGitHubAPI(t *testing.T) {
	e, fake, gh := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)

	assert.Equal(t, 401, e.do("GET", "/api/v1/github", h2Cred{}, nil).status)
	r := e.do("GET", "/api/v1/github", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var st struct {
		OauthApp           *struct{ ClientID string }
		CallbackURL        string
		WebhookBase        string
		WebURL             string
		AutomationIdentity string
	}
	r.json(t, &st)
	assert.Nil(t, st.OauthApp)
	// 未配置 OAuth App 时也给出回调地址与 webhook 基址（设置页据此提示登记）
	assert.Equal(t, h2PublicURL+"/api/v1/public/github/oauth/callback", st.CallbackURL)
	assert.Equal(t, h2PublicURL, st.WebhookBase)
	assert.Equal(t, fake.URL(), st.WebURL)
	assert.NotContains(t, string(r.body), `"installations"`)
	assert.NotContains(t, string(r.body), `"webhookUrl"`)

	// 未配置 OAuth App 时连接身份 → 409
	r = e.do("POST", "/api/v1/github/identities/user/authorize", dev, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "github_not_configured", r.code(t))

	// M15 8.1：旧 App 的操作已从契约删除
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/github/app/manifest"},
		{"DELETE", "/api/v1/github/app"},
		{"POST", "/api/v1/github/installations/sync"},
	} {
		got := e.do(c.method, c.path, dev, nil).status
		assert.Contains(t, []int{404, 405}, got, c.path)
	}
	assert.Contains(t, []int{404, 405}, getNoRedirect(t, e.srv.URL+"/api/v1/public/github/manifest/start?state=x").StatusCode)
	assert.Contains(t, []int{404, 405}, getNoRedirect(t, e.srv.URL+"/api/v1/public/github/manifest/callback?code=x&state=x").StatusCode)

	require.NoError(t, gh.SetOAuthApp(context.Background(), fake.OAuthApp.ClientID, fake.OAuthApp.ClientSecret, ""))

	// 身份：授权地址 → 回调
	fake.AddUser(1, "owner", "")
	fake.GrantCode("u1", "owner")
	r = e.do("POST", "/api/v1/github/identities/user/authorize", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var red struct{ URL string }
	r.json(t, &red)
	u, err := url.Parse(red.URL)
	require.NoError(t, err)
	resp := getNoRedirect(t, e.srv.URL+"/api/v1/public/github/oauth/callback?code=u1&state="+url.QueryEscape(u.Query().Get("state")))
	assert.Equal(t, h2PublicURL+"/settings/github?connected=user", resp.Header.Get("Location"))

	resp = getNoRedirect(t, e.srv.URL+"/api/v1/public/github/oauth/callback?state=bogus&code=x")
	assert.Equal(t, 302, resp.StatusCode)
	assert.True(t, strings.HasSuffix(resp.Header.Get("Location"), "?error=state_invalid"), resp.Header.Get("Location"))
	resp = getNoRedirect(t, e.srv.URL+"/api/v1/public/github/oauth/callback?error=access_denied&state=x")
	assert.Equal(t, h2PublicURL+"/settings/github?error=access_denied", resp.Header.Get("Location"))

	// 设置：Agent 账号未连接也可设为 agent（R6，使用时按 Q6 回退）
	require.NoError(t, e.st.Q.CreateOwner(context.Background(), db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	r = e.do("PUT", "/api/v1/github/settings", dev, map[string]any{"automationIdentity": "user"})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &st)
	assert.Equal(t, "user", st.AutomationIdentity)
	r = e.do("PUT", "/api/v1/github/settings", dev, map[string]any{"automationIdentity": "agent"})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &st)
	assert.Equal(t, "agent", st.AutomationIdentity)

	assert.Equal(t, 204, e.do("DELETE", "/api/v1/github/identities/user", dev, nil).status)
	assert.Equal(t, 404, e.do("DELETE", "/api/v1/github/identities/user", dev, nil).status)
}

func TestGitHubOAuthAppAPI(t *testing.T) {
	e, fake, _ := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)
	var st struct {
		OauthApp *struct {
			ClientID string
		}
		CallbackURL string
		WebhookBase string
		Identities  struct {
			User *struct {
				Login         string
				Scopes        []string
				MissingScopes []string
			}
		}
	}
	r := e.do("GET", "/api/v1/github", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &st)
	assert.Nil(t, st.OauthApp)
	assert.Equal(t, h2PublicURL, st.WebhookBase)

	// 首次保存必须带 secret
	r = e.do("PUT", "/api/v1/github/oauth-app", dev, map[string]any{"clientId": fake.OAuthApp.ClientID})
	assert.Equal(t, 400, r.status)
	assert.Equal(t, "invalid_request", r.code(t))

	r = e.do("PUT", "/api/v1/github/oauth-app", dev, map[string]any{
		"clientId": fake.OAuthApp.ClientID, "clientSecret": fake.OAuthApp.ClientSecret, "webhookBase": "https://hooks.example.com/",
	})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &st)
	require.NotNil(t, st.OauthApp)
	assert.Equal(t, fake.OAuthApp.ClientID, st.OauthApp.ClientID)
	assert.Equal(t, h2PublicURL+"/api/v1/public/github/oauth/callback", st.CallbackURL)
	assert.Equal(t, "https://hooks.example.com", st.WebhookBase)

	// 授权：scope 恰为七项；授予少于所需时 missingScopes 非空
	fake.AddUser(1, "owner", "")
	fake.Lock()
	fake.Scopes = "repo, workflow"
	fake.Unlock()
	fake.GrantCode("u1", "owner")
	r = e.do("POST", "/api/v1/github/identities/user/authorize", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var red struct{ URL string }
	r.json(t, &red)
	u, err := url.Parse(red.URL)
	require.NoError(t, err)
	assert.Equal(t, "repo workflow read:org admin:repo_hook gist write:packages user:email", u.Query().Get("scope"))
	assert.Equal(t, fake.OAuthApp.ClientID, u.Query().Get("client_id"))
	resp := getNoRedirect(t, e.srv.URL+"/api/v1/public/github/oauth/callback?code=u1&state="+url.QueryEscape(u.Query().Get("state")))
	assert.Equal(t, h2PublicURL+"/settings/github?connected=user", resp.Header.Get("Location"))
	r = e.do("GET", "/api/v1/github", dev, nil)
	r.json(t, &st)
	require.NotNil(t, st.Identities.User)
	assert.Equal(t, []string{"repo", "workflow"}, st.Identities.User.Scopes)
	assert.Equal(t, []string{"read:org", "admin:repo_hook", "gist", "write:packages", "user:email"}, st.Identities.User.MissingScopes)

	// 删除：身份一并断开
	assert.Equal(t, 204, e.do("DELETE", "/api/v1/github/oauth-app", dev, nil).status)
	st.OauthApp, st.Identities.User = nil, nil
	r = e.do("GET", "/api/v1/github", dev, nil)
	r.json(t, &st)
	assert.Nil(t, st.OauthApp)
	assert.Nil(t, st.Identities.User)
	r = e.do("POST", "/api/v1/github/identities/user/authorize", dev, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "github_not_configured", r.code(t))
}

func TestGitHubAPINotConfigured(t *testing.T) {
	e := newH2Env(t)
	assert.Equal(t, 501, e.do("GET", "/api/v1/github", h2DeviceToken(t, e), nil).status)
}

func TestGitHubHooksAPI(t *testing.T) {
	e, _, _ := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)
	ctx := context.Background()

	r := e.do("GET", "/api/v1/github/hooks", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"hooks":[]}`, string(r.body))

	hookID, msg := int64(77), "SAML enforcement https://github.com/orgs/acme/sso?authorization_request=X"
	require.NoError(t, e.st.Q.UpsertGithubHook(ctx, db.UpsertGithubHookParams{
		RepoID: 2, FullName: "acme/widgets", HookID: &hookID, SecretEnc: []byte("x"), Status: "active", UpdatedAt: 10,
	}))
	require.NoError(t, e.st.Q.UpsertGithubHook(ctx, db.UpsertGithubHookParams{
		RepoID: 1, FullName: "acme/docs", Status: "sso_required", Error: &msg, UpdatedAt: 20,
	}))
	at, src := int64(5000), "poll"
	require.NoError(t, e.st.Q.TouchGithubHookDelivery(ctx, db.TouchGithubHookDeliveryParams{LastDeliveryAt: &at, LastDeliverySrc: &src, RepoID: 2}))

	r = e.do("GET", "/api/v1/github/hooks", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"hooks":[
		{"repoId":1,"fullName":"acme/docs","status":"sso_required","error":"SAML enforcement https://github.com/orgs/acme/sso?authorization_request=X","updatedAt":20},
		{"repoId":2,"fullName":"acme/widgets","hookId":77,"status":"active","lastDeliveryAt":5000,"lastDeliverySource":"poll","updatedAt":10}
	]}`, string(r.body))
	// hook 的 secret 不出现在响应里
	assert.NotContains(t, string(r.body), "secret")
}

func TestGitHubAuditAPI(t *testing.T) {
	e, _, _ := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)
	ctx := context.Background()
	th := "T-0190a0b0-0000-7000-8000-000000000001"
	sha := "abc"
	// 5 条：奇数 id 属于线程 th、仓库 acme/widgets；偶数 id 无线程、仓库为空、带门禁结论
	for i := 1; i <= 5; i++ {
		p := db.InsertGithubAuditParams{At: int64(i * 1000), OrbID: "orb-1", Slot: "agent", Method: "POST", Op: "/repos/{owner}/{repo}/issues", Status: 201}
		if i%2 == 1 {
			p.ThreadID, p.Repo, p.BodySha256 = &th, "acme/widgets", &sha
		} else {
			p.Slot, p.Gate, p.Status = "user", "denied: no prepare record", 403
		}
		require.NoError(t, e.st.Q.InsertGithubAudit(ctx, p))
	}
	type page struct {
		Entries []struct {
			ID         int64
			At         int64
			ThreadID   *string
			OrbID      *string
			Slot       string
			Repo       *string
			Gate       *string
			Status     int
			BodySha256 *string
		}
		NextBefore *int64
	}
	get := func(q string) page {
		t.Helper()
		r := e.do("GET", "/api/v1/github/audit"+q, dev, nil)
		require.Equal(t, 200, r.status, string(r.body))
		var p page
		r.json(t, &p)
		return p
	}
	ids := func(p page) []int64 {
		out := []int64{}
		for _, e := range p.Entries {
			out = append(out, e.ID)
		}
		return out
	}

	p := get("")
	assert.Equal(t, []int64{5, 4, 3, 2, 1}, ids(p))
	assert.Nil(t, p.NextBefore)
	require.NotNil(t, p.Entries[0].Repo)
	assert.Equal(t, "acme/widgets", *p.Entries[0].Repo)
	assert.Equal(t, th, *p.Entries[0].ThreadID)
	assert.Equal(t, "orb-1", *p.Entries[0].OrbID)
	assert.Nil(t, p.Entries[0].Gate)
	assert.Nil(t, p.Entries[1].Repo, "空仓库省略")
	assert.Nil(t, p.Entries[1].ThreadID)
	assert.Equal(t, "denied: no prepare record", *p.Entries[1].Gate)
	assert.Equal(t, 403, p.Entries[1].Status)

	// 分页：每页 2 条，nextBefore 是本页最后一条的 id，最后一页不给
	p = get("?limit=2")
	assert.Equal(t, []int64{5, 4}, ids(p))
	require.NotNil(t, p.NextBefore)
	assert.Equal(t, int64(4), *p.NextBefore)
	p = get("?limit=2&before=4")
	assert.Equal(t, []int64{3, 2}, ids(p))
	require.NotNil(t, p.NextBefore)
	p = get("?limit=2&before=2")
	assert.Equal(t, []int64{1}, ids(p))
	assert.Nil(t, p.NextBefore)
	p = get("?limit=3&before=4")
	assert.Equal(t, []int64{3, 2, 1}, ids(p))
	assert.Nil(t, p.NextBefore, "恰好取完时不给 nextBefore")

	// 过滤：线程、仓库（不区分大小写）
	assert.Equal(t, []int64{5, 3, 1}, ids(get("?thread="+th)))
	assert.Equal(t, []int64{5, 3, 1}, ids(get("?repo=ACME/Widgets")))
	assert.Equal(t, []int64{3}, ids(get("?repo=acme/widgets&thread="+th+"&before=5&limit=1")))
	assert.Empty(t, get("?repo=acme/other").Entries)

	for _, q := range []string{"?limit=0", "?limit=201", "?before=0"} {
		assert.Equal(t, 400, e.do("GET", "/api/v1/github/audit"+q, dev, nil).status, q)
	}
}

func TestGitHubReplayAPI(t *testing.T) {
	e, _, _ := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	add := func(guid string, receivedAt int64, outcome string) {
		t.Helper()
		n, err := e.st.Q.InsertGithubDelivery(ctx, db.InsertGithubDeliveryParams{
			Guid: guid, Event: "issues", Source: "webhook", Payload: []byte(`{"action":"opened"}`), ReceivedAt: receivedAt,
		})
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		if outcome != "pending" {
			require.NoError(t, e.st.Q.FinishGithubDelivery(ctx, db.FinishGithubDeliveryParams{Guid: guid, Outcome: outcome}))
		}
	}
	add("done", now-time.Hour.Milliseconds(), "ignored:event")
	add("failed", now-6*24*time.Hour.Milliseconds(), "error:boom")
	add("old", now-8*24*time.Hour.Milliseconds(), "handled")
	add("busy", now, "pending")
	outcome := func(guid string) string {
		t.Helper()
		row, err := e.st.Q.GetGithubDelivery(ctx, guid)
		require.NoError(t, err)
		return row.Outcome
	}

	assert.Equal(t, 202, e.do("POST", "/api/v1/github/deliveries/done/replay", dev, nil).status)
	assert.Equal(t, "pending", outcome("done"))
	assert.Equal(t, 202, e.do("POST", "/api/v1/github/deliveries/failed/replay", dev, nil).status)
	assert.Equal(t, "pending", outcome("failed"))

	r := e.do("POST", "/api/v1/github/deliveries/old/replay", dev, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "conflict", r.code(t))
	assert.Equal(t, "handled", outcome("old"))
	r = e.do("POST", "/api/v1/github/deliveries/busy/replay", dev, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "conflict", r.code(t))
	r = e.do("POST", "/api/v1/github/deliveries/nope/replay", dev, nil)
	assert.Equal(t, 404, r.status)
	assert.Equal(t, "not_found", r.code(t))
	assert.Equal(t, 401, e.do("POST", "/api/v1/github/deliveries/done/replay", h2Cred{}, nil).status)
}

func TestGitHubPresetsAPI(t *testing.T) {
	e, _, _ := newGitHubEnv(t)
	r := e.do("GET", "/api/v1/github/presets", h2DeviceToken(t, e), nil)
	require.Equal(t, 200, r.status, string(r.body))
	var list struct {
		Presets []struct {
			ID      string
			Name    string
			Trigger struct {
				Event  string
				Prompt string
			}
		}
	}
	r.json(t, &list)
	got := map[string]string{}
	for _, p := range list.Presets {
		assert.NotEmpty(t, p.Name, p.ID)
		assert.NotEmpty(t, p.Trigger.Prompt, p.ID)
		got[p.ID] = p.Trigger.Event
	}
	assert.Equal(t, map[string]string{
		"robomp_triage": "issue_opened", "robomp_review": "pull_request_opened", "robomp_release": "workflow_run_completed",
	}, got)
}

// setupOAuthApp 经服务配好假 GitHub 的 OAuth App（不连接身份）。
func setupOAuthApp(t *testing.T, fake *githubtest.Server, gh *github.Service) {
	t.Helper()
	require.NoError(t, gh.SetOAuthApp(context.Background(), fake.OAuthApp.ClientID, fake.OAuthApp.ClientSecret, ""))
}

func TestProjectGitHubBinding(t *testing.T) {
	e, fake, gh := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)
	connectGitHubUser(t, e, fake, gh, "owner")
	fake.AddRepo(githubtest.Repo{ID: 42, FullName: "Acme/Widgets", OwnerID: 900})

	type project struct {
		ID     string
		Github *struct {
			RepoID     int64
			FullName   string
			Bound      bool
			BindStatus *string
			Listen     struct{ Reviews, Comments, Ci bool }
		}
	}
	r := e.do("POST", "/api/v1/projects", dev, map[string]any{"name": "w", "repoUrl": "https://github.com/acme/widgets.git"})
	require.Equal(t, 201, r.status, string(r.body))
	var p project
	r.json(t, &p)
	require.NotNil(t, p.Github)
	assert.EqualValues(t, 42, p.Github.RepoID)
	assert.Equal(t, "acme/widgets", p.Github.FullName)
	assert.True(t, p.Github.Bound)
	assert.True(t, p.Github.Listen.Reviews)

	r = e.do("PATCH", "/api/v1/projects/"+p.ID, dev, map[string]any{"githubListen": map[string]bool{"reviews": false, "comments": true, "ci": true}})
	require.Equal(t, 200, r.status, string(r.body))
	p = project{}
	r.json(t, &p)
	require.NotNil(t, p.Github)
	assert.False(t, p.Github.Listen.Reviews)
	assert.True(t, p.Github.Listen.Ci)

	// 改成我的账号读不到的仓库：清空仓库 id，显示「未绑定」及原因 not_found（R8）
	r = e.do("PATCH", "/api/v1/projects/"+p.ID, dev, map[string]any{"repoUrl": "https://github.com/acme/other"})
	require.Equal(t, 200, r.status, string(r.body))
	p = project{}
	r.json(t, &p)
	require.NotNil(t, p.Github)
	assert.False(t, p.Github.Bound)
	require.NotNil(t, p.Github.BindStatus)
	assert.Equal(t, "not_found", *p.Github.BindStatus)
	row, err := e.st.Q.GetProject(context.Background(), p.ID)
	require.NoError(t, err)
	assert.Nil(t, row.GithubRepoID)

	// 非 GitHub 项目没有 github 字段
	r = e.do("POST", "/api/v1/projects", dev, map[string]any{"name": "plain"})
	require.Equal(t, 201, r.status, string(r.body))
	p = project{}
	r.json(t, &p)
	assert.Nil(t, p.Github)
}

func TestListGitHubReposAPI(t *testing.T) {
	e, fake, gh := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)
	setupOAuthApp(t, fake, gh)
	r := e.do("GET", "/api/v1/github/repos", dev, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "github_identity_required", r.code(t))

	fake.AddUser(1, "owner", "")
	fake.GrantCode("u1", "owner")
	raw, err := gh.BeginAuthorize(context.Background(), "user")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	_, err = gh.FinishAuthorize(context.Background(), "u1", u.Query().Get("state"))
	require.NoError(t, err)
	fake.AddRepo(githubtest.Repo{ID: 1, FullName: "acme/widgets", Permissions: map[string]string{"owner": "write"}})
	fake.AddRepo(githubtest.Repo{ID: 2, FullName: "acme/gadgets", Permissions: map[string]string{"owner": "write"}})

	r = e.do("GET", "/api/v1/github/repos?q=wid", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var list struct {
		Repos []struct {
			ID            int64
			FullName      string
			DefaultBranch string
		}
	}
	r.json(t, &list)
	require.Len(t, list.Repos, 1)
	assert.Equal(t, "acme/widgets", list.Repos[0].FullName)
	assert.Equal(t, "main", list.Repos[0].DefaultBranch)
	assert.Equal(t, 400, e.do("GET", "/api/v1/github/repos?q="+strings.Repeat("x", 101), dev, nil).status)
}

func TestGitHubRefsAPINotBound(t *testing.T) {
	e, fake, gh := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)
	setupOAuthApp(t, fake, gh)
	r := e.do("POST", "/api/v1/projects", dev, map[string]any{"name": "plain", "repoUrl": "https://github.com/acme/none"})
	require.Equal(t, 201, r.status, string(r.body))
	var p struct{ ID string }
	r.json(t, &p)
	r = e.do("GET", "/api/v1/projects/"+p.ID+"/github/refs?q=x", dev, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "github_not_bound", r.code(t))
	r = e.do("POST", "/api/v1/projects/"+p.ID+"/github/refs/7/attachment", dev, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "github_not_bound", r.code(t))
	r = e.do("GET", "/api/v1/projects/prj_01HZZZZZZZZZZZZZZZZZZZZZZZ/github/refs", dev, nil)
	assert.Equal(t, 404, r.status)
}
