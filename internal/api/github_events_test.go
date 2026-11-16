package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/github"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
)

// connectGitHubUser 配好 OAuth App（webhook 基址 = 测试服务地址）并把我的账号连到 login。
func connectGitHubUser(t *testing.T, e *h2Env, fake *githubtest.Server, gh *github.Service, login string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, gh.SetOAuthApp(ctx, fake.OAuthApp.ClientID, fake.OAuthApp.ClientSecret, e.srv.URL))
	fake.AddUser(1, login, "")
	fake.GrantCode("c-"+login, login)
	raw, err := gh.BeginAuthorize(ctx, "user")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	_, err = gh.FinishAuthorize(ctx, "c-"+login, u.Query().Get("state"))
	require.NoError(t, err)
}

func TestGitHubWebhookEndpoint(t *testing.T) {
	e, fake, gh := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)
	hook := e.srv.URL + "/api/v1/public/github/webhook"
	post := func(body []byte, h http.Header) int {
		req, err := http.NewRequest(http.MethodPost, hook, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header = h.Clone()
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GitHub-Event", "ping")
		req.Header.Set("X-GitHub-Delivery", "g-api")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	sig := func(v string) http.Header { return http.Header{"X-Hub-Signature-256": {v}} }
	body := []byte(`{"zen":"hi"}`)
	// 既没有仓库 hook 也没有旧 App → 401；公开路由不需要会话
	assert.Equal(t, http.StatusUnauthorized, post(body, sig(fake.Sign(body))))

	// 绑定（步骤 1.4 前仍经旧 App）后自动建仓库 hook
	createGitHubApp(t, fake, gh)
	connectGitHubUser(t, e, fake, gh, "owner")
	fake.AddInstallation(10, "Acme")
	fake.AddRepo(githubtest.Repo{ID: 42, FullName: "Acme/Widgets", OwnerID: 900, InstallationID: 10, Permissions: map[string]string{"owner": "admin"}})
	r := e.do("POST", "/api/v1/projects", dev, map[string]any{"name": "w", "repoUrl": "https://github.com/acme/widgets"})
	require.Equal(t, 201, r.status, string(r.body))
	h, ok := fake.HookOf("Acme/Widgets", hook)
	require.True(t, ok)

	// 仓库 hook 投递：经真实路由验签受理
	status, _ := fake.DeliverHook("Acme/Widgets", "issues", map[string]any{"action": "edited"})
	assert.Equal(t, http.StatusAccepted, status)
	signed := func(hookID, target int64) http.Header {
		return http.Header{
			"X-Hub-Signature-256":                  {githubtest.SignHook(h.Secret, body)},
			"X-Github-Hook-Id":                     {strconv.FormatInt(hookID, 10)},
			"X-Github-Hook-Installation-Target-Id": {strconv.FormatInt(target, 10)},
		}
	}
	assert.Equal(t, http.StatusUnauthorized, post(body, signed(h.ID+1, 42)), "hook id 不对")
	assert.Equal(t, http.StatusUnauthorized, post(body, signed(h.ID, 43)), "target id 不符")
	assert.Equal(t, http.StatusAccepted, post(body, signed(h.ID, 42)))

	// 过渡：旧 App 的投递（没有匹配的 hook 行）仍可验签
	assert.Equal(t, http.StatusUnauthorized, post([]byte(`{"zen":"x"}`), sig("sha256=00")))
	status, _ = fake.Deliver(hook, "ping", map[string]any{"zen": "x"})
	assert.Equal(t, http.StatusAccepted, status)

	big := make([]byte, 26<<20)
	assert.Equal(t, http.StatusRequestEntityTooLarge, post(big, sig(fake.Sign(big))))
}

func TestGitHubProjectHookLifecycle(t *testing.T) {
	e, fake, gh := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)
	createGitHubApp(t, fake, gh)
	connectGitHubUser(t, e, fake, gh, "owner")
	fake.AddInstallation(10, "Acme")
	fake.AddRepo(githubtest.Repo{ID: 42, FullName: "Acme/Widgets", OwnerID: 900, InstallationID: 10, Permissions: map[string]string{"owner": "admin"}})
	create := func(name string) string {
		r := e.do("POST", "/api/v1/projects", dev, map[string]any{"name": name, "repoUrl": "https://github.com/acme/widgets"})
		require.Equal(t, 201, r.status, string(r.body))
		var p struct{ ID string }
		r.json(t, &p)
		return p.ID
	}
	hooks := func() int {
		fake.Lock()
		defer fake.Unlock()
		return len(fake.RepoHooks)
	}
	p1, p2, p3 := create("a"), create("b"), create("c")
	require.Equal(t, 1, hooks())

	// 还有其他项目绑定：删除、改到别的仓库都不删 hook
	assert.Equal(t, 204, e.do("DELETE", "/api/v1/projects/"+p1, dev, nil).status)
	assert.Equal(t, 1, hooks())
	r := e.do("PATCH", "/api/v1/projects/"+p2, dev, map[string]any{"repoUrl": "https://gitlab.com/acme/widgets"})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Equal(t, 1, hooks())
	// 最后一个绑定的项目归档：删除 hook
	r = e.do("PATCH", "/api/v1/projects/"+p3, dev, map[string]any{"archived": true})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Equal(t, 0, hooks())
}

