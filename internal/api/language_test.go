package api_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

const languageSettingsPath = "/api/v1/settings/language"

func TestLanguageSettingsAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)

	assert.Equal(t, 401, e.do("GET", languageSettingsPath, h2Cred{}, nil).status)
	assert.Equal(t, 403, e.do("PUT", languageSettingsPath, h2Cred{cookie: e.webSession()}, map[string]any{"language": "en"}).status)

	// 尚无所有者：GET 给缺省值，PUT 409
	r := e.do("GET", languageSettingsPath, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"language":"zh-CN"}`, string(r.body))
	r = e.do("PUT", languageSettingsPath, c, map[string]any{"language": "en"})
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "conflict", r.code(t))

	require.NoError(t, e.st.Q.CreateOwner(context.Background(), db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	r = e.do("GET", languageSettingsPath, c, nil)
	assert.JSONEq(t, `{"language":"zh-CN"}`, string(r.body), "新所有者的缺省值")

	// 往返读写一致
	for _, lang := range []string{"en", "zh-CN", "en"} {
		r = e.do("PUT", languageSettingsPath, c, map[string]any{"language": lang})
		require.Equal(t, 200, r.status, string(r.body))
		assert.JSONEq(t, `{"language":"`+lang+`"}`, string(r.body))
		r = e.do("GET", languageSettingsPath, h2Cred{cookie: e.webSession()}, nil)
		require.Equal(t, 200, r.status)
		assert.JSONEq(t, `{"language":"`+lang+`"}`, string(r.body))
	}

	// 非法值拒绝且不写库
	for _, bad := range []map[string]any{{}, {"language": "fr"}, {"language": ""}} {
		r = e.do("PUT", languageSettingsPath, c, bad)
		assert.Equal(t, 400, r.status, "%v: %s", bad, r.body)
		assert.Equal(t, "invalid_request", r.code(t))
	}
	r = e.do("GET", languageSettingsPath, c, nil)
	assert.JSONEq(t, `{"language":"en"}`, string(r.body), "被拒的请求不写库")
}
