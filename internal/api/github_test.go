package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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
	e, fake, _ := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)

	assert.Equal(t, 401, e.do("GET", "/api/v1/github", h2Cred{}, nil).status)
	r := e.do("POST", "/api/v1/github/app/manifest", h2Cred{cookie: e.webSession()}, map[string]any{"public": false})
	assert.Equal(t, 403, r.status, string(r.body))

	r = e.do("GET", "/api/v1/github", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var st struct {
		App        *struct{ Slug, InstallURL string }
		WebhookURL string
	}
	r.json(t, &st)
	assert.Nil(t, st.App)
	assert.Equal(t, h2PublicURL+"/api/v1/public/github/webhook", st.WebhookURL)

	// 未创建 App 时连接身份 → 409
	r = e.do("POST", "/api/v1/github/identities/user/authorize", dev, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "github_not_connected", r.code(t))

	r = e.do("POST", "/api/v1/github/app/manifest", h2Cred{cookie: e.webSession(), csrf: true}, map[string]any{"public": false})
	require.Equal(t, 200, r.status, string(r.body))
	var red struct{ URL string }
	r.json(t, &red)
	u, err := url.Parse(red.URL)
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/public/github/manifest/start", u.Path)
	state := u.Query().Get("state")

	// 独立跳转页：公开、带 CSP、表单指向假 GitHub
	resp := getNoRedirect(t, e.srv.URL+u.Path+"?"+u.RawQuery)
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "default-src 'none'; style-src 'unsafe-inline'; form-action "+fake.URL()+"; base-uri 'none'; frame-ancestors 'none'",
		resp.Header.Get("Content-Security-Policy"))
	page, _ := io.ReadAll(resp.Body)
	assert.Regexp(t, regexp.MustCompile(`action="`+regexp.QuoteMeta(fake.URL())+`/settings/apps/new\?state=`), string(page))

	resp = getNoRedirect(t, e.srv.URL+"/api/v1/public/github/manifest/callback?code="+fake.App.ManifestCode+"&state="+url.QueryEscape(state))
	assert.Equal(t, 302, resp.StatusCode)
	assert.Equal(t, h2PublicURL+"/settings/github?created=1", resp.Header.Get("Location"))
	resp = getNoRedirect(t, e.srv.URL+"/api/v1/public/github/manifest/callback?code="+fake.App.ManifestCode+"&state="+url.QueryEscape(state))
	assert.Equal(t, h2PublicURL+"/settings/github?error=state_invalid", resp.Header.Get("Location"))

	r = e.do("GET", "/api/v1/github", dev, nil)
	require.Equal(t, 200, r.status)
	r.json(t, &st)
	require.NotNil(t, st.App)
	assert.Equal(t, "oryn-test", st.App.Slug)

	// 身份：授权地址 → 回调
	fake.AddUser(1, "owner", "")
	fake.GrantCode("u1", "owner")
	r = e.do("POST", "/api/v1/github/identities/user/authorize", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &red)
	u, err = url.Parse(red.URL)
	require.NoError(t, err)
	resp = getNoRedirect(t, e.srv.URL+"/api/v1/public/github/oauth/callback?code=u1&state="+url.QueryEscape(u.Query().Get("state")))
	assert.Equal(t, h2PublicURL+"/settings/github?connected=user", resp.Header.Get("Location"))

	resp = getNoRedirect(t, e.srv.URL+"/api/v1/public/github/oauth/callback?state=bogus&code=x")
	assert.Equal(t, 302, resp.StatusCode)
	assert.True(t, strings.HasSuffix(resp.Header.Get("Location"), "?error=state_invalid"), resp.Header.Get("Location"))
	resp = getNoRedirect(t, e.srv.URL+"/api/v1/public/github/oauth/callback?error=access_denied&state=x")
	assert.Equal(t, h2PublicURL+"/settings/github?error=access_denied", resp.Header.Get("Location"))

	// 设置与安装同步
	r = e.do("PUT", "/api/v1/github/settings", dev, map[string]any{"automationIdentity": "agent"})
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "github_agent_required", r.code(t))
	fake.AddInstallation(10, "acme")
	r = e.do("POST", "/api/v1/github/installations/sync", dev, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var synced struct {
		Installations []struct{ AccountLogin string }
	}
	r.json(t, &synced)
	require.Len(t, synced.Installations, 1)
	assert.Equal(t, "acme", synced.Installations[0].AccountLogin)

	assert.Equal(t, 204, e.do("DELETE", "/api/v1/github/identities/user", dev, nil).status)
	assert.Equal(t, 404, e.do("DELETE", "/api/v1/github/identities/user", dev, nil).status)
	assert.Equal(t, 204, e.do("DELETE", "/api/v1/github/app", dev, nil).status)
	r = e.do("GET", "/api/v1/github", dev, nil)
	st.App = nil
	r.json(t, &st)
	assert.Nil(t, st.App)
}

func TestGitHubAPINotConfigured(t *testing.T) {
	e := newH2Env(t)
	assert.Equal(t, 501, e.do("GET", "/api/v1/github", h2DeviceToken(t, e), nil).status)
}
