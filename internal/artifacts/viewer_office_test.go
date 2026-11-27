package artifacts

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Lynricsy/Oryn/internal/artifacts/render"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// officeAssets 模拟嵌入的运行时、Office 查看器与它按需加载的第三方资源。
func officeAssets(name string) ([]byte, bool) {
	switch name {
	case runtimeAsset:
		return []byte(`window.__orynStart=function(c){};`), true
	case "sandbox/viewers/docx.js", "sandbox/viewers/pdf.js":
		return []byte(`console.log("office")`), true
	case "office/ooxml/docx.mjs", "office/ooxml/assets/render-worker-a.js", "office/ooxml/docx_parser_bg.wasm",
		"pdfjs/pdf.worker.min.mjs", "pdfjs/cmaps/UniGB-UCS2-H.bcmap":
		return []byte(name), true
	}
	return nil, false
}

func mkOffice(t *testing.T, e *env, typ string, data []byte) Artifact {
	t.Helper()
	a, err := e.s.Create(context.Background(), CreateParams{Title: "季度报告", Type: typ, Files: []FileChange{putBytes(TypeEntry(typ), data)}})
	require.NoError(t, err)
	return a
}

// Office 类型的查看器页：bundle 名取 TypeSpec.Viewer，配置带入口大小与标题；内嵌帧注入带 viewer 标记的运行时，
// 顶层帧独占窗口（standalone）。
func TestHostOfficeViewer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h := newHost(t, e, "localhost", officeAssets)
	mw := h.Middleware(http.NotFoundHandler())
	src := ooxmlZip(t, partWord, ctDocm, "word/document.xml")
	a := mkOffice(t, e, TypeDocm, src)

	token, _, err := h.OpenFrame(ctx, a.ID, 0, "", "zh-CN")
	require.NoError(t, err)
	host := "a-" + token + ".localhost:7600"
	body := do(mw, hostReq{host: host, path: "/"}).Body.String()
	assert.Contains(t, body, `<link rel="stylesheet" href="/_oryn/viewer/docx.css">`)
	assert.Contains(t, body, `<script type="module" src="/_oryn/viewer/docx.js"></script>`)
	cfg := viewerConfigOf(t, body)
	assert.Equal(t, "docm", cfg["type"])
	assert.Equal(t, "docx", cfg["viewer"])
	assert.Equal(t, "document.docm", cfg["entry"])
	assert.Equal(t, "季度报告", cfg["title"])
	assert.EqualValues(t, a.Size, cfg["size"])
	assert.Positive(t, a.Size)
	assert.NotContains(t, cfg, "standalone", "内嵌帧不是独占窗口")
	m := configRe.FindStringSubmatch(body)
	require.NotNil(t, m, "内嵌帧注入运行时")
	assert.Contains(t, m[1], `"path":"document.docm"`)
	assert.Contains(t, m[1], `"viewer":true`)

	// 顶层帧：没有运行时，查看器自己提供下载原件
	link, err := h.OpenLink(ctx, a.ID, 0, "zh-CN")
	require.NoError(t, err)
	top := strings.TrimSuffix(strings.TrimPrefix(link, "http://a-"), ".localhost:7600/")
	body = do(mw, hostReq{host: "a-" + top + ".localhost:7600", path: "/"}).Body.String()
	assert.NotContains(t, body, "__orynStart")
	assert.Equal(t, true, viewerConfigOf(t, body)["standalone"])

	// 下载原件：入口带 ?download=1 回 attachment（文件名 = 标题 + 扩展名），字节等于原件，支持 Range；
	// 不带参数时照常内联提供（查看器 fetch 它）
	plain := do(mw, hostReq{host: host, path: "/_oryn/v/1/document.docm"})
	require.Equal(t, http.StatusOK, plain.Code)
	assert.Empty(t, plain.Header().Get("Content-Disposition"))
	assert.Equal(t, src, plain.Body.Bytes())
	dl := do(mw, hostReq{host: host, path: "/document.docm?download=1"})
	require.Equal(t, http.StatusOK, dl.Code)
	assert.Equal(t, "attachment; filename*=UTF-8''%E5%AD%A3%E5%BA%A6%E6%8A%A5%E5%91%8A.docm", dl.Header().Get("Content-Disposition"))
	assert.Equal(t, "application/vnd.ms-word.document.macroEnabled.12", dl.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", dl.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, src, dl.Body.Bytes())
	part := do(mw, hostReq{host: host, path: "/_oryn/v/1/document.docm?download=1", header: map[string]string{"Range": "bytes=0-3"}})
	require.Equal(t, http.StatusPartialContent, part.Code)
	assert.Equal(t, src[:4], part.Body.Bytes())
	assert.NotEmpty(t, part.Header().Get("Content-Disposition"))

	// 文本类型与非入口文件不变成下载
	doc := mkDoc(t, e, "# x\n")
	dtok, _, err := h.OpenFrame(ctx, doc.ID, 0, "", "")
	require.NoError(t, err)
	rec := do(mw, hostReq{host: "a-" + dtok + ".localhost:7600", path: "/document.md?download=1"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Content-Disposition"))
}

// 查看器按需加载的第三方资源：ooxml/ 与 pdfjs/ 前缀映射到嵌入产物的 office/ooxml/ 与 pdfjs/，按扩展名给 Content-Type，
// 不透明源的请求回 ACAO null；不合法的路径与没有的文件 404。
func TestHostViewerVendorAssets(t *testing.T) {
	e := newEnv(t)
	h := newHost(t, e, "localhost", officeAssets)
	mw := h.Middleware(http.NotFoundHandler())
	a := mkOffice(t, e, TypePDF, pdfBytes())
	token, _, err := h.OpenFrame(context.Background(), a.ID, 0, "", "")
	require.NoError(t, err)
	host := "a-" + token + ".localhost:7600"
	null := map[string]string{"Origin": "null"}
	for p, ct := range map[string]string{
		"/_oryn/viewer/ooxml/docx.mjs":                  "text/javascript; charset=utf-8",
		"/_oryn/viewer/ooxml/assets/render-worker-a.js": "text/javascript; charset=utf-8",
		"/_oryn/viewer/ooxml/docx_parser_bg.wasm":       "application/wasm",
		"/_oryn/viewer/pdfjs/pdf.worker.min.mjs":        "text/javascript; charset=utf-8",
		"/_oryn/viewer/pdfjs/cmaps/UniGB-UCS2-H.bcmap":  "application/octet-stream",
	} {
		rec := do(mw, hostReq{host: host, path: p, header: null})
		require.Equal(t, http.StatusOK, rec.Code, p)
		assert.Equal(t, ct, rec.Header().Get("Content-Type"), p)
		assert.Equal(t, "null", rec.Header().Get("Access-Control-Allow-Origin"), p)
		assert.NotEmpty(t, rec.Header().Get("Content-Security-Policy"), p)
		assert.True(t, strings.HasSuffix(p, strings.TrimPrefix(rec.Body.String(), "office/")), p)
	}
	for _, p := range []string{
		"/_oryn/viewer/ooxml/missing.mjs",
		"/_oryn/viewer/ooxml/_oryn/x.js",
		"/_oryn/viewer/ooxml/a//b.js",
		"/_oryn/viewer/pdfjs/",
		"/_oryn/viewer/xlsx.js",
	} {
		assert.Equal(t, http.StatusNotFound, do(mw, hostReq{host: host, path: p}).Code, p)
	}
}

// 发布页上的 Office 查看器是独占窗口；渲染模式的查看器页不是。
func TestPublicOfficeViewer(t *testing.T) {
	pe := newPubEnv(t, "localhost")
	ctx := context.Background()
	src := xlsxBytes(t)
	a := mkOffice(t, pe.env, TypeXlsx, src)
	p, err := pe.s.Publish(ctx, a.ID, PublishParams{Access: AccessPublic})
	require.NoError(t, err)
	body := pubReq(pe, p.Slug, "GET", "/", nil).Body.String()
	cfg := viewerConfigOf(t, body)
	assert.Equal(t, "xlsx", cfg["viewer"])
	assert.Equal(t, true, cfg["standalone"])
	assert.Equal(t, "workbook.xlsx", cfg["entry"])
	assert.NotContains(t, body, "__orynStart")
	rec := pubReq(pe, p.Slug, "GET", "/workbook.xlsx", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, src, rec.Body.Bytes())
	assert.Empty(t, rec.Header().Get("Content-Disposition"))

	// 下载原件：同一条 attachment 变体
	rec = pubReq(pe, p.Slug, "GET", "/workbook.xlsx?download=1", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "attachment; filename*=UTF-8''%E5%AD%A3%E5%BA%A6%E6%8A%A5%E5%91%8A.xlsx", rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", rec.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, src, rec.Body.Bytes())
}

func TestRenderSiteOfficeViewer(t *testing.T) {
	e := newEnv(t)
	h := newHost(t, e, "localhost", officeAssets)
	a := mkOffice(t, e, TypePDF, pdfBytes())
	self := "http://" + render.Host + "/tok/"
	serve := func(p string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://"+render.Host+"/tok"+p+"?view=print", nil)
		rec := httptest.NewRecorder()
		h.ServeRender(rec, req, render.Target{ArtifactID: a.ID, Version: 1, Self: self}, p)
		return rec
	}
	body := serve("/").Body.String()
	assert.Contains(t, body, self+"_oryn/viewer/pdf.js")
	cfg := viewerConfigOf(t, body)
	assert.Equal(t, "pdf", cfg["viewer"])
	assert.EqualValues(t, len(pdfBytes()), cfg["size"])
	assert.NotContains(t, cfg, "standalone")
	assert.NotContains(t, body, "__orynStart")
	assert.Equal(t, "pdfjs/pdf.worker.min.mjs", serve("/_oryn/viewer/pdfjs/pdf.worker.min.mjs").Body.String())
}

// 超过 MaxPreviewBytes 的 Office 版本不出缩略图：接口直接回 no_thumbnail、不入队，ensureThumb 不渲染。
func TestThumbOfficeTooLarge(t *testing.T) {
	e := newEnv(t)
	r := &fakeRenderer{}
	withRenderer(e, r)
	ctx := context.Background()
	big := append(pdfBytes(), bytes.Repeat([]byte{' '}, MaxPreviewBytes)...)
	a := mkOffice(t, e, TypePDF, big)
	require.Greater(t, a.Size, int64(MaxPreviewBytes))

	_, _, err := e.s.Thumbnail(ctx, a.ID, 0)
	assert.Equal(t, CodeNoThumbnail, code(err))
	_, _, _, ok := e.s.nextThumb()
	assert.False(t, ok, "不能预览的版本不入队")
	require.NoError(t, e.s.ensureThumb(ctx, a.ID, 1))
	assert.Zero(t, r.runs.Load(), "不渲染")

	// 换成小文件的新版本照常出图
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{putBytes(EntryPDF, pdfBytes())}})
	require.NoError(t, err)
	require.NoError(t, e.s.ensureThumb(ctx, a.ID, 2))
	assert.EqualValues(t, 1, r.runs.Load())
	_, served, err := e.s.Thumbnail(ctx, a.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, served)
	_, _, err = e.s.Thumbnail(ctx, a.ID, 1)
	assert.Equal(t, CodeNoThumbnail, code(err), "旧的大版本不拿新版本的图顶替")
}
