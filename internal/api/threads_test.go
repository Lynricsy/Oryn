package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/threads/threadstest"
)

var (
	fakeompOnce sync.Once
	fakeompPath string
	fakeompErr  error
)

func fakeomp(t *testing.T) string {
	t.Helper()
	fakeompOnce.Do(func() {
		dir, err := os.MkdirTemp("", "oryn-api-fakeomp-*")
		if err != nil {
			fakeompErr = err
			return
		}
		fakeompPath, fakeompErr = threadstest.BuildFakeOmp(dir)
	})
	require.NoError(t, fakeompErr)
	return fakeompPath
}

type threadsEnv struct {
	t   *testing.T
	st  *store.Store
	svc *threads.Service
	srv *httptest.Server
	dir string
}

func newThreadsEnv(t *testing.T) *threadsEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "data"))
	require.NoError(t, err)
	orbs, err := threadstest.NewOrbs(st, fakeomp(t), filepath.Join(dir, "orbs"), "FAKEOMP_DELAY_MS=5")
	require.NoError(t, err)
	svc := threads.New(threads.Config{DataDir: filepath.Join(dir, "data")}, st, orbs)
	cfg := config.DefaultOrynd()
	cfg.DataDir = filepath.Join(dir, "data")
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st}}
	s.Threads = &api.ThreadsDeps{Service: svc}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(func() {
		srv.Close()
		orbs.Close()
		svc.Close()
		_ = st.Close()
	})
	return &threadsEnv{t: t, st: st, svc: svc, srv: srv, dir: dir}
}

