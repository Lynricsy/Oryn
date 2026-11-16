package api_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
