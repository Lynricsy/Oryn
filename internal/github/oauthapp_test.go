package github

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codeOf 取 *Error 的码；不是 *Error 返回空串。
func codeOf(err error) string {
	var ge *Error
	if errors.As(err, &ge) {
		return ge.Code
	}
	return ""
}

func stateOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	st := u.Query().Get("state")
	require.NotEmpty(t, st)
	return st
}

// oauthApp 用假服务的 OAuth App 凭据完成配置。
func (e *testEnv) oauthApp() {
	e.t.Helper()
	require.NoError(e.t, e.s.SetOAuthApp(e.ctx, e.fake.OAuthApp.ClientID, e.fake.OAuthApp.ClientSecret, ""))
}

func TestSetOAuthApp(t *testing.T) {
	e := newEnv(t)
	st, err := e.s.Status(e.ctx)
	require.NoError(t, err)
	assert.Nil(t, st.OauthApp)
	require.NotNil(t, st.WebhookBase)
	assert.Equal(t, testPublicURL, *st.WebhookBase)

	_, _, err = e.s.oauthClient(e.ctx)
	var ge *Error
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, "github_not_configured", ge.Code)
	assert.Equal(t, http.StatusConflict, ge.Status)

	// 首次保存必须带 secret；clientId 必填；webhookBase 必须是 http(s) 地址
	assert.Equal(t, "invalid_request", codeOf(e.s.SetOAuthApp(e.ctx, "Ov23.test", "", "")))
	assert.Equal(t, "invalid_request", codeOf(e.s.SetOAuthApp(e.ctx, "  ", "s", "")))
	assert.Equal(t, "invalid_request", codeOf(e.s.SetOAuthApp(e.ctx, "Ov23.test", "s", "ftp://hooks.example.com")))
	assert.Equal(t, "invalid_request", codeOf(e.s.SetOAuthApp(e.ctx, "Ov23.test", "s", "https://hooks.example.com/?a=1")))

	e.oauthApp()
	st, err = e.s.Status(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, st.OauthApp)
	assert.Equal(t, "Ov23.test", st.OauthApp.ClientID)
	assert.Equal(t, testPublicURL+"/api/v1/public/github/oauth/callback", st.OauthApp.CallbackURL)
	assert.Equal(t, testPublicURL, *st.WebhookBase)
	row, err := e.st.Q.GetGithubOAuthApp(e.ctx)
	require.NoError(t, err)
	assert.NotContains(t, string(row.ClientSecretEnc), "oauth-secret")
	plain, err := e.s.cfg.Box.Open(aadOAuthSecret, row.ClientSecretEnc)
	require.NoError(t, err)
	assert.Equal(t, "oauth-secret", string(plain))

	// 不带 secret：沿用已存的值；webhookBase 去掉结尾斜杠
	require.NoError(t, e.s.SetOAuthApp(e.ctx, "Ov23.new", "", "https://hooks.example.com/"))
	id, secret, err := e.s.oauthClient(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, "Ov23.new", id)
	assert.Equal(t, "oauth-secret", secret)
	st, err = e.s.Status(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://hooks.example.com", *st.WebhookBase)
}

func TestOAuthClient(t *testing.T) {
	e := newEnv(t)
	_, _, err := e.s.oauthClient(e.ctx)
	assert.Equal(t, "github_not_configured", codeOf(err))

	e.oauthApp()
	id, secret, err := e.s.oauthClient(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, "Ov23.test", id)
	assert.Equal(t, "oauth-secret", secret)
}

func TestDeleteOAuthApp(t *testing.T) {
	e := newEnv(t)
	e.owner()
	e.oauthApp()
	e.fake.AddUser(1, "owner", "")
	e.fake.AddUser(7, "oryn-bot", "")
	require.NoError(t, e.connect("user", "c1", "owner"))
	require.NoError(t, e.connect("agent", "c2", "oryn-bot"))

	require.NoError(t, e.s.DeleteOAuthApp(e.ctx))
	idents, err := e.st.Q.ListGithubIdentities(e.ctx)
	require.NoError(t, err)
	assert.Empty(t, idents)
	_, err = e.st.Q.GetGithubOAuthApp(e.ctx)
	assert.True(t, isNoRows(err))
	st, err := e.s.Status(e.ctx)
	require.NoError(t, err)
	assert.Nil(t, st.OauthApp)
	assert.Nil(t, st.Identities.User)
	assert.Nil(t, st.Identities.Agent)
	_, err = e.s.BeginAuthorize(e.ctx, "user")
	assert.Equal(t, "github_not_configured", codeOf(err))
	// 幂等
	require.NoError(t, e.s.DeleteOAuthApp(e.ctx))
}

func TestCallClassifiesErrors(t *testing.T) {
	e := newEnv(t)
	const ssoURL = "https://github.com/orgs/acme/sso?authorization_request=abc"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sso":
			w.Header().Set("X-GitHub-SSO", "required; url="+ssoURL)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource protected by organization SAML enforcement. You must grant your OAuth token access to this organization."}`))
		case "/restricted":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Although you appear to have the correct authorization credentials, the ` + "`acme`" +
				` organization has enabled OAuth App access restrictions, meaning that data access to third-parties is limited."}`))
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Must have push access to view collaborator permission."}`))
		case "/unauthorized":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)

	get := func(path string) *ghError {
		t.Helper()
		err := e.s.call(e.ctx, http.MethodGet, srv.URL+path, "Bearer x", nil, nil)
		var ge *ghError
		require.True(t, errors.As(err, &ge), "%v", err)
		assert.True(t, IsUpstream(err))
		return ge
	}

	ge := get("/sso")
	assert.Equal(t, kindSSORequired, ge.Kind)
	assert.Equal(t, http.StatusForbidden, ge.Status)
	assert.Equal(t, ssoURL, ge.SSOURL)

	ge = get("/restricted")
	assert.Equal(t, kindOrgRestricted, ge.Kind)
	assert.Empty(t, ge.SSOURL)

	assert.Equal(t, kindUpstream, get("/forbidden").Kind)
	assert.Equal(t, kindReauth, get("/unauthorized").Kind)

	ge = get("/missing")
	assert.Equal(t, kindNotFound, ge.Kind)
	err := e.s.call(e.ctx, http.MethodGet, srv.URL+"/missing", "", nil, nil)
	assert.True(t, isStatus(err, http.StatusNotFound), "仍可按状态码判断")
	assert.False(t, retryable(err))
}

func TestMissingScopes(t *testing.T) {
	assert.Nil(t, missingScopes(parseScopes("repo, workflow, read:org, admin:repo_hook, gist, write:packages, user:email")))
	assert.Nil(t, missingScopes(parseScopes("repo, workflow, admin:org, admin:repo_hook, gist, write:packages, user")))
	assert.Equal(t, []string{"workflow", "admin:repo_hook", "gist", "write:packages", "user:email"}, missingScopes(parseScopes("repo, read:org")))
	assert.Equal(t, []string{"a", "b"}, parseScopes(" a,b  a "))
	assert.Empty(t, parseScopes(""))
}
