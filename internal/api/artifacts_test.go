package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
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
	"github.com/Lynricsy/Oryn/internal/artifacts"
	"github.com/Lynricsy/Oryn/internal/artifacts/render"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// newArtifactsEnv 组装只带 artifact 服务的 REST（不需要线程服务：详情里的线程标题直接读库）。
func newArtifactsEnv(t *testing.T) (*threadsEnv, *artifacts.Service) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "data"))
	require.NoError(t, err)
	svc, err := artifacts.New(artifacts.Config{DataDir: filepath.Join(dir, "data"), PublicURL: "http://localhost:7600"}, st, nil)
	require.NoError(t, err)
	cfg := config.DefaultOrynd()
	cfg.DataDir = filepath.Join(dir, "data")
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st}}
	s.Artifacts = &api.ArtifactsDeps{Service: svc}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(func() {
		srv.Close()
		_ = st.Close()
	})
	return &threadsEnv{t: t, st: st, srv: srv, dir: dir}, svc
}

func artifactThread(t *testing.T, st *store.Store, title string) string {
	t.Helper()
	id := ids.Thread()
	require.NoError(t, st.Q.CreateThread(t.Context(), db.CreateThreadParams{
		Kind: "work", ID: id, Title: title, TitleSource: "user", State: "idle", SessionDir: "/s/" + id, CreatedAt: 1, UpdatedAt: 1,
	}))
	return id
}

func artFiles(kv ...string) []artifacts.FileChange {
	out := make([]artifacts.FileChange, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, artifacts.FileChange{Path: kv[i], Data: strings.NewReader(kv[i+1])})
	}
	return out
}

func decodeAs[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(body, &v), string(body))
	return v
}