func TestGitHubTriggersAPI(t *testing.T) {
	e, fake, gh := newGitHubEnv(t)
	dev := h2DeviceToken(t, e)
	createGitHubApp(t, fake, gh)
	fake.AddInstallation(10, "Acme")
	fake.AddRepo(githubtest.Repo{ID: 42, FullName: "Acme/Widgets", OwnerID: 900, InstallationID: 10})
	r := e.do("POST", "/api/v1/projects", dev, map[string]any{"name": "w", "repoUrl": "https://github.com/acme/widgets"})
	require.Equal(t, 201, r.status, string(r.body))
	var p struct{ ID string }
	r.json(t, &p)
	base := "/api/v1/projects/" + p.ID + "/github/triggers"

	r = e.do("POST", base, dev, map[string]any{"name": "m", "event": "mention", "prompt": "p"})
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "github_agent_required", r.code(t))
	r = e.do("POST", base, dev, map[string]any{"name": "l", "event": "issue_labeled", "prompt": "p"})
	assert.Equal(t, 400, r.status)
	assert.Equal(t, "invalid_request", r.code(t))

	r = e.do("POST", base, dev, map[string]any{"name": "Triage", "event": "issue_labeled", "label": "oryn", "prompt": "Triage {{url}}", "announce": false})
	require.Equal(t, 201, r.status, string(r.body))
	var tr struct {
		ID       string
		Label    string
		Announce bool
		Enabled  bool
	}
	r.json(t, &tr)
	assert.Regexp(t, `^ght_[0-9A-Z]{26}$`, tr.ID)
	assert.Equal(t, "oryn", tr.Label)
	assert.False(t, tr.Announce)
	assert.True(t, tr.Enabled)

	r = e.do("PATCH", "/api/v1/github/triggers/"+tr.ID, dev, map[string]any{"enabled": false})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &tr)
	assert.False(t, tr.Enabled)

	r = e.do("GET", base, dev, nil)
	require.Equal(t, 200, r.status)
	var list struct{ Triggers []struct{ ID string } }
	r.json(t, &list)
	require.Len(t, list.Triggers, 1)
	assert.Equal(t, tr.ID, list.Triggers[0].ID)

	assert.Equal(t, 204, e.do("DELETE", "/api/v1/github/triggers/"+tr.ID, dev, nil).status)
	assert.Equal(t, 404, e.do("DELETE", "/api/v1/github/triggers/"+tr.ID, dev, nil).status)
}
