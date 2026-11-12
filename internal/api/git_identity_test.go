package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

func TestGitIdentitySettingsAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	const path = "/api/v1/settings/git-identity"
	good := map[string]any{"name": " Ada Lovelace ", "email": "ada@example.com"}

	assert.Equal(t, 401, e.do("GET", path, h2Cred{}, nil).status)
	assert.Equal(t, 401, e.do("PUT", path, h2Cred{}, good).status)
	// 网页会话的变更请求必须带 CSRF 头。
	assert.Equal(t, 403, e.do("PUT", path, h2Cred{cookie: e.webSession()}, good).status)

	r := e.do("GET", path, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{}`, string(r.body))
	// 尚未初始化所有者：无处保存。
	assert.Equal(t, 409, e.do("PUT", path, c, good).status)

	require.NoError(t, e.st.Q.CreateOwner(context.Background(), db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	for _, bad := range []map[string]any{
		{"name": "Ada", "email": "not-an-email"},
		{"name": "", "email": "ada@example.com"},
		{"name": "   ", "email": "ada@example.com"},
		{"name": "Ada\nEvil", "email": "ada@example.com"},
		{"name": "Ada", "email": "a@b\x00"},
		{"name": strings.Repeat("x", 201), "email": "ada@example.com"},
		{"name": "Ada", "email": strings.Repeat("x", 320) + "@e"},
		{"name": "Ada"},
	} {
		r := e.do("PUT", path, c, bad)
		assert.Equal(t, 400, r.status, "%v: %s", bad, r.body)
	}

	r = e.do("PUT", path, c, good)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"identity":{"name":"Ada Lovelace","email":"ada@example.com"}}`, string(r.body))
	r = e.do("GET", path, h2Cred{cookie: e.webSession()}, nil)
	require.Equal(t, 200, r.status)
	assert.JSONEq(t, `{"identity":{"name":"Ada Lovelace","email":"ada@example.com"}}`, string(r.body))

	require.Equal(t, 204, e.do("DELETE", path, c, nil).status)
	r = e.do("GET", path, c, nil)
	assert.JSONEq(t, `{}`, string(r.body))
}

func TestProjectGitAuthorOverrideAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)

	r := e.do("POST", "/api/v1/projects", c, map[string]any{"name": "half", "gitAuthorName": "Bot"})
	assert.Equal(t, 400, r.status)
	assert.Equal(t, "invalid_request", r.code(t))
	assert.Equal(t, 400, e.do("POST", "/api/v1/projects", c, map[string]any{"name": "bad", "gitAuthorName": "Bot", "gitAuthorEmail": "nope"}).status)
	assert.Equal(t, 400, e.do("POST", "/api/v1/projects", c, map[string]any{"name": "bad", "gitAuthorName": "Bot", "gitAuthorEmail": ""}).status)

	r = e.do("POST", "/api/v1/projects", c, map[string]any{"name": "p", "gitAuthorName": "Bot", "gitAuthorEmail": "bot@example.com"})
	require.Equal(t, 201, r.status, string(r.body))
	var p struct {
		ID             string
		GitAuthorName  *string
		GitAuthorEmail *string
	}
	r.json(t, &p)
	require.NotNil(t, p.GitAuthorName)
	assert.Equal(t, "Bot", *p.GitAuthorName)
	assert.Equal(t, "bot@example.com", *p.GitAuthorEmail)

	url := "/api/v1/projects/" + p.ID
	// 未涉及身份的更新不动覆盖；半套更新被拒绝且不落库。
	r = e.do("PATCH", url, c, map[string]any{"color": "red"})
	require.Equal(t, 200, r.status)
	r.json(t, &p)
	assert.Equal(t, "Bot", *p.GitAuthorName)
	r = e.do("PATCH", url, c, map[string]any{"gitAuthorEmail": "other@example.com"})
	assert.Equal(t, 400, r.status)
	assert.Equal(t, "invalid_request", r.code(t))
	assert.Equal(t, 400, e.do("PATCH", url, c, map[string]any{"gitAuthorName": "Bo\rt", "gitAuthorEmail": "bot@example.com"}).status)

	r = e.do("PATCH", url, c, map[string]any{"gitAuthorName": "Bot 2", "gitAuthorEmail": "bot2@example.com"})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &p)
	assert.Equal(t, "Bot 2", *p.GitAuthorName)
	assert.Equal(t, "bot2@example.com", *p.GitAuthorEmail)

	// 两项都传空串：清除。
	r = e.do("PATCH", url, c, map[string]any{"gitAuthorName": "", "gitAuthorEmail": ""})
	require.Equal(t, 200, r.status, string(r.body))
	p.GitAuthorName, p.GitAuthorEmail = nil, nil
	r.json(t, &p)
	assert.Nil(t, p.GitAuthorName)
	assert.Nil(t, p.GitAuthorEmail)
	row, err := e.st.Q.GetProject(context.Background(), p.ID)
	require.NoError(t, err)
	assert.Nil(t, row.GitAuthorName)
	assert.Nil(t, row.GitAuthorEmail)

	assert.Equal(t, 404, e.do("PATCH", "/api/v1/projects/prj_missing", c, map[string]any{"gitAuthorName": "a", "gitAuthorEmail": "a@b.c"}).status)
	assert.Equal(t, 401, e.do("PATCH", url, h2Cred{}, map[string]any{"gitAuthorName": "a", "gitAuthorEmail": "a@b.c"}).status)
}
