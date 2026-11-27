package api_test

import (
	"context"
	"encoding/json"
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
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const viewThread = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"

// newViewFramesEnv 组装视图帧的 REST、匿名分享端点与 /_af/ 帧挂载；线程里有一次成功的 render_html 调用（call_html）、
// 一次 render_ui 调用（call_ui）与一次还没有结果的 render_html 调用（call_pending）。
func newViewFramesEnv(t *testing.T) (*httptest.Server, *share.Service) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "data"))
	require.NoError(t, err)
	svc, err := artifacts.New(artifacts.Config{DataDir: filepath.Join(dir, "data"), PublicURL: "http://localhost:7600"}, st, nil)
	require.NoError(t, err)
	base, err := portalhost.NewBase("http://localhost:7600", "")
	require.NoError(t, err)
	host := artifacts.NewHost(svc, artifacts.HostConfig{Base: base, Asset: func(string) ([]byte, bool) { return nil, false }})

	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: viewThread, Title: "views", TitleSource: "auto", State: "idle",
		SessionDir: "/x", CreatedAt: 1, UpdatedAt: 1}))
	add := func(id, parent string, msg map[string]any) {
		b, err := json.Marshal(map[string]any{"type": "message", "id": id, "parentId": parent, "message": msg})
		require.NoError(t, err)
		var pp *string
		if parent != "" {
			pp = &parent
		}
		_, err = st.Q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: viewThread, File: "s.jsonl", EntryID: id, ParentID: pp, Type: "message", Payload: b})
		require.NoError(t, err)
	}
	call := func(id, name string, args map[string]any) map[string]any {
		return map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "toolCall", "id": id, "name": name, "arguments": args}}}
	}
	result := func(id string) map[string]any {
		return map[string]any{"role": "toolResult", "toolCallId": id, "toolName": "render_html", "content": "Rendered."}
	}
	add("u1", "", map[string]any{"role": "user", "content": "show me"})
	add("a1", "u1", call("call_html", "render_html", map[string]any{"html": "<p>hi</p>", "libs": "chartjs"}))
	add("r1", "a1", result("call_html"))
	add("a2", "r1", call("call_ui", "render_ui", map[string]any{"html": "<p>not html</p>"}))
	add("r2", "a2", result("call_ui"))
	add("a3", "r2", call("call_pending", "render_html", map[string]any{"html": "<p>pending</p>"}))
	leaf := "a3"
	require.NoError(t, st.Q.SetThreadLeaf(ctx, db.SetThreadLeafParams{LeafEntryID: &leaf, ID: viewThread}))

	shares := share.New(share.Config{PublicURL: "http://localhost:7600"}, st, shareBlobThreads{}, nopShareHub{}, nil, nil, nil)
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
	s.Shares = &api.ShareDeps{Service: shares}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(func() {
		srv.Close()
		_ = st.Close()
	})
	return srv, shares
}

func openView(t *testing.T, url, callID string, header map[string]string) (int, apigen.ViewFrame) {
	t.Helper()
	resp, body := send(t, "POST", url, strings.NewReader(`{"toolCallId":"`+callID+`"}`), header)
	var f apigen.ViewFrame
	if resp.StatusCode == http.StatusCreated {
		require.NoError(t, json.Unmarshal(body, &f))
	}
	return resp.StatusCode, f
}

func TestThreadViewFrame(t *testing.T) {
	srv, _ := newViewFramesEnv(t)
	url := srv.URL + "/api/v1/threads/" + viewThread + "/view-frames"

	resp, _ := send(t, "POST", url, strings.NewReader(`{"toolCallId":"call_html"}`), map[string]string{"Content-Type": "application/json"})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "线程端点要登录")

	hdr := map[string]string{"X-Test-Auth": "ok", "Content-Type": "application/json", "Origin": "http://localhost:5173"}
	code, f := openView(t, url, "call_pending", hdr)
	require.Equal(t, http.StatusCreated, code, "执行中（还没有结果）的调用也能开帧")
	code, f = openView(t, url, "call_html", hdr)
	require.Equal(t, http.StatusCreated, code)
	assert.Regexp(t, `^[a-z2-7]{26}$`, f.Token)
	assert.Equal(t, "http://localhost:7600/_af/"+f.Token+"/", f.URL)

	resp, body := send(t, "GET", local(srv, f.URL), nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "帧请求不经鉴权")
	assert.Contains(t, string(body), "<body><p>hi</p></body>")
	csp := resp.Header.Get("Content-Security-Policy")
	assert.True(t, strings.HasPrefix(csp, "sandbox allow-scripts; default-src 'none';"), csp)
	assert.True(t, strings.HasSuffix(csp, "frame-ancestors http://localhost:5173"), csp)

	for _, id := range []string{"call_ui", "missing"} {
		code, _ := openView(t, url, id, hdr)
		assert.Equal(t, http.StatusNotFound, code, id)
	}
	code, _ = openView(t, srv.URL+"/api/v1/threads/T-00000000-0000-7000-8000-000000000000/view-frames", "call_html", hdr)
	assert.Equal(t, http.StatusNotFound, code, "线程不存在")
	code, _ = openView(t, url, "", hdr)
	assert.Equal(t, http.StatusBadRequest, code)

	resp, _ = send(t, "DELETE", srv.URL+"/api/v1/artifact-frames/"+f.Token, nil, hdr)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp, _ = send(t, "GET", local(srv, f.URL), nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "关帧后 404")
}

func TestPublicShareViewFrame(t *testing.T) {
	srv, shares := newViewFramesEnv(t)
	sh, err := shares.Create(t.Context(), viewThread, apigen.ShareModeLive, true)
	require.NoError(t, err)
	url := srv.URL + "/api/v1/public/shares/" + sh.Slug + "/view-frames"
	hdr := map[string]string{"Content-Type": "application/json"}

	code, f := openView(t, url, "call_html", hdr)
	require.Equal(t, http.StatusCreated, code, "匿名可申请")
	resp, body := send(t, "GET", local(srv, f.URL), nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "<p>hi</p>")

	for _, id := range []string{"call_ui", "call_pending", "missing"} {
		code, _ := openView(t, url, id, hdr)
		assert.Equal(t, http.StatusNotFound, code, "%s：不是 render_html 或还没有成功结果", id)
	}
	code, _ = openView(t, srv.URL+"/api/v1/public/shares/nope/view-frames", "call_html", hdr)
	assert.Equal(t, http.StatusNotFound, code)

	require.NoError(t, shares.Revoke(t.Context(), sh.ID))
	resp, _ = send(t, "GET", local(srv, f.URL), nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "撤销后帧即 404")
	code, _ = openView(t, url, "call_html", hdr)
	assert.Equal(t, http.StatusNotFound, code)
}
