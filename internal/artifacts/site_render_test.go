package artifacts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/render"
)

// 渲染模式（步骤 4.1）：原样提供版本里的文件，不注入运行时、不标源码；CSP 同帧但 self 是作业根、没有 frame-ancestors。
func TestRenderSite(t *testing.T) {
	e := newEnv(t)
	h := newHost(t, e, "localhost", fakeRuntime)
	a := mkPage(t, e)
	ctx := context.Background()

	v, strict, err := h.RenderTarget(ctx, a.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, v)
	assert.False(t, strict)
	_, _, err = h.RenderTarget(ctx, a.ID, 9)
	assert.Error(t, err)
	_, _, err = h.RenderTarget(ctx, "art_00000000000000000000000000", 0)
	assert.Error(t, err)

	self := "http://" + render.Host + "/tok/"
	serve := func(p string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://"+render.Host+"/tok"+p, nil)
		rec := httptest.NewRecorder()
		h.ServeRender(rec, req, render.Target{ArtifactID: a.ID, Version: v, Self: self}, p)
		return rec
	}
	rec := serve("/")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, page, rec.Body.String())
	csp := rec.Header().Get("Content-Security-Policy")
	assert.True(t, strings.HasPrefix(csp, sandboxFrame+";"), csp)
	assert.Contains(t, csp, "script-src "+self+" ")
	assert.Contains(t, csp, "https://cdn.jsdelivr.net")
	assert.NotContains(t, csp, "frame-ancestors")
	assert.NotContains(t, csp, "'self'")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	rec = serve("/style.css")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "h1{color:red}", rec.Body.String())
	assert.Equal(t, http.StatusNotFound, serve("/missing.html").Code)

	strictRec := httptest.NewRecorder()
	h.ServeRender(strictRec, httptest.NewRequest(http.MethodGet, "/", nil), render.Target{ArtifactID: a.ID, Version: v, Strict: true, Self: self}, "/")
	assert.NotContains(t, strictRec.Header().Get("Content-Security-Policy"), "https://cdn.jsdelivr.net")

	_, err = e.s.Delete(ctx, a.ID)
	require.NoError(t, err)
	_, _, err = h.RenderTarget(ctx, a.ID, 0)
	oe, ok := AsOpError(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, CodeDeleted, oe.Code)
}
