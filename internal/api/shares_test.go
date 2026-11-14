package api_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/realtime"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// shareBlobThreads 是分享服务的线程依赖替身：blob 按 sha 给出探测类型与文件路径。
type shareBlobThreads struct {
	blobs map[string]db.Blob
	paths map[string]string
}

func (shareBlobThreads) Publish(context.Context, string) {}

func (f shareBlobThreads) Blob(_ context.Context, sha string) (db.Blob, string, error) {
	b, ok := f.blobs[sha]
	if !ok {
		return db.Blob{}, "", threads.ErrNotFound
	}
	return b, f.paths[sha], nil
}

type nopShareHub struct{}

func (nopShareHub) ServePublic(http.ResponseWriter, *http.Request, realtime.PublicSub, int64, *int64) {
}
func (nopShareHub) ClosePublic(string) {}

// F7：公开 blob 与 /f 同一白名单——安全栅格图片内联，其余（探测为 text/html、SVG 等）一律附件下载。
func TestPublicShareBlobContentType(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	mimes := map[byte]string{'a': "image/png", 'b': "text/html; charset=utf-8", 'c': "image/svg+xml", 'd': "IMAGE/JPEG"}
	th := shareBlobThreads{blobs: map[string]db.Blob{}, paths: map[string]string{}}
	const tid = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"
	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: tid, Title: "pics", TitleSource: "auto", State: "idle", SessionDir: "/x", CreatedAt: 1, UpdatedAt: 1}))
	var content []string
	for c, m := range mimes {
		sha := strings.Repeat(string(c), 64)
		p := filepath.Join(dir, "blob-"+string(c))
		require.NoError(t, os.WriteFile(p, []byte("body-"+string(c)), 0o600))
		th.blobs[sha], th.paths[sha] = db.Blob{Sha256: sha, Mime: m, CreatedAt: 1}, p
		content = append(content, fmt.Sprintf(`{"type":"image","data":"blob:sha256:%s","mimeType":"image/png"}`, sha))
	}
	payload := `{"type":"message","id":"u1","message":{"role":"user","content":[` + strings.Join(content, ",") + `]}}`
	_, err = st.Q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: tid, File: "s.jsonl", EntryID: "u1", Type: "message", Payload: []byte(payload)})
	require.NoError(t, err)
	leaf := "u1"
	require.NoError(t, st.Q.SetThreadLeaf(ctx, db.SetThreadLeafParams{LeafEntryID: &leaf, ID: tid}))

	svc := share.New(share.Config{PublicURL: h2PublicURL}, st, th, nopShareHub{}, nil, nil, nil)
	sh, err := svc.Create(ctx, tid, apigen.ShareModeSnapshot, true)
	require.NoError(t, err)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = h2PublicURL, dir
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	s.Shares = &api.ShareDeps{Service: svc}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(srv.Close)

	cases := []struct {
		c           byte
		ctype, disp string
	}{
		{'a', "image/png", "inline"},
		{'b', "application/octet-stream", "attachment"},
		{'c', "application/octet-stream", "attachment"},
		{'d', "image/jpeg", "inline"},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + "/api/v1/public/shares/" + sh.Slug + "/blobs/" + strings.Repeat(string(tc.c), 64))
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, mimes[tc.c])
		assert.Equal(t, "body-"+string(tc.c), string(body))
		assert.Equal(t, tc.ctype, resp.Header.Get("Content-Type"), mimes[tc.c])
		assert.Equal(t, tc.disp, resp.Header.Get("Content-Disposition"), mimes[tc.c])
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
		assert.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))
		assert.Equal(t, "noindex, nofollow", resp.Header.Get("X-Robots-Tag"))
	}
}

// 匿名 `GET public/shares/{slug}/identity`：200 + noindex + no-store，只含用到且设了头像的 role；错误 slug 与撤销后 404。
func TestPublicShareIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	const tid = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"
	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: tid, Title: "roles", TitleSource: "auto", State: "idle", SessionDir: "/x", CreatedAt: 1, UpdatedAt: 1}))
	rec := `{"type":"custom","id":"r1","customType":"oryn-model-source","data":{"v":1,"source":"role","model":"p/m","role":"Mare"}}`
	_, err = st.Q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: tid, File: "s.jsonl", EntryID: "r1", Type: "custom", Payload: []byte(rec)})
	require.NoError(t, err)
	parent := "r1"
	_, err = st.Q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: tid, File: "s.jsonl", EntryID: "u1", ParentID: &parent, Type: "message",
		Payload: []byte(`{"type":"message","id":"u1","parentId":"r1","message":{"role":"user","content":"hi"}}`)})
	require.NoError(t, err)
	leaf := "u1"
	require.NoError(t, st.Q.SetThreadLeaf(ctx, db.SetThreadLeafParams{LeafEntryID: &leaf, ID: tid}))
	for _, role := range []string{"Mare", "Nahida"} {
		img := "data:image/png;base64,AVATAR-" + role
		require.NoError(t, st.Q.UpsertRoleAvatar(ctx, db.UpsertRoleAvatarParams{Role: role, AvatarImage: &img, UpdatedAt: 1}))
	}

	svc := share.New(share.Config{PublicURL: h2PublicURL}, st, shareBlobThreads{}, nopShareHub{}, nil, nil, nil)
	sh, err := svc.Create(ctx, tid, apigen.ShareModeLive, true)
	require.NoError(t, err)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = h2PublicURL, dir
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	s.Shares = &api.ShareDeps{Service: svc}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(srv.Close)

	get := func(slug string) (*http.Response, string) {
		resp, err := http.Get(srv.URL + "/api/v1/public/shares/" + slug + "/identity")
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, string(body)
	}
	resp, body := get(sh.Slug)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, "noindex, nofollow", resp.Header.Get("X-Robots-Tag"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Contains(t, body, `"role":"Mare"`)
	assert.Contains(t, body, "AVATAR-Mare")
	assert.NotContains(t, body, "Nahida", "没用到的 role 不出网")
	assert.NotContains(t, body, `"agent"`, "agent 身份未设置：省略")

	for _, slug := range []string{strings.Repeat("0", 32), "not-a-slug"} {
		resp, body := get(slug)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, slug)
		assert.Contains(t, body, `"not_found"`)
		assert.Equal(t, "noindex, nofollow", resp.Header.Get("X-Robots-Tag"))
	}
	require.NoError(t, svc.Revoke(ctx, sh.ID))
	resp, _ = get(sh.Slug)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "撤销后 404")
}
