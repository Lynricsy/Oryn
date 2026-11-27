package api_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

const aiConsentPath = "/api/v1/settings/ai-data-consent"

type aiConsentResp struct {
	Recipients []struct {
		ID       string
		Name     string
		Host     *string
		Purposes []string
	}
	ConsentedAt           *int64
	ConsentedRecipientIds []string
	Required              bool
}

func TestAiDataConsentAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	get := func() aiConsentResp {
		t.Helper()
		r := e.do("GET", aiConsentPath, c, nil)
		require.Equal(t, 200, r.status, string(r.body))
		var out aiConsentResp
		r.json(t, &out)
		return out
	}

	assert.Equal(t, 401, e.do("GET", aiConsentPath, h2Cred{}, nil).status)
	assert.Equal(t, 403, e.do("PUT", aiConsentPath, h2Cred{cookie: e.webSession()}, map[string]any{"recipientIds": []string{}}).status)

	// 没有任何接收方：不需要同意
	r := e.do("GET", aiConsentPath, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"recipients":[],"consentedAt":null,"consentedRecipientIds":[],"required":false}`, string(r.body))

	putModels := func(content string) {
		t.Helper()
		r := e.do("PUT", "/api/v1/agent-config/files/models.yml", c, []byte(content))
		require.Equal(t, 200, r.status, string(r.body))
	}
	putModels("providers:\n  anthropic:\n    apiKey: sk-ant-literal-0123456789abcdef\n")
	st := get()
	require.Len(t, st.Recipients, 1)
	assert.Equal(t, "model:anthropic", st.Recipients[0].ID)
	assert.Equal(t, "Anthropic", st.Recipients[0].Name)
	assert.Equal(t, "api.anthropic.com", *st.Recipients[0].Host)
	assert.Equal(t, []string{"chat"}, st.Recipients[0].Purposes)
	assert.True(t, st.Required)
	assert.Nil(t, st.ConsentedAt)

	// 尚无所有者：PUT / DELETE 409
	r = e.do("PUT", aiConsentPath, c, map[string]any{"recipientIds": []string{"model:anthropic"}})
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "conflict", r.code(t))
	assert.Equal(t, 409, e.do("DELETE", aiConsentPath, c, nil).status)

	require.NoError(t, e.st.Q.CreateOwner(context.Background(), db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))

	// 不在当前接收方里的 id → 400，且不写库
	r = e.do("PUT", aiConsentPath, c, map[string]any{"recipientIds": []string{"model:anthropic", "model:openai"}})
	assert.Equal(t, 400, r.status, string(r.body))
	assert.Equal(t, "invalid_request", r.code(t))
	assert.True(t, get().Required)

	// 同意 → 不再需要
	r = e.do("PUT", aiConsentPath, c, map[string]any{"recipientIds": []string{"model:anthropic", "model:anthropic"}})
	require.Equal(t, 200, r.status, string(r.body))
	var put aiConsentResp
	r.json(t, &put)
	assert.False(t, put.Required)
	assert.Equal(t, []string{"model:anthropic"}, put.ConsentedRecipientIds)
	require.NotNil(t, put.ConsentedAt)
	assert.Positive(t, *put.ConsentedAt)
	assert.False(t, get().Required)

	// 新增 provider → 再次需要；已同意的记录保持
	putModels("providers:\n  anthropic:\n    apiKey: sk-ant-literal-0123456789abcdef\n  deepseek:\n    apiKey: sk-ds-literal-0123456789abcdef\n")
	st = get()
	assert.True(t, st.Required)
	assert.Equal(t, []string{"model:anthropic"}, st.ConsentedRecipientIds)
	r = e.do("PUT", aiConsentPath, c, map[string]any{"recipientIds": []string{"model:deepseek", "model:anthropic"}})
	require.Equal(t, 200, r.status, string(r.body))
	st = get()
	assert.False(t, st.Required)
	assert.Equal(t, []string{"model:anthropic", "model:deepseek"}, st.ConsentedRecipientIds)

	// 撤回 → 清空并再次需要
	r = e.do("DELETE", aiConsentPath, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	st = get()
	assert.True(t, st.Required)
	assert.Nil(t, st.ConsentedAt)
	assert.Empty(t, st.ConsentedRecipientIds)
}
