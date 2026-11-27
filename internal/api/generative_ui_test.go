package api_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

const generativeUIPath = "/api/v1/settings/generative-ui"

func TestGenerativeUISettingsAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)

	assert.Equal(t, 401, e.do("GET", generativeUIPath, h2Cred{}, nil).status)
	assert.Equal(t, 403, e.do("PUT", generativeUIPath, h2Cred{cookie: e.webSession()}, map[string]any{"enabled": false}).status)

	// 尚无所有者：GET 给缺省值（开启），PUT 409
	r := e.do("GET", generativeUIPath, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"enabled":true}`, string(r.body))
	r = e.do("PUT", generativeUIPath, c, map[string]any{"enabled": false})
	assert.Equal(t, 409, r.status, string(r.body))

	require.NoError(t, e.st.Q.CreateOwner(context.Background(), db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	r = e.do("GET", generativeUIPath, c, nil)
	assert.JSONEq(t, `{"enabled":true}`, string(r.body), "新所有者的缺省值")

	for _, on := range []bool{false, true, false} {
		r = e.do("PUT", generativeUIPath, c, map[string]any{"enabled": on})
		require.Equal(t, 200, r.status, string(r.body))
		r = e.do("GET", generativeUIPath, h2Cred{cookie: e.webSession()}, nil)
		require.Equal(t, 200, r.status)
		if on {
			assert.JSONEq(t, `{"enabled":true}`, string(r.body))
		} else {
			assert.JSONEq(t, `{"enabled":false}`, string(r.body))
		}
	}
}
