package api_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

const composerPrefsPath = "/api/v1/settings/composer"

func TestComposerPrefsAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)

	assert.Equal(t, 401, e.do("GET", composerPrefsPath, h2Cred{}, nil).status)
	assert.Equal(t, 403, e.do("PUT", composerPrefsPath, h2Cred{cookie: e.webSession()}, map[string]any{"defaultTarget": "role"}).status)

	// 尚无所有者：GET 给缺省值，PUT 409
	r := e.do("GET", composerPrefsPath, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"defaultTarget":"role"}`, string(r.body))
	r = e.do("PUT", composerPrefsPath, c, map[string]any{"defaultTarget": "assistant"})
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "conflict", r.code(t))

	require.NoError(t, e.st.Q.CreateOwner(context.Background(), db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	r = e.do("GET", composerPrefsPath, c, nil)
	assert.JSONEq(t, `{"defaultTarget":"role"}`, string(r.body), "新所有者的缺省值")

	// 往返读写一致
	for _, target := range []string{"assistant", "role", "assistant"} {
		r = e.do("PUT", composerPrefsPath, c, map[string]any{"defaultTarget": target})
		require.Equal(t, 200, r.status, string(r.body))
		assert.JSONEq(t, `{"defaultTarget":"`+target+`"}`, string(r.body))
		r = e.do("GET", composerPrefsPath, h2Cred{cookie: e.webSession()}, nil)
		require.Equal(t, 200, r.status)
		assert.JSONEq(t, `{"defaultTarget":"`+target+`"}`, string(r.body))
	}

	for _, bad := range []map[string]any{{}, {"defaultTarget": "model"}, {"defaultTarget": ""}} {
		r = e.do("PUT", composerPrefsPath, c, bad)
		assert.Equal(t, 400, r.status, "%v: %s", bad, r.body)
		assert.Equal(t, "invalid_request", r.code(t))
	}
	r = e.do("GET", composerPrefsPath, c, nil)
	assert.JSONEq(t, `{"defaultTarget":"assistant"}`, string(r.body), "被拒的请求不写库")
}
