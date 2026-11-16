package api_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 发布 REST：密钥扫描 409 与确认、私有直接发布、换码、取消发布、grant 地址、列表、published 字段、带 unpublish 的删除、设置。
func TestArtifactPublishREST(t *testing.T) {
	e, svc := newArtifactsEnv(t)
	ctx := t.Context()
	base, err := portalhost.NewBase("http://localhost:7600", "")
	require.NoError(t, err)
	svc.ConfigurePublishing(artifacts.PublishConfig{Base: base, GrantKey: artifacts.DeriveGrantKey([]byte("k")), Scan: (&share.Service{}).ScanContent})

	a, err := svc.Create(ctx, artifacts.CreateParams{Title: "密钥页", Files: artFiles("index.html", `<p>sk-test-abcdefghijklmnopqrstuvwx0123</p>`)})
	require.NoError(t, err)
	b, err := svc.Create(ctx, artifacts.CreateParams{Title: "干净页", Files: artFiles("index.html", "<h1>hi</h1>")})
	require.NoError(t, err)

	code, body := e.do("GET", "/api/v1/artifacts/"+a.ID+"/publication", nil)
	require.Equal(t, 200, code, string(body))
	st := decodeAs[apigen.ArtifactPublicationState](t, body)
	assert.Nil(t, st.Publication)
	assert.Zero(t, st.UsageBytes)

	// public：扫描命中 → 409 ShareScanConflict；确认后成功
	code, body = e.do("PUT", "/api/v1/artifacts/"+a.ID+"/publication", map[string]any{"access": "public"})
	require.Equal(t, 409, code, string(body))
	conflict := decodeAs[apigen.ShareScanConflict](t, body)
	assert.Equal(t, apigen.ShareScanConflictCodeSecretsFound, conflict.Code)
	require.NotEmpty(t, conflict.Scan.Findings)
	assert.Equal(t, "index.html", conflict.Scan.Findings[0].EntryID)
	code, body = e.do("PUT", "/api/v1/artifacts/"+a.ID+"/publication", map[string]any{"access": "public", "acknowledgeFindings": true})
	require.Equal(t, 200, code, string(body))
	st = decodeAs[apigen.ArtifactPublicationState](t, body)
	require.NotNil(t, st.Publication)
	assert.Equal(t, apigen.ArtifactAccessPublic, st.Publication.Access)
	assert.Nil(t, st.Publication.Code)
	assert.Equal(t, a.Size, st.UsageBytes)
	assert.Regexp(t, `^http://localhost:7600/p/[a-z2-7]{26}/$`, st.Publication.URL)

	// private：不扫描，直接成功
	code, body = e.do("PUT", "/api/v1/artifacts/"+b.ID+"/publication", map[string]any{"access": "private"})
	require.Equal(t, 200, code, string(body))
	code, body = e.do("PUT", "/api/v1/artifacts/"+b.ID+"/publication", map[string]any{"access": "code"})
	require.Equal(t, 200, code, string(body))
	st = decodeAs[apigen.ArtifactPublicationState](t, body)
	require.NotNil(t, st.Publication.Code)
	assert.Regexp(t, `^[23456789abcdefghjkmnpqrstuvwxyz]{4}$`, *st.Publication.Code)
	assert.Equal(t, a.Size+b.Size, st.UsageBytes)

	// 换码
	code, body = e.do("POST", "/api/v1/artifacts/"+b.ID+"/publication/code", nil)
	require.Equal(t, 200, code, string(body))
	rot := decodeAs[apigen.ArtifactPublicationState](t, body)
	require.NotNil(t, rot.Publication.Code)
	assert.False(t, rot.Publication.CodeLocked)

	// grant：带授权段的地址
	pb, err := svc.Publication(ctx, b.ID)
	require.NoError(t, err)
	code, body = e.do("POST", "/api/v1/artifact-publications/"+pb.Slug+"/grant", nil)
	require.Equal(t, 200, code, string(body))
	link := decodeAs[apigen.ArtifactOpenLink](t, body)
	m := regexp.MustCompile(`^` + regexp.QuoteMeta(pb.URL) + `~([^/]+)/$`).FindStringSubmatch(link.URL)
	require.NotNil(t, m, link.URL)
	assert.True(t, svc.GrantValid(*pb, m[1]))
	code, _ = e.do("POST", "/api/v1/artifact-publications/aaaaaaaaaaaaaaaaaaaaaaaaaa/grant", nil)
	assert.Equal(t, 404, code)

	// 列表与 published 字段
	code, body = e.do("GET", "/api/v1/artifact-publications", nil)
	require.Equal(t, 200, code, string(body))
	pl := decodeAs[apigen.ArtifactPublicationList](t, body)
	require.Len(t, pl.Publications, 2)
	code, body = e.do("GET", "/api/v1/artifacts", nil)
	require.Equal(t, 200, code, string(body))
	for _, x := range decodeAs[apigen.ArtifactList](t, body).Artifacts {
		require.NotNil(t, x.Published, x.ID)
	}

	// 带 unpublish 的删除删掉发布记录；普通删除保留
	code, _ = e.do("DELETE", "/api/v1/artifacts/"+a.ID+"?unpublish=true", nil)
	require.Equal(t, 204, code)
	_, err = e.st.Q.GetArtifactPublication(ctx, a.ID)
	assert.Error(t, err)
	code, _ = e.do("DELETE", "/api/v1/artifacts/"+b.ID, nil)
	require.Equal(t, 204, code)
	_, err = e.st.Q.GetArtifactPublication(ctx, b.ID)
	require.NoError(t, err)

	// 取消发布
	code, _ = e.do("DELETE", "/api/v1/artifacts/"+b.ID+"/publication", nil)
	require.Equal(t, 204, code)
	code, body = e.do("GET", "/api/v1/artifacts/"+b.ID+"/publication", nil)
	require.Equal(t, 200, code, string(body))
	assert.Nil(t, decodeAs[apigen.ArtifactPublicationState](t, body).Publication)
	code, _ = e.do("POST", "/api/v1/artifacts/"+b.ID+"/publication/code", nil)
	assert.Equal(t, 404, code)
}

func TestArtifactPublishPrefsREST(t *testing.T) {
	e, _ := newArtifactsEnv(t)
	code, body := e.do("GET", "/api/v1/settings/artifacts", nil)
	require.Equal(t, 200, code, string(body))
	assert.False(t, decodeAs[apigen.ArtifactPrefs](t, body).AutoPublish)
	code, _ = e.do("PUT", "/api/v1/settings/artifacts", map[string]any{"autoPublish": true})
	assert.Equal(t, 409, code, "没有所有者")

	require.NoError(t, e.st.Q.CreateOwner(t.Context(), db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	code, body = e.do("PUT", "/api/v1/settings/artifacts", map[string]any{"autoPublish": true})
	require.Equal(t, 200, code, string(body))
	code, body = e.do("GET", "/api/v1/settings/artifacts", nil)
	require.Equal(t, 200, code, string(body))
	assert.True(t, decodeAs[apigen.ArtifactPrefs](t, body).AutoPublish)
}
