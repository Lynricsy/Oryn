package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/store"
)

// newFramesEnv 组装主域回退模式（未配置 portal_domain）的 REST 与 /_af/ 帧挂载，接法同 server.go。
// 鉴权中间件只放行带 X-Test-Auth 的请求，用来证明帧请求不经鉴权（帧请求不带 cookie，令牌就是凭证）。
func newFramesEnv(t *testing.T) (*httptest.Server, *artifacts.Service) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "data"))
	require.NoError(t, err)
	svc, err := artifacts.New(artifacts.Config{DataDir: filepath.Join(dir, "data"), PublicURL: "http://localhost:7600"}, st, nil)
	require.NoError(t, err)
	base, err := portalhost.NewBase("http://localhost:7600", "")
	require.NoError(t, err)
	host := artifacts.NewHost(svc, artifacts.HostConfig{Base: base})
	cfg := config.DefaultOrynd()
	cfg.DataDir = filepath.Join(dir, "data")
	s := &api.Server{Deps: api.Deps{
		Config: cfg, Store: st, FrameSrc: "'self'",
		AuthMiddleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Test-Auth") != "ok" {
					api.WriteError(w, http.StatusUnauthorized, "unauthorized", "no session")
					return
				}
				next.ServeHTTP(w, r)
			})
		},
		Mounts: map[string]http.Handler{artifacts.FramePathPrefix + "*": host.Handler()},
	}}
	s.Artifacts = &api.ArtifactsDeps{Service: svc, Host: host}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(func() {
		srv.Close()
		_ = st.Close()
	})
	return srv, svc
}

