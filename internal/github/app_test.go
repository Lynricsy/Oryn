package github

import (
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
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

var formRe = regexp.MustCompile(`<form method="post" action="([^"]*)"><input type="hidden" name="manifest" value="([^"]*)">`)

// createApp 走一遍 manifest 流程。
func (e *testEnv) createApp() {
	e.t.Helper()
	raw, err := e.s.BeginManifest(e.ctx, apigen.GitHubManifestRequest{})
	require.NoError(e.t, err)
	require.NoError(e.t, e.s.FinishManifest(e.ctx, e.fake.App.ManifestCode, stateOf(e.t, raw)))
}

func TestManifestFlow(t *testing.T) {
	e := newEnv(t)
	raw, err := e.s.BeginManifest(e.ctx, apigen.GitHubManifestRequest{Public: true})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(raw, testPublicURL+"/api/v1/public/github/manifest/start?state=ogs_"), raw)
	state := stateOf(t, raw)

	page, err := e.s.ManifestStartPage(e.ctx, state)
	require.NoError(t, err)
	assert.NotContains(t, page, "<script")
	assert.Contains(t, page, "继续前往 GitHub 创建 App")
	m := formRe.FindStringSubmatch(page)
	require.NotNil(t, m, page)
	action := html.UnescapeString(m[1])
	assert.Equal(t, e.fake.URL()+"/settings/apps/new?state="+url.QueryEscape(state), action)
	var man struct {
		Name           string
		HookAttributes struct {
			URL    string
			Active bool
		} `json:"hook_attributes"`
		RedirectURL        string            `json:"redirect_url"`
		CallbackURLs       []string          `json:"callback_urls"`
		Public             bool              `json:"public"`
		DefaultPermissions map[string]string `json:"default_permissions"`
		DefaultEvents      []string          `json:"default_events"`
	}
	require.NoError(t, json.Unmarshal([]byte(html.UnescapeString(m[2])), &man))
	assert.Equal(t, "Oryn oryn.test", man.Name)
	assert.Equal(t, testPublicURL+"/api/v1/public/github/webhook", man.HookAttributes.URL)
	assert.True(t, man.HookAttributes.Active)
	assert.Equal(t, testPublicURL+"/api/v1/public/github/manifest/callback", man.RedirectURL)
	assert.Equal(t, []string{testPublicURL + "/api/v1/public/github/oauth/callback"}, man.CallbackURLs)
	assert.True(t, man.Public)
	assert.Equal(t, map[string]string{
		"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read", "statuses": "read",
		"actions": "read", "metadata": "read",
	}, man.DefaultPermissions)
	assert.Equal(t, []string{
		"pull_request", "pull_request_review", "pull_request_review_comment", "issue_comment", "issues",
		"check_suite", "status", "repository",
	}, man.DefaultEvents)

	// 不存在的 state
	_, err = e.s.ManifestStartPage(e.ctx, "ogs_bogus")
	assert.Equal(t, "not_found", codeOf(err))

	require.NoError(t, e.s.FinishManifest(e.ctx, e.fake.App.ManifestCode, state))
	st, err := e.s.Status(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, st.App)
	assert.Equal(t, "oryn-test", st.App.Slug)
	assert.Equal(t, e.fake.URL()+"/apps/oryn-test/installations/new", st.App.InstallURL)
	assert.True(t, st.App.Public)
	assert.Equal(t, apigen.GitHubIdentitySlot("user"), st.AutomationIdentity)
	assert.NotNil(t, st.Installations)
	row, err := e.st.Q.GetGithubApp(e.ctx)
	require.NoError(t, err)
	assert.NotContains(t, string(row.PrivateKeyEnc), "BEGIN")
	assert.NotContains(t, string(row.ClientSecretEnc), e.fake.App.ClientSecret)

	// 同一 state 再用一次
	assert.Equal(t, "state_invalid", codeOf(e.s.FinishManifest(e.ctx, e.fake.App.ManifestCode, state)))
	// 已有 App
	_, err = e.s.BeginManifest(e.ctx, apigen.GitHubManifestRequest{})
	assert.Equal(t, "conflict", codeOf(err))
}

func TestManifestOptions(t *testing.T) {
	e := newEnv(t)
	org, hook := "acme-org", "https://hooks.example.com/x"
	raw, err := e.s.BeginManifest(e.ctx, apigen.GitHubManifestRequest{Org: &org, WebhookURL: &hook})
	require.NoError(t, err)
	page, err := e.s.ManifestStartPage(e.ctx, stateOf(t, raw))
	require.NoError(t, err)
	m := formRe.FindStringSubmatch(page)
	require.NotNil(t, m)
	assert.True(t, strings.HasPrefix(html.UnescapeString(m[1]), e.fake.URL()+"/organizations/acme-org/settings/apps/new?state="))
	assert.Contains(t, html.UnescapeString(m[2]), `"url":"https://hooks.example.com/x"`)

	bad := "http://hooks.example.com/x"
	_, err = e.s.BeginManifest(e.ctx, apigen.GitHubManifestRequest{WebhookURL: &bad})
	assert.Equal(t, "invalid_request", codeOf(err))
	badOrg := "a/b"
	_, err = e.s.BeginManifest(e.ctx, apigen.GitHubManifestRequest{Org: &badOrg})
	assert.Equal(t, "invalid_request", codeOf(err))

	require.NoError(t, e.s.FinishManifest(e.ctx, e.fake.App.ManifestCode, stateOf(t, raw)))
	st, err := e.s.Status(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, hook, st.WebhookURL)
}

func TestFinishManifestRejectsIdentityState(t *testing.T) {
	e := newEnv(t)
	state := ids.Token("ogs")
	slot := "user"
	require.NoError(t, e.st.Q.InsertGithubState(e.ctx, db.InsertGithubStateParams{
		StateHash: ids.Hash(state), Kind: "identity", Slot: &slot, ExpiresAt: e.clock.Now().Add(stateTTL).UnixMilli(),
	}))
	assert.Equal(t, "state_invalid", codeOf(e.s.FinishManifest(e.ctx, e.fake.App.ManifestCode, state)))
	_, err := e.st.Q.GetGithubState(e.ctx, ids.Hash(state))
	require.NoError(t, err, "种类不符时不消费")
}

func TestDeleteApp(t *testing.T) {
	e := newEnv(t)
	e.createApp()
	require.NoError(t, e.s.DeleteApp(e.ctx))
	st, err := e.s.Status(e.ctx)
	require.NoError(t, err)
	assert.Nil(t, st.App)
	assert.Equal(t, testPublicURL+"/api/v1/public/github/webhook", st.WebhookURL)
}