// artifact REST：列表、详情（线程、画布、指定版本）、改名、软删除 / 恢复 / 永久删除、版本列表、恢复版本（含 409）。
func TestArtifactsREST(t *testing.T) {
	e, svc := newArtifactsEnv(t)
	ctx := t.Context()
	creator := artifactThread(t, e.st, "做报告")
	writer := artifactThread(t, e.st, "改样式")

	a, err := svc.Create(ctx, artifacts.CreateParams{
		Title: "周报", ThreadID: creator, Author: artifacts.Author{ThreadID: creator, Label: "做报告"},
		Files: artFiles("index.html", "<h1>v1</h1>", "a.css", "h1{}"),
	})
	require.NoError(t, err)
	_, err = svc.Write(ctx, artifacts.WriteParams{ID: a.ID, BaseVersion: 1, Author: artifacts.Author{ThreadID: writer, Label: "改样式"}, Files: artFiles("a.css", "h1{color:red}")})
	require.NoError(t, err)
	canvas, err := svc.Create(ctx, artifacts.CreateParams{Title: "画布", Origin: artifacts.OriginImport, Files: artFiles(
		"canvas.json", `{"format":1,"canvases":[{"id":"c1","name":"页 1"}],"pages":[],"notes":[]}`)})
	require.NoError(t, err)

	// 列表：最近更新的在前；按线程（创建或写过版本）过滤
	code, body := e.do("GET", "/api/v1/artifacts", nil)
	require.Equal(t, 200, code, string(body))
	list := decodeAs[apigen.ArtifactList](t, body)
	require.Len(t, list.Artifacts, 2)
	code, body = e.do("GET", "/api/v1/artifacts?threadId="+writer, nil)
	require.Equal(t, 200, code, string(body))
	list = decodeAs[apigen.ArtifactList](t, body)
	require.Len(t, list.Artifacts, 1)
	assert.Equal(t, a.ID, list.Artifacts[0].ID)
	assert.Equal(t, 2, list.Artifacts[0].Version)

	// 详情：最新版本；线程先创建者后写入者；版本 1 的文件清单
	code, body = e.do("GET", "/api/v1/artifacts/"+a.ID, nil)
	require.Equal(t, 200, code, string(body))
	d := decodeAs[apigen.ArtifactDetail](t, body)
	assert.Equal(t, 2, d.Version)
	assert.Equal(t, 2, d.Head)
	assert.Equal(t, []apigen.ArtifactThreadRef{{ThreadID: creator, Title: "做报告"}, {ThreadID: writer, Title: "改样式"}}, d.Threads)
	assert.NotNil(t, d.Capabilities)
	assert.Nil(t, d.Canvas)
	require.Len(t, d.Files, 2)
	assert.Equal(t, "a.css", d.Files[0].Path)
	assert.Equal(t, int64(len("h1{color:red}")), d.Files[0].Size)
	code, body = e.do("GET", "/api/v1/artifacts/"+a.ID+"?version=1", nil)
	require.Equal(t, 200, code, string(body))
	d = decodeAs[apigen.ArtifactDetail](t, body)
	assert.Equal(t, 1, d.Version)
	assert.Equal(t, int64(len("h1{}")), d.Files[0].Size)
	code, _ = e.do("GET", "/api/v1/artifacts/"+a.ID+"?version=9", nil)
	assert.Equal(t, 404, code)
	code, _ = e.do("GET", "/api/v1/artifacts/"+ids.Artifact(), nil)
	assert.Equal(t, 404, code)

	// 画布：canvas.json 解析后放进 canvas；导入的没有线程
	code, body = e.do("GET", "/api/v1/artifacts/"+canvas.ID, nil)
	require.Equal(t, 200, code, string(body))
	d = decodeAs[apigen.ArtifactDetail](t, body)
	assert.Equal(t, apigen.ArtifactKindCanvas, d.Artifact.Kind)
	require.NotNil(t, d.Canvas)
	assert.EqualValues(t, 1, d.Canvas["format"])
	assert.Empty(t, d.Threads)

	// 改名不产生版本
	code, body = e.do("PATCH", "/api/v1/artifacts/"+a.ID, map[string]any{"title": "  月报  "})
	require.Equal(t, 200, code, string(body))
	got := decodeAs[apigen.Artifact](t, body)
	assert.Equal(t, "月报", got.Title)
	assert.Equal(t, 2, got.Version)
	code, body = e.do("PATCH", "/api/v1/artifacts/"+a.ID, map[string]any{"title": " "})
	assert.Equal(t, 400, code)
	assert.Equal(t, "invalid", decodeAs[apigen.Error](t, body).Code)

	// 版本列表：新的在前，作者
	code, body = e.do("GET", "/api/v1/artifacts/"+a.ID+"/versions", nil)
	require.Equal(t, 200, code, string(body))
	vs := decodeAs[apigen.ArtifactVersionList](t, body).Versions
	require.Len(t, vs, 2)
	assert.Equal(t, 2, vs[0].Version)
	require.NotNil(t, vs[0].AuthorThreadID)
	assert.Equal(t, writer, *vs[0].AuthorThreadID)
	assert.Equal(t, "改样式", vs[0].AuthorLabel)

	// 恢复版本 1：作者是用户，head 变 3
	code, body = e.do("POST", "/api/v1/artifacts/"+a.ID+"/versions/1/restore", map[string]any{"baseVersion": 2})
	require.Equal(t, 200, code, string(body))
	assert.Equal(t, 3, decodeAs[apigen.Artifact](t, body).Version)
	v3, err := svc.Version(ctx, a.ID, 3)
	require.NoError(t, err)
	assert.Empty(t, v3.AuthorThreadID)
	// 用过期的 baseVersion 恢复：a.css 在版本 2 之后被改过 → 409 带 state
	code, body = e.do("POST", "/api/v1/artifacts/"+a.ID+"/versions/2/restore", map[string]any{"baseVersion": 2})
	require.Equal(t, 409, code, string(body))
	conflict := decodeAs[apigen.ArtifactConflict](t, body)
	assert.Equal(t, "conflict", conflict.Code)
	assert.Equal(t, 3, conflict.State.Version)
	assert.False(t, conflict.State.Deleted)
	assert.Equal(t, []string{"a.css"}, conflict.State.Changed)

	// 标记打开
	code, _ = e.do("POST", "/api/v1/artifacts/"+a.ID+"/opened", nil)
	assert.Equal(t, 204, code)
	got2, err := svc.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, got2.OpenedAt.IsZero())

	// 不在回收站里时不能永久删除
	code, _ = e.do("DELETE", "/api/v1/artifacts/"+a.ID+"?permanent=true", nil)
	assert.Equal(t, 400, code)
	// 软删除 → 回收站可见；回收站里不能改名（410）
	code, _ = e.do("DELETE", "/api/v1/artifacts/"+a.ID, nil)
	require.Equal(t, 204, code)
	code, body = e.do("GET", "/api/v1/artifacts?deleted=true", nil)
	require.Equal(t, 200, code)
	list = decodeAs[apigen.ArtifactList](t, body)
	require.Len(t, list.Artifacts, 1)
	assert.NotNil(t, list.Artifacts[0].DeletedAt)
	code, body = e.do("GET", "/api/v1/artifacts", nil)
	require.Equal(t, 200, code)
	assert.Len(t, decodeAs[apigen.ArtifactList](t, body).Artifacts, 1)
	code, body = e.do("PATCH", "/api/v1/artifacts/"+a.ID, map[string]any{"title": "x"})
	assert.Equal(t, 410, code, string(body))
	// 恢复
	code, body = e.do("POST", "/api/v1/artifacts/"+a.ID+"/restore", nil)
	require.Equal(t, 200, code, string(body))
	assert.Nil(t, decodeAs[apigen.Artifact](t, body).DeletedAt)
	// 再删并永久删除
	code, _ = e.do("DELETE", "/api/v1/artifacts/"+a.ID, nil)
	require.Equal(t, 204, code)
	code, _ = e.do("DELETE", "/api/v1/artifacts/"+a.ID+"?permanent=true", nil)
	require.Equal(t, 204, code)
	code, _ = e.do("GET", "/api/v1/artifacts/"+a.ID, nil)
	assert.Equal(t, 404, code)
}