func (e *threadsEnv) do(method, path string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	require.NoError(e.t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestThreadsAPI(t *testing.T) {
	e := newThreadsEnv(t)

	code, _ := e.do("POST", "/api/v1/threads", map[string]any{"prompt": " ", "executor": map[string]any{"kind": "new_orb"}})
	assert.Equal(t, 400, code)
	code, _ = e.do("POST", "/api/v1/threads", map[string]any{"prompt": "x", "projectId": "prj_missing", "executor": map[string]any{"kind": "new_orb"}})
	assert.Equal(t, 404, code)

	code, body := e.do("POST", "/api/v1/threads", map[string]any{
		"prompt": "hello", "executor": map[string]any{"kind": "new_orb"}, "labels": []string{"a"}, "title": "我的线程",
	})
	require.Equal(t, 201, code, string(body))
	var env apigen.ThreadEnvelope
	require.NoError(t, json.Unmarshal(body, &env))
	th := env.Thread
	assert.Equal(t, apigen.ThreadState("provisioning"), th.State)
	assert.Equal(t, []string{"a"}, th.Labels)
	assert.Equal(t, apigen.ThreadTitleSource("user"), th.TitleSource)

	require.Eventually(t, func() bool {
		code, body := e.do("GET", "/api/v1/threads/"+th.ID, nil)
		var got apigen.Thread
		_ = json.Unmarshal(body, &got)
		return code == 200 && got.State == "idle" && got.LeafEntryID != nil
	}, 20*time.Second, 20*time.Millisecond)

	code, body = e.do("GET", "/api/v1/threads/"+th.ID+"/entries?limit=2", nil)
	require.Equal(t, 200, code)
	var page threads.EntryPage
	require.NoError(t, json.Unmarshal(body, &page))
	assert.Len(t, page.Entries, 2)
	assert.True(t, page.HasMore)
	code, body = e.do("GET", "/api/v1/threads/"+th.ID+"/entries?after=2", nil)
	require.Equal(t, 200, code)
	require.NoError(t, json.Unmarshal(body, &page))
	assert.False(t, page.HasMore)
	assert.EqualValues(t, 3, page.Entries[0].Seq)
	assert.Equal(t, 400, func() int { c, _ := e.do("GET", "/api/v1/threads/"+th.ID+"/entries?limit=501", nil); return c }())

	// PATCH：标签替换、归档、颜色清除
	code, body = e.do("PATCH", "/api/v1/threads/"+th.ID, map[string]any{"labels": []string{"b", "c"}, "color": "red", "pinned": true})
	require.Equal(t, 200, code, string(body))
	var got apigen.Thread
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, []string{"b", "c"}, got.Labels)
	require.NotNil(t, got.Color)
	assert.True(t, got.Pinned)
	code, body = e.do("PATCH", "/api/v1/threads/"+th.ID, map[string]any{"color": "", "archived": true, "title": "新名字"})
	require.Equal(t, 200, code)
	got = apigen.Thread{}
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Nil(t, got.Color)
	assert.NotNil(t, got.ArchivedAt)
	assert.Equal(t, "新名字", got.Title)

	var list apigen.ThreadList
	_, body = e.do("GET", "/api/v1/threads", nil)
	require.NoError(t, json.Unmarshal(body, &list))
	assert.Empty(t, list.Threads, "缺省只列未归档")
	_, body = e.do("GET", "/api/v1/threads?archived=true&q=%E6%96%B0", nil)
	require.NoError(t, json.Unmarshal(body, &list))
	assert.Len(t, list.Threads, 1)
	_, body = e.do("GET", "/api/v1/threads?archived=true&project=none&q=c", nil)
	require.NoError(t, json.Unmarshal(body, &list))
	assert.Len(t, list.Threads, 1, "q 也匹配标签")

	code, _ = e.do("POST", "/api/v1/threads/"+th.ID+"/wake", nil)
	assert.Equal(t, 202, code)
	code, _ = e.do("POST", "/api/v1/threads/"+th.ID+"/fork", map[string]any{"env": "bogus"})
	assert.Equal(t, 400, code)

	// 挂到已有 orb
	code, body = e.do("POST", "/api/v1/threads", map[string]any{"prompt": "again", "executor": map[string]any{"kind": "orb", "orbId": *th.OrbID}})
	require.Equal(t, 201, code, string(body))
	require.NoError(t, json.Unmarshal(body, &env))
	assert.Equal(t, *th.OrbID, *env.Thread.OrbID)

	code, _ = e.do("DELETE", "/api/v1/threads/"+th.ID, nil)
	assert.Equal(t, 204, code)
	code, _ = e.do("GET", "/api/v1/threads/"+th.ID, nil)
	assert.Equal(t, 404, code)
}

func TestGetBlob(t *testing.T) {
	e := newThreadsEnv(t)
	data := []byte("\x89PNG\r\n\x1a\nfake image")
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	code, _ := e.do("GET", "/api/v1/blobs/"+sha, nil)
	assert.Equal(t, 404, code)

	path := filepath.Join(e.dir, "data", "blobs", sha[:2], sha)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o600))
	require.NoError(t, e.st.Q.InsertBlob(context.Background(), db.InsertBlobParams{Sha256: sha, Size: int64(len(data)), Mime: "image/png", CreatedAt: 1}))
	resp, err := http.Get(e.srv.URL + "/api/v1/blobs/" + sha)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, data, b)
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	assert.Contains(t, resp.Header.Get("Cache-Control"), "immutable")
}

// F7：私有 blob 与公开 blob、/f 同一白名单——安全栅格图片内联，探测为 text/html、SVG 等一律附件下载。
func TestGetBlobContentTypeWhitelist(t *testing.T) {
	e := newThreadsEnv(t)
	cases := []struct {
		mime, ctype, disp string
	}{
		{"image/png", "image/png", "inline"},
		{"IMAGE/JPEG; q=1", "image/jpeg", "inline"},
		{"text/html; charset=utf-8", "application/octet-stream", "attachment"},
		{"image/svg+xml", "application/octet-stream", "attachment"},
		{"", "application/octet-stream", "attachment"},
	}
	for i, c := range cases {
		data := []byte(fmt.Sprintf("blob %d", i))
		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])
		path := filepath.Join(e.dir, "data", "blobs", sha[:2], sha)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, data, 0o600))
		require.NoError(t, e.st.Q.InsertBlob(context.Background(), db.InsertBlobParams{Sha256: sha, Size: int64(len(data)), Mime: c.mime, CreatedAt: 1}))
		resp, err := http.Get(e.srv.URL + "/api/v1/blobs/" + sha)
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode, c.mime)
		assert.Equal(t, data, b)
		assert.Equal(t, c.ctype, resp.Header.Get("Content-Type"), c.mime)
		assert.Equal(t, c.disp, resp.Header.Get("Content-Disposition"), c.mime)
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
		assert.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))
	}
}
