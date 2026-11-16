package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 同意 REST：列表按 artifact 合并能力且不含回收站里的；回答已结束的请求回 404；收回整个 artifact 的授权。
func TestArtifactConsentsREST(t *testing.T) {
	e, svc := newArtifactsEnv(t)
	ctx := t.Context()

	code, body := e.do("GET", "/api/v1/artifact-consents", nil)
	require.Equal(t, 200, code, string(body))
	assert.Empty(t, decodeAs[apigen.ArtifactConsentList](t, body).Consents)

	a, err := svc.Create(ctx, artifacts.CreateParams{Title: "仓库浏览", Files: artFiles("index.html", "<h1>x</h1>"), Capabilities: []string{"files", "sample"}})
	require.NoError(t, err)
	b, err := svc.Create(ctx, artifacts.CreateParams{Title: "问答", Files: artFiles("index.html", "<h1>y</h1>"), Capabilities: []string{"sample"}})
	require.NoError(t, err)
	for _, g := range []db.UpsertArtifactConsentParams{
		{ArtifactID: a.ID, Capability: "sample", GrantedAt: 10},
		{ArtifactID: a.ID, Capability: "files", GrantedAt: 30},
		{ArtifactID: b.ID, Capability: "sample", GrantedAt: 20},
	} {
		require.NoError(t, e.st.Q.UpsertArtifactConsent(ctx, g))
	}
	code, body = e.do("GET", "/api/v1/artifact-consents", nil)
	require.Equal(t, 200, code, string(body))
	list := decodeAs[apigen.ArtifactConsentList](t, body).Consents
	require.Len(t, list, 2)
	assert.Equal(t, a.ID, list[0].ArtifactID)
	assert.Equal(t, "仓库浏览", list[0].Title)
	assert.Equal(t, int64(30), list[0].GrantedAt)
	assert.Equal(t, []apigen.ArtifactConsentCapability{apigen.ArtifactConsentFiles, apigen.ArtifactConsentSample}, list[0].Capabilities)
	assert.Equal(t, b.ID, list[1].ArtifactID)

	// 收回 a；b 进回收站后也不再列出
	code, body = e.do("DELETE", "/api/v1/artifact-consents/"+a.ID, nil)
	require.Equal(t, 204, code, string(body))
	_, err = svc.Delete(ctx, b.ID)
	require.NoError(t, err)
	code, body = e.do("GET", "/api/v1/artifact-consents", nil)
	require.Equal(t, 200, code, string(body))
	assert.Empty(t, decodeAs[apigen.ArtifactConsentList](t, body).Consents)

	code, body = e.do("DELETE", "/api/v1/artifact-consents/bogus", nil)
	assert.Equal(t, 400, code, string(body))
	code, body = e.do("POST", "/api/v1/artifact-consents/acr_gone", map[string]any{"allow": true})
	assert.Equal(t, 404, code, string(body))
	code, body = e.do("POST", "/api/v1/artifact-consents/acr_gone", map[string]any{"allow": "yes"})
	assert.Equal(t, 400, code, string(body))
}
