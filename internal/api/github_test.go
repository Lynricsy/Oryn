package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
	gh := github.New(github.Config{PublicURL: h2PublicURL, APIURL: fake.URL(), WebURL: fake.URL(), Box: box}, st, nil, nil, nil)
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
		App                *struct{ Slug string }
		Installations      []struct{ ID int64 }
		WebhookURL         string
		AutomationIdentity string
	}
	r.json(t, &st)
	assert.Nil(t, st.App)
	assert.Empty(t, st.Installations)
	assert.Equal(t, h2PublicURL+"/api/v1/public/github/webhook", st.WebhookURL)

	// 未配置 OAuth App 时连接身份 → 409
	r = e.do("POST", "/api/v1/github/identities/user/authorize", dev, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "github_not_configured", r.code(t))

	// M15 1.4：旧 App 的操作不再实现（8.1 删除契约前由 apigen.Unimplemented 回 501）
	assert.Equal(t, 501, e.do("POST", "/api/v1/github/app/manifest", dev, map[string]any{"public": false}).status)
	assert.Equal(t, 501, e.do("DELETE", "/api/v1/github/app", dev, nil).status)
	assert.Equal(t, 501, e.do("POST", "/api/v1/github/installations/sync", dev, nil).status)
	assert.Equal(t, 501, getNoRedirect(t, e.srv.URL+"/api/v1/public/github/manifest/start?state=x").StatusCode)
	assert.Equal(t, 501, getNoRedirect(t, e.srv.URL+"/api/v1/public/github/manifest/callback?code=x&state=x").StatusCode)

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
			ClientID    string
			CallbackURL string
		}
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
	assert.Equal(t, h2PublicURL+"/api/v1/public/github/oauth/callback", st.OauthApp.CallbackURL)
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
