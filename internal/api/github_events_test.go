package api_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/github/githubtest"
)

func TestGitHubWebhookEndpoint(t *testing.T) {
	e, fake, gh := newGitHubEnv(t)
	hook := e.srv.URL + "/api/v1/public/github/webhook"
	post := func(body []byte, sig string) int {
		req, err := http.NewRequest(http.MethodPost, hook, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GitHub-Event", "ping")
		req.Header.Set("X-GitHub-Delivery", "g-api")
		req.Header.Set("X-Hub-Signature-256", sig)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	body := []byte(`{"zen":"hi"}`)
	// 没有 App → 404；公开路由不需要会话
	assert.Equal(t, http.StatusNotFound, post(body, fake.Sign(body)))
	createGitHubApp(t, fake, gh)
	assert.Equal(t, http.StatusUnauthorized, post(body, "sha256=00"))
	assert.Equal(t, http.StatusAccepted, post(body, fake.Sign(body)))
	status, _ := fake.Deliver(hook, "ping", map[string]any{"zen": "x"})
	assert.Equal(t, http.StatusAccepted, status)

	big := make([]byte, 26<<20)
	assert.Equal(t, http.StatusRequestEntityTooLarge, post(big, fake.Sign(big)))
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