func send(t *testing.T, method, url string, body io.Reader, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	require.NoError(t, err)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

var authed = map[string]string{"X-Test-Auth": "ok", "Content-Type": "application/json"}

// local 把 public_url（http://localhost:7600）下的帧地址换到测试服务器上。
func local(srv *httptest.Server, url string) string {
	return srv.URL + strings.TrimPrefix(url, "http://localhost:7600")
}

// 申请帧、在主域回退路径上取帧（不经鉴权）、关闭帧、在浏览器中打开、能力。
func TestArtifactFramesREST(t *testing.T) {
	srv, svc := newFramesEnv(t)
	a, err := svc.Create(t.Context(), artifacts.CreateParams{Title: "周报", Files: artFiles("index.html", "<h1>hi</h1>", "a.css", "h1{}")})
	require.NoError(t, err)

	resp, body := send(t, "POST", srv.URL+"/api/v1/artifacts/"+a.ID+"/frames", strings.NewReader(`{}`),
		map[string]string{"X-Test-Auth": "ok", "Content-Type": "application/json", "Origin": "http://localhost:5173"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	fr := decodeAs[apigen.ArtifactFrame](t, body)
	assert.Equal(t, "http://localhost:7600/_af/"+fr.Token+"/", fr.URL)

	resp, body = send(t, "GET", srv.URL+"/_af/"+fr.Token+"/", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	csp := resp.Header.Get("Content-Security-Policy")
	assert.True(t, strings.HasPrefix(csp, "sandbox allow-scripts allow-forms allow-downloads;"), csp)
	assert.NotContains(t, csp, "'self'")
	assert.Contains(t, csp, "default-src 'none'; script-src http://localhost:7600/_af/"+fr.Token+"/ ")
	assert.True(t, strings.HasSuffix(csp, "frame-ancestors http://localhost:5173"), csp)
	assert.Contains(t, string(body), `<h1 data-oryn-src="`)
	resp, _ = send(t, "GET", srv.URL+"/_af/"+fr.Token+"/a.css", nil, map[string]string{"Origin": "null"})
	assert.Equal(t, "null", resp.Header.Get("Access-Control-Allow-Origin"))

	// 不带 Origin → public_url 的源；钉住不存在的版本 → 404；未登录 → 401
	resp, body = send(t, "POST", srv.URL+"/api/v1/artifacts/"+a.ID+"/frames", strings.NewReader(`{"version":1}`), authed)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	pinned := decodeAs[apigen.ArtifactFrame](t, body)
	resp, _ = send(t, "GET", local(srv, pinned.URL), nil, nil)
	assert.True(t, strings.HasSuffix(resp.Header.Get("Content-Security-Policy"), "frame-ancestors http://localhost:7600"))
	resp, _ = send(t, "POST", srv.URL+"/api/v1/artifacts/"+a.ID+"/frames", strings.NewReader(`{"version":9}`), authed)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = send(t, "POST", srv.URL+"/api/v1/artifacts/"+a.ID+"/frames", strings.NewReader(`{}`), nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	resp, _ = send(t, "DELETE", srv.URL+"/api/v1/artifact-frames/"+fr.Token, nil, authed)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp, _ = send(t, "GET", srv.URL+"/_af/"+fr.Token+"/", nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, body = send(t, "POST", srv.URL+"/api/v1/artifacts/"+a.ID+"/open-link", strings.NewReader(`{}`), authed)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	link := decodeAs[apigen.ArtifactOpenLink](t, body)
	resp, body = send(t, "GET", local(srv, link.URL), nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Security-Policy"), "allow-top-navigation-by-user-activation")
	assert.Equal(t, "<h1>hi</h1>", string(body))

	resp, body = send(t, "GET", srv.URL+"/api/v1/artifacts/capabilities", nil, authed)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	caps := decodeAs[apigen.ArtifactCapabilities](t, body)
	assert.Equal(t, apigen.ArtifactFramesPath, caps.Frames)
	assert.False(t, caps.Render)
	assert.False(t, caps.Sample)

	_, err = svc.Delete(t.Context(), a.ID)
	require.NoError(t, err)
	resp, _ = send(t, "GET", local(srv, pinned.URL), nil, nil)
	assert.Equal(t, http.StatusGone, resp.StatusCode)
	resp, _ = send(t, "POST", srv.URL+"/api/v1/artifacts/"+a.ID+"/frames", strings.NewReader(`{}`), authed)
	assert.Equal(t, http.StatusGone, resp.StatusCode)
}

func multipartBody(t *testing.T, kv ...string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for i := 0; i+1 < len(kv); i += 2 {
		w, err := mw.CreateFormFile(kv[i], kv[i])
		require.NoError(t, err)
		_, _ = io.WriteString(w, kv[i+1])
	}
	require.NoError(t, mw.Close())
	return &buf, mw.FormDataContentType()
}

// 导入（成功、缺入口、文件太多）与导出（html / zip 的响应头）。
func TestArtifactImportExportREST(t *testing.T) {
	srv, _ := newFramesEnv(t)
	importURL := srv.URL + "/api/v1/artifacts/import?name=report.html"
	body, ct := multipartBody(t, "index.html", "<title>周报</title><p>hi</p>")
	resp, b := send(t, "POST", importURL, body, map[string]string{"X-Test-Auth": "ok", "Content-Type": ct})
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(b))
	a := decodeAs[apigen.Artifact](t, b)
	assert.Equal(t, "周报", a.Title)
	assert.Equal(t, apigen.ArtifactOrigin("import"), a.Origin)
	assert.Nil(t, a.ThreadID)

	body, ct = multipartBody(t, "page.html", "<p>x</p>")
	resp, b = send(t, "POST", importURL, body, map[string]string{"X-Test-Auth": "ok", "Content-Type": ct})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "no_index", decodeAs[map[string]any](t, b)["code"])

	kv := []string{"index.html", "<p>x</p>"}
	for i := range artifacts.MaxVersionFiles {
		kv = append(kv, fmt.Sprintf("f/%d.txt", i), "x")
	}
	body, ct = multipartBody(t, kv...)
	resp, b = send(t, "POST", importURL, body, map[string]string{"X-Test-Auth": "ok", "Content-Type": ct})
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	assert.Equal(t, "too_many", decodeAs[map[string]any](t, b)["code"])

	resp, _ = send(t, "POST", importURL, strings.NewReader(`{}`), authed)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, b = send(t, "GET", srv.URL+"/api/v1/artifacts/"+a.ID+"/export?format=html", nil, authed)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(t, "<title>周报</title><p>hi</p>", string(b))
	assert.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment; filename*=UTF-8''%E5%91%A8%E6%8A%A5.html"))

	resp, b = send(t, "GET", srv.URL+"/api/v1/artifacts/"+a.ID+"/export?format=zip", nil, authed)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(t, "application/zip", resp.Header.Get("Content-Type"))
	assert.Equal(t, []byte("PK"), b[:2])

	resp, b = send(t, "GET", srv.URL+"/api/v1/artifacts/"+a.ID+"/export?format=pdf", nil, authed)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var e map[string]string
	require.NoError(t, json.Unmarshal(b, &e))
	assert.Equal(t, "invalid", e["code"])
}
