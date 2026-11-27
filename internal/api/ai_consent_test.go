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
	assert.Equal(t, "model:anthropic@api.anthropic.com", st.Recipients[0].ID)
	assert.Equal(t, "Anthropic", st.Recipients[0].Name)
	assert.Equal(t, "api.anthropic.com", *st.Recipients[0].Host)
	assert.Equal(t, []string{"chat"}, st.Recipients[0].Purposes)
	assert.True(t, st.Required)
	assert.Nil(t, st.ConsentedAt)

	// 尚无所有者：PUT / DELETE 409
	r = e.do("PUT", aiConsentPath, c, map[string]any{"recipientIds": []string{"model:anthropic@api.anthropic.com"}})
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "conflict", r.code(t))
	assert.Equal(t, 409, e.do("DELETE", aiConsentPath, c, nil).status)

	require.NoError(t, e.st.Q.CreateOwner(context.Background(), db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))

	// 不在当前接收方里的 id → 400，且不写库
	r = e.do("PUT", aiConsentPath, c, map[string]any{"recipientIds": []string{"model:anthropic@api.anthropic.com", "model:openai@api.openai.com"}})
	assert.Equal(t, 400, r.status, string(r.body))
	assert.Equal(t, "invalid_request", r.code(t))
	assert.True(t, get().Required)

	// 同意 → 不再需要
	r = e.do("PUT", aiConsentPath, c, map[string]any{"recipientIds": []string{"model:anthropic@api.anthropic.com", "model:anthropic@api.anthropic.com"}})
	require.Equal(t, 200, r.status, string(r.body))
	var put aiConsentResp
	r.json(t, &put)
	assert.False(t, put.Required)
	assert.Equal(t, []string{"model:anthropic@api.anthropic.com"}, put.ConsentedRecipientIds)
	require.NotNil(t, put.ConsentedAt)
	assert.Positive(t, *put.ConsentedAt)
	assert.False(t, get().Required)

	// 新增 provider → 再次需要；已同意的记录保持
	putModels("providers:\n  anthropic:\n    apiKey: sk-ant-literal-0123456789abcdef\n  deepseek:\n    apiKey: sk-ds-literal-0123456789abcdef\n")
	st = get()
	assert.True(t, st.Required)
	assert.Equal(t, []string{"model:anthropic@api.anthropic.com"}, st.ConsentedRecipientIds)
	r = e.do("PUT", aiConsentPath, c, map[string]any{"recipientIds": []string{"model:deepseek@api.deepseek.com", "model:anthropic@api.anthropic.com"}})
	require.Equal(t, 200, r.status, string(r.body))
	st = get()
	assert.False(t, st.Required)
	assert.Equal(t, []string{"model:anthropic@api.anthropic.com", "model:deepseek@api.deepseek.com"}, st.ConsentedRecipientIds)

	// 已同意的 provider 下某个模型单独改指到别的主机 → 那是新的接收方，再次需要
	putModels("providers:\n  anthropic:\n    apiKey: sk-ant-literal-0123456789abcdef\n    models:\n      - { id: relay, baseUrl: https://relay.example.net/v1 }\n  deepseek:\n    apiKey: sk-ds-literal-0123456789abcdef\n")
	st = get()
	assert.True(t, st.Required)
	var ids []string
	for _, rc := range st.Recipients {
		ids = append(ids, rc.ID)
	}
	assert.Contains(t, ids, "model:anthropic@relay.example.net")
	r = e.do("PUT", aiConsentPath, c, map[string]any{"recipientIds": ids})
	require.Equal(t, 200, r.status, string(r.body))
	assert.False(t, get().Required)

	// 撤回 → 清空并再次需要
	r = e.do("DELETE", aiConsentPath, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	st = get()
	assert.True(t, st.Required)
	assert.Nil(t, st.ConsentedAt)
	assert.Empty(t, st.ConsentedRecipientIds)
}
