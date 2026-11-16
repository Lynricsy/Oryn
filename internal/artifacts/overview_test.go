package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/artifacts/render"
)

const overviewDoc = `{"format":1,"canvases":[{"id":"c1","name":"首页"},{"id":"c2","name":"</script><b>"}],` +
	`"pages":[{"id":"a","canvas":"c1","name":"A","file":"a.html"},{"id":"b","canvas":"c2","name":"B 页","file":"dir/b #1.html"}],` +
	`"notes":[{"id":"n1","canvas":"c1","kind":"sticky","color":"yellow","x":0,"y":0,"text":"</script><script>alert(1)</script>"}]}`

// mkCanvas 建一个三画框以内的画布 artifact；extra 是额外的文件。
func mkCanvas(t *testing.T, e *env, extra ...FileChange) Artifact {
	t.Helper()
	files := append([]FileChange{
		put("canvas.json", overviewDoc), put("a.html", "<p>a</p>"), put("dir/b #1.html", "<p>b</p>"),
	}, extra...)
	a, err := e.s.Create(context.Background(), CreateParams{Title: "设计稿 </title>", Files: files})
	require.NoError(t, err)
	require.Equal(t, KindCanvas, a.Kind)
	return a
}

// overviewJSON 取出概览页里 #oryn-canvas 的数据。
func overviewJSON(t *testing.T, page string) overviewData {
	t.Helper()
	const open = `<script type="application/json" id="oryn-canvas">`
	_, rest, ok := strings.Cut(page, open)
	require.True(t, ok, "没有 id=\"oryn-canvas\"")
	raw, _, ok := strings.Cut(rest, "</script>")
	require.True(t, ok)
	var d overviewData
	require.NoError(t, json.Unmarshal([]byte(raw), &d))
	return d
}

func TestCanvasOverviewPage(t *testing.T) {
	doc, err := canvas.Parse([]byte(overviewDoc))
	require.NoError(t, err)

	page := string(canvasOverview(doc, "设计稿 </title>", ""))
	assert.True(t, strings.HasPrefix(page, overviewMarker+"\n<!doctype html>"), page[:80])
	assert.True(t, isOverviewPage([]byte(page)))
	assert.True(t, isOverviewPage([]byte("\xef\xbb\xbf"+page)))
	assert.False(t, isOverviewPage([]byte("<!doctype html>"+overviewMarker)))
	assert.Contains(t, page, "<title>设计稿 &lt;/title&gt;</title>")
	assert.NotContains(t, page, "<html lang=")
	// 页面数据里的 </script> 被转义，不会提前结束数据块
	assert.NotContains(t, page, "</script><script>alert(1)")
	assert.NotContains(t, page, "</script><b>")

	d := overviewJSON(t, page)
	assert.Equal(t, "设计稿 </title>", d.Title)
	assert.Equal(t, []string{"en", "zh"}, slices.Sorted(maps.Keys(d.Text)))
	got, err := canvas.Parse(d.Doc)
	require.NoError(t, err)
	assert.Equal(t, doc, got)
	// 没有脚本时的回落：按画布页分组的画框链接，地址逐段编码
	assert.Contains(t, page, `<a href="dir/b%20%231.html">B 页</a>`)

	zh := string(canvasOverview(doc, "x", "zh-CN"))
	assert.Contains(t, zh, `<html lang="zh-CN">`)
	assert.Equal(t, []string{"zh"}, slices.Sorted(maps.Keys(overviewJSON(t, zh).Text)))
	en := overviewJSON(t, string(canvasOverview(doc, "x", "en")))
	assert.Equal(t, "Fit all", en.Text["en"].Fit)
}

