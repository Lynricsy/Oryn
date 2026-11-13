package api_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

func TestProjectIconAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	var p struct {
		ID   string
		Icon *string
	}

	for _, bad := range []string{"ab", "🧪🧪", "a\nb", "\u0301"} {
		r := e.do("POST", "/api/v1/projects", c, map[string]any{"name": "bad", "icon": bad})
		assert.Equal(t, 400, r.status, "%q: %s", bad, r.body)
	}

	r := e.do("POST", "/api/v1/projects", c, map[string]any{"name": "p", "icon": "\U000F0668"})
	require.Equal(t, 201, r.status, string(r.body))
	r.json(t, &p)
	require.NotNil(t, p.Icon)
	assert.Equal(t, "\U000F0668", *p.Icon)

	url := "/api/v1/projects/" + p.ID
	// 未涉及 icon 的更新不动它
	r = e.do("PATCH", url, c, map[string]any{"color": "red"})
	require.Equal(t, 200, r.status)
	r.json(t, &p)
	assert.Equal(t, "\U000F0668", *p.Icon)

	r = e.do("PATCH", url, c, map[string]any{"icon": "👩‍💻"})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &p)
	assert.Equal(t, "👩‍💻", *p.Icon)
	r = e.do("GET", url, c, nil)
	require.Equal(t, 200, r.status)
	r.json(t, &p)
	assert.Equal(t, "👩‍💻", *p.Icon)

	assert.Equal(t, 400, e.do("PATCH", url, c, map[string]any{"icon": "xy"}).status)

	// 空串清除
	r = e.do("PATCH", url, c, map[string]any{"icon": ""})
	require.Equal(t, 200, r.status, string(r.body))
	p.Icon = nil
	r.json(t, &p)
	assert.Nil(t, p.Icon)
	assert.NotContains(t, string(r.body), `"icon"`)
	row, err := e.st.Q.GetProject(context.Background(), p.ID)
	require.NoError(t, err)
	assert.Nil(t, row.Icon)
}

// agent 头像与用户头像共用同一套校验与 handler，两条路由跑同一组用例。
func TestAvatarIdentityAPI(t *testing.T) {
	for _, path := range []string{"/api/v1/settings/agent-identity", "/api/v1/settings/user-identity"} {
		t.Run(path, func(t *testing.T) { testAvatarIdentityAPI(t, path) })
	}
}

func testAvatarIdentityAPI(t *testing.T, path string) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))

	assert.Equal(t, 401, e.do("GET", path, h2Cred{}, nil).status)
	assert.Equal(t, 401, e.do("PUT", path, h2Cred{}, map[string]any{}).status)
	assert.Equal(t, 403, e.do("PUT", path, h2Cred{cookie: e.webSession()}, map[string]any{"avatarIcon": "🤖"}).status)

	r := e.do("GET", path, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{}`, string(r.body))
	assert.Equal(t, 409, e.do("PUT", path, c, map[string]any{"avatarIcon": "🤖"}).status, "尚未初始化所有者")

	require.NoError(t, e.st.Q.CreateOwner(context.Background(), db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))

	for _, bad := range []map[string]any{
		{"avatarIcon": "🤖", "avatarImage": png},
		{"avatarIcon": "robot"},
		{"avatarIcon": "🤖\u0007"},
		{"avatarImage": "data:image/gif;base64,R0lGOD"},
		{"avatarImage": "data:image/svg+xml;base64,PHN2Zz4="},
		{"avatarImage": "https://example.com/a.png"},
		{"avatarImage": "data:image/png;base64,***"},
	} {
		r := e.do("PUT", path, c, bad)
		assert.Equal(t, 400, r.status, "%v: %s", bad, r.body)
		assert.Equal(t, "invalid_request", r.code(t))
	}
	big := "data:image/webp;base64," + strings.Repeat("A", 128<<10)
	r = e.do("PUT", path, c, map[string]any{"avatarImage": big})
	assert.Equal(t, 413, r.status)
	assert.Equal(t, "too_large", r.code(t))

	r = e.do("PUT", path, c, map[string]any{"avatarIcon": " \U000F06A9 "})
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, "{\"avatarIcon\":\"\U000F06A9\"}", string(r.body), "首尾空白被去掉")
	r = e.do("GET", path, h2Cred{cookie: e.webSession()}, nil)
	require.Equal(t, 200, r.status)
	assert.JSONEq(t, "{\"avatarIcon\":\"\U000F06A9\"}", string(r.body))

	// 整体替换：换成图片后图标被清除；空串等同省略
	r = e.do("PUT", path, c, map[string]any{"avatarImage": png, "avatarIcon": ""})
	require.Equal(t, 200, r.status, string(r.body))
	r = e.do("GET", path, c, nil)
	assert.JSONEq(t, `{"avatarImage":"`+png+`"}`, string(r.body))

	// 都省略：恢复默认
	require.Equal(t, 200, e.do("PUT", path, c, map[string]any{}).status)
	r = e.do("GET", path, c, nil)
	assert.JSONEq(t, `{}`, string(r.body))
}

// 两组列互不覆盖：写一个不影响另一个，恢复默认也只清自己那组。
func TestAvatarIdentitiesIndependent(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	require.NoError(t, e.st.Q.CreateOwner(context.Background(), db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	const agent, user = "/api/v1/settings/agent-identity", "/api/v1/settings/user-identity"
	jpeg := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("\xff\xd8 fake"))

	require.Equal(t, 200, e.do("PUT", agent, c, map[string]any{"avatarIcon": "🤖"}).status)
	assert.JSONEq(t, `{}`, string(e.do("GET", user, c, nil).body), "写 agent 头像不影响用户头像")

	require.Equal(t, 200, e.do("PUT", user, c, map[string]any{"avatarImage": jpeg}).status)
	assert.JSONEq(t, `{"avatarIcon":"🤖"}`, string(e.do("GET", agent, c, nil).body), "写用户头像不影响 agent 头像")
	assert.JSONEq(t, `{"avatarImage":"`+jpeg+`"}`, string(e.do("GET", user, c, nil).body))

	require.Equal(t, 200, e.do("PUT", agent, c, map[string]any{}).status)
	assert.JSONEq(t, `{}`, string(e.do("GET", agent, c, nil).body))
	assert.JSONEq(t, `{"avatarImage":"`+jpeg+`"}`, string(e.do("GET", user, c, nil).body), "恢复 agent 默认不清用户头像")

	row, err := e.st.Q.GetOwnerUserIdentity(context.Background())
	require.NoError(t, err)
	assert.Nil(t, row.UserAvatarIcon)
	require.NotNil(t, row.UserAvatarImage)
	assert.Equal(t, jpeg, *row.UserAvatarImage)
}