// artifact 文件下载：HTML 一律 attachment + sandbox CSP；栅格图 inline；支持 Range 与指定版本。
func TestArtifactFileREST(t *testing.T) {
	e, svc := newArtifactsEnv(t)
	ctx := t.Context()
	png := "\x89PNG\r\n\x1a\nfake"
	a, err := svc.Create(ctx, artifacts.CreateParams{Title: "页", Files: artFiles("index.html", "<h1>hello</h1>", "img/a.png", png)})
	require.NoError(t, err)
	_, err = svc.Write(ctx, artifacts.WriteParams{ID: a.ID, BaseVersion: 1, Files: artFiles("index.html", "<h1>v2</h1>")})
	require.NoError(t, err)

	get := func(path string, hdr map[string]string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest("GET", e.srv.URL+path, nil)
		require.NoError(t, err)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	resp, body := get("/api/v1/artifacts/"+a.ID+"/file?path=index.html", nil)
	require.Equal(t, 200, resp.StatusCode, body)
	assert.Equal(t, "<h1>v2</h1>", body)
	assert.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "attachment")

	resp, body = get("/api/v1/artifacts/"+a.ID+"/file?path=index.html&version=1", map[string]string{"Range": "bytes=4-8"})
	require.Equal(t, 206, resp.StatusCode, body)
	assert.Equal(t, "hello", body)
	assert.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))

	resp, body = get("/api/v1/artifacts/"+a.ID+"/file?path=img/a.png", nil)
	require.Equal(t, 200, resp.StatusCode, body)
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	assert.Equal(t, "inline", resp.Header.Get("Content-Disposition"))
	assert.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))

	resp, _ = get("/api/v1/artifacts/"+a.ID+"/file?path=missing.html", nil)
	assert.Equal(t, 404, resp.StatusCode)
	resp, _ = get("/api/v1/artifacts/"+a.ID+"/file?path=index.html&version=7", nil)
	assert.Equal(t, 404, resp.StatusCode)
}