// 导出画布的 zip 补一个生成的 index.html；把 zip 原样导入回来，版本里没有它。自己有 index.html 的画布不补。
func TestOverviewExportImport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := mkCanvas(t, e)

	x, err := e.s.Export(ctx, a.ID, 0, ExportZip)
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, x.Write(&out))
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	require.NoError(t, err)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
		rc, err := f.Open()
		require.NoError(t, err)
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		if f.Name == EntryHTML {
			assert.True(t, isOverviewPage(b))
			assert.Equal(t, "设计稿 </title>", overviewJSON(t, string(b)).Title)
		}
		w, err := mw.CreateFormFile(f.Name, f.Name)
		require.NoError(t, err)
		_, _ = w.Write(b)
	}
	require.NoError(t, mw.Close())
	assert.ElementsMatch(t, []string{"canvas.json", "a.html", "dir/b #1.html", EntryHTML}, names)

	back, err := e.s.Import(ctx, multipart.NewReader(&buf, mw.Boundary()), ImportParams{Name: "设计稿"})
	require.NoError(t, err)
	assert.Equal(t, KindCanvas, back.Kind)
	files, err := e.s.Files(ctx, back.ID, 0)
	require.NoError(t, err)
	paths := []string{}
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	assert.ElementsMatch(t, []string{"canvas.json", "a.html", "dir/b #1.html"}, paths)

	// 自己有 index.html 的画布：导出原样，不补
	own := mkCanvas(t, e, put(EntryHTML, page))
	x, err = e.s.Export(ctx, own.ID, 0, ExportZip)
	require.NoError(t, err)
	out.Reset()
	require.NoError(t, x.Write(&out))
	zr, err = zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	require.NoError(t, err)
	count := 0
	for _, f := range zr.File {
		if f.Name == EntryHTML {
			count++
			rc, err := f.Open()
			require.NoError(t, err)
			b, _ := io.ReadAll(rc)
			_ = rc.Close()
			assert.Equal(t, page, string(b))
		}
	}
	assert.Equal(t, 1, count)

	// 只有生成的概览页、没有 canvas.json 的导入照常报缺入口
	_, err = e.s.Import(ctx, multipartOf(t, EntryHTML, overviewMarker+"\n<p>x</p>"), ImportParams{})
	assert.Equal(t, CodeNoIndex, code(err))
}

// 「在浏览器中打开」的顶层帧与渲染模式在画布的根上提供概览页；普通查看帧不提供。顶层帧的根之外不发 frame-ancestors，
// 概览页才能内嵌画框（根运行在不透明源里）。
func TestOverviewServedAtRoot(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h := newHost(t, e, "localhost", fakeRuntime)
	mw := h.Middleware(http.NotFoundHandler())
	a := mkCanvas(t, e)

	link, err := h.OpenLink(ctx, a.ID, 0, "zh-CN")
	require.NoError(t, err)
	host := strings.TrimSuffix(strings.TrimPrefix(link, "http://"), "/")
	rec := do(mw, hostReq{host: host, path: "/"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, isOverviewPage(rec.Body.Bytes()))
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `<html lang="zh-CN">`)
	csp := rec.Header().Get("Content-Security-Policy")
	assert.True(t, strings.HasPrefix(csp, sandboxTopLevel+";"), csp)
	assert.True(t, strings.HasSuffix(csp, "frame-ancestors 'none'"), csp)
	// 画框按相对路径落在同一版本：/_oryn/v/1/dir/b%20%231.html
	rec = do(mw, hostReq{host: host, path: "/" + ReservedPrefix + "/v/1/dir/b%20%231.html"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "<p>b</p>", rec.Body.String())
	assert.NotContains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors")

	// 普通查看帧的根不是概览页（那里是编辑器）
	token, _, err := h.OpenFrame(ctx, a.ID, 0, "", "")
	require.NoError(t, err)
	rec = do(mw, hostReq{host: "a-" + token + ".localhost:7600", path: "/"})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors http://localhost:7600")

	// 渲染模式（缩略图）
	serve := func(id, p string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://"+render.Host+"/tok"+p+"?view=thumbnail", nil)
		rec := httptest.NewRecorder()
		h.ServeRender(rec, req, render.Target{ArtifactID: id, Version: 1, Self: "http://" + render.Host + "/tok/"}, p)
		return rec
	}
	rec = serve(a.ID, "/")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, isOverviewPage(rec.Body.Bytes()))
	assert.NotContains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors")
	assert.Equal(t, "<p>a</p>", serve(a.ID, "/a.html").Body.String())

	// 自己有 index.html 的画布与普通页面：根就是 index.html
	own := mkCanvas(t, e, put(EntryHTML, page))
	assert.Equal(t, page, serve(own.ID, "/").Body.String())
	web := mkPage(t, e)
	assert.Equal(t, page, serve(web.ID, "/").Body.String())
}