// fakePNGRenderer 回一张按窗口尺寸的白色 PNG。
type fakePNGRenderer struct{}

func (fakePNGRenderer) Status() (bool, string) { return true, "" }
func (fakePNGRenderer) Run(_ context.Context, j render.Job) ([]byte, error) {
	var buf bytes.Buffer
	err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, j.Width, j.Height)))
	return buf.Bytes(), err
}

// 缩略图（命中、回落、未命中）与 render.png（不可用 503、尺寸与路径校验、下载头）。
func TestArtifactThumbnailRenderREST(t *testing.T) {
	e, svc := newArtifactsEnv(t)
	ctx := t.Context()
	a, err := svc.Create(ctx, artifacts.CreateParams{Title: "周/报", Files: artFiles("index.html", "<h1>v1</h1>")})
	require.NoError(t, err)

	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(e.srv.URL + path)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	base := "/api/v1/artifacts/" + a.ID
	resp, body := get(base + "/thumbnail")
	require.Equal(t, 404, resp.StatusCode, body)
	assert.Equal(t, artifacts.CodeNoThumbnail, decodeAs[apigen.Error](t, []byte(body)).Code)

	v1, err := svc.Version(ctx, a.ID, 1)
	require.NoError(t, err)
	dir := filepath.Join(e.dir, "data", "artifacts", "thumbs")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, v1.FilesKey+".jpg"), []byte("jpeg-v1"), 0o600))
	_, err = svc.Write(ctx, artifacts.WriteParams{ID: a.ID, BaseVersion: 1, Files: artFiles("index.html", "<h1>v2</h1>")})
	require.NoError(t, err)

	resp, body = get(base + "/thumbnail?version=1")
	require.Equal(t, 200, resp.StatusCode, body)
	assert.Equal(t, "jpeg-v1", body)
	assert.Equal(t, "image/jpeg", resp.Header.Get("Content-Type"))
	assert.Equal(t, "private, max-age=31536000, immutable", resp.Header.Get("Cache-Control"))
	for _, q := range []string{"", "?version=2"} {
		resp, body = get(base + "/thumbnail" + q)
		require.Equal(t, 200, resp.StatusCode, body)
		assert.Equal(t, "jpeg-v1", body, "v2 没有图时回 v1 的")
		assert.Equal(t, "private, no-cache", resp.Header.Get("Cache-Control"))
	}
	resp, _ = get(base + "/thumbnail?version=9")
	assert.Equal(t, 404, resp.StatusCode)

	resp, body = get(base + "/render.png")
	require.Equal(t, 503, resp.StatusCode, body)
	assert.Equal(t, artifacts.CodeRenderUnavailable, decodeAs[apigen.Error](t, []byte(body)).Code)

	svc.UseRenderer(ctx, fakePNGRenderer{})
	resp, body = get(base + "/render.png?width=320&height=200")
	require.Equal(t, 200, resp.StatusCode, body)
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "attachment")
	assert.Contains(t, resp.Header.Get("Content-Disposition"), ".png")
	img, err := png.Decode(strings.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, 320, 200), img.Bounds())
	resp, _ = get(base + "/render.png?width=63")
	assert.Equal(t, 400, resp.StatusCode)
	resp, _ = get(base + "/render.png?height=8001")
	assert.Equal(t, 400, resp.StatusCode)
	resp, _ = get(base + "/render.png?path=missing.html")
	assert.Equal(t, 400, resp.StatusCode)
	resp, _ = get(base + "/render.png?version=1&path=index.html")
	assert.Equal(t, 200, resp.StatusCode)
}
