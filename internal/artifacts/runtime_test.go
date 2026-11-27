package artifacts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeViewerAssets 模拟嵌入的运行时与查看器构建产物。
func fakeViewerAssets(name string) ([]byte, bool) {
	switch name {
	case runtimeAsset:
		return []byte(`window.__orynStart=function(c){};`), true
	case "sandbox/viewers/document.js":
		return []byte(`console.log("viewer")`), true
	}
	return nil, false
}

var viewerCfgRe = `<script type="application/json" id="oryn-viewer">`

// viewerConfigOf 解出查看器页里的配置。
func viewerConfigOf(t *testing.T, body string) map[string]any {
	t.Helper()
	_, rest, ok := strings.Cut(body, viewerCfgRe)
	require.True(t, ok, body)
	raw, _, ok := strings.Cut(rest, "</script>")
	require.True(t, ok)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &m))
	return m
}

func mkDoc(t *testing.T, e *env, md string) Artifact {
	t.Helper()
	a, err := e.s.Create(context.Background(), CreateParams{Title: "文档", Type: TypeDocument, Files: []FileChange{put("document.md", md), put("images/a.png", pngBytes)}})
	require.NoError(t, err)
	return a
}

// 类型化 artifact 的帧根是查看器页：跟随帧先钉到 /_oryn/v/<n>/，入口与图片按相对路径读同一版本。
func TestHostViewerPage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h := newHost(t, e, "localhost", fakeViewerAssets)
	mw := h.Middleware(http.NotFoundHandler())
	a := mkDoc(t, e, "# 第一版\n")

	token, _, err := h.OpenFrame(ctx, a.ID, 0, "", "zh-CN")
	require.NoError(t, err)
	host := "a-" + token + ".localhost:7600"
	nav := doRaw(mw, hostReq{host: host, path: "/?view=thumbnail"})
	require.Equal(t, http.StatusFound, nav.Code)
	assert.Equal(t, "/_oryn/v/1/?view=thumbnail", nav.Header().Get("Location"))
	rec := doRaw(mw, hostReq{host: host, path: "/_oryn/v/1/?view=thumbnail"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	body := rec.Body.String()
	assert.Contains(t, body, `id="oryn-viewer"`)
	assert.Contains(t, body, `<html lang="zh-CN">`)
	assert.Contains(t, body, `<link rel="stylesheet" href="/_oryn/viewer/document.css">`)
	assert.Contains(t, body, `<script type="module" src="/_oryn/viewer/document.js"></script>`)
	assert.Contains(t, body, `<div id="root"></div>`)
	assert.Equal(t, map[string]any{
		"type": "document", "viewer": "document", "format": float64(1), "version": float64(1), "head": float64(1),
		"entry": "document.md", "size": float64(0), "title": "文档", "locale": "zh-CN", "root": "/",
	}, viewerConfigOf(t, body))
	m := configRe.FindStringSubmatch(body)
	require.NotNil(t, m, "查看器页注入运行时")
	assert.Contains(t, m[1], `"root":"/","runtime":true,"viewer":true`)
	assert.Less(t, strings.Index(body, "__orynStart"), strings.Index(body, `type="module"`))

	// 入口文件与图片照常从同一版本取
	assert.Equal(t, "# 第一版\n", do(mw, hostReq{host: host, path: "/_oryn/v/1/document.md"}).Body.String())
	assert.Equal(t, pngBytes, do(mw, hostReq{host: host, path: "/_oryn/v/1/images/a.png"}).Body.String())

	// 查看器资源：只认四个名字；没构建的 404
	js := do(mw, hostReq{host: host, path: "/_oryn/viewer/document.js"})
	require.Equal(t, http.StatusOK, js.Code)
	assert.Equal(t, "text/javascript; charset=utf-8", js.Header().Get("Content-Type"))
	assert.Equal(t, `console.log("viewer")`, js.Body.String())
	assert.NotEmpty(t, js.Header().Get("Content-Security-Policy"))
	assert.Equal(t, http.StatusNotFound, do(mw, hostReq{host: host, path: "/_oryn/viewer/document.css"}).Code)
	assert.Equal(t, http.StatusNotFound, do(mw, hostReq{host: host, path: "/_oryn/viewer/evil.js"}).Code)

	// 新版本之后：钉住 v1 的帧仍是 v1 的查看器、head 是 2、不带运行时
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("document.md", "# 第二版\n")}})
	require.NoError(t, err)
	pinned, _, err := h.OpenFrame(ctx, a.ID, 1, "", "bad locale!")
	require.NoError(t, err)
	body = do(mw, hostReq{host: "a-" + pinned + ".localhost:7600", path: "/"}).Body.String()
	cfg := viewerConfigOf(t, body)
	assert.EqualValues(t, 1, cfg["version"])
	assert.EqualValues(t, 2, cfg["head"])
	assert.NotContains(t, cfg, "locale", "不合法的 locale 被忽略")
	assert.Contains(t, body, `"runtime":false`)
	assert.Contains(t, body, "<html>")

	// 顶层帧：查看器页不注入运行时
	link, err := h.OpenLink(ctx, a.ID, 0, "en")
	require.NoError(t, err)
	top := strings.TrimSuffix(strings.TrimPrefix(link, "http://a-"), ".localhost:7600/")
	body = do(mw, hostReq{host: "a-" + top + ".localhost:7600", path: "/"}).Body.String()
	assert.Contains(t, body, `id="oryn-viewer"`)
	assert.NotContains(t, body, "__orynStart")
}

// 主域回退：查看器资源与运行时端点都在 /_af/<令牌>/ 之下。
func TestHostViewerFallback(t *testing.T) {
	e := newEnv(t)
	h := newHost(t, e, "", fakeViewerAssets)
	a := mkDoc(t, e, "# x\n")
	token, _, err := h.OpenFrame(context.Background(), a.ID, 0, "", "")
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle(FramePathPrefix, h.Handler())
	root := "/_af/" + token + "/"
	body := do(mux, hostReq{host: "localhost:7600", path: root}).Body.String()
	assert.Contains(t, body, `src="`+root+`_oryn/viewer/document.js"`)
	assert.EqualValues(t, root, viewerConfigOf(t, body)["root"])
	assert.Equal(t, http.StatusOK, do(mux, hostReq{host: "localhost:7600", path: root + "_oryn/viewer/document.js"}).Code)

	rec := postRuntime(mux, "localhost:7600", root+"_oryn/v1", "null", `{"v":1,"id":"1","kind":"use","cap":"content"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]any{"v": float64(1), "id": "1", "ok": true, "value": map[string]any{"ops": []any{"read", "write"}}}, replyOf(t, rec))
	// 回退模式下帧自己的源就是 public_url 的源
	assert.Equal(t, http.StatusOK, postRuntime(mux, "localhost:7600", root+"_oryn/v1", "http://localhost:7600", `{"v":1,"id":"1","kind":"use","cap":"content"}`).Code)
}

func postRuntime(h http.Handler, host, path, origin, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func replyOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

// 运行时端点的 content 能力：read / write，冲突与校验错误，以及各种拒绝。
func TestRuntimeContent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h := newHost(t, e, "localhost", fakeViewerAssets)
	mw := h.Middleware(http.NotFoundHandler())
	a := mkDoc(t, e, "# 第一版\n")
	token, _, err := h.OpenFrame(ctx, a.ID, 0, "", "")
	require.NoError(t, err)
	host := "a-" + token + ".localhost:7600"
	call := func(body string) map[string]any {
		t.Helper()
		rec := postRuntime(mw, host, "/_oryn/v1", "null", body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "null", rec.Header().Get("Access-Control-Allow-Origin"))
		return replyOf(t, rec)
	}
	errCode := func(r map[string]any) string {
		t.Helper()
		require.Equal(t, false, r["ok"], r)
		return r["error"].(map[string]any)["code"].(string)
	}

	assert.Equal(t, map[string]any{"ops": []any{"read", "write"}}, call(`{"v":1,"id":"u1","kind":"use","cap":"content"}`)["value"])
	r := call(`{"v":1,"id":"r1","kind":"call","cap":"content","op":"read"}`)
	assert.Equal(t, "r1", r["id"])
	assert.Equal(t, map[string]any{
		"type": "document", "format": float64(1), "version": float64(1), "head": float64(1), "entry": "document.md", "text": "# 第一版\n",
	}, r["value"])

	w := call(`{"v":1,"id":"w1","kind":"call","cap":"content","op":"write","args":[{"baseVersion":1,"text":"# 第二版\n"}]}`)
	assert.Equal(t, map[string]any{"version": float64(2)}, w["value"], w)
	vs, err := e.s.Versions(ctx, a.ID)
	require.NoError(t, err)
	assert.Empty(t, vs[0].AuthorThreadID, "页面写入的版本作者是用户")
	assert.Equal(t, "# 第二版\n", readFile(t, e.s, a.ID, 0, "document.md"))

	// 过期的 baseVersion：入口文件在它之后被改过 → conflict，不落版本
	assert.Equal(t, CodeConflict, errCode(call(`{"v":1,"id":"w2","kind":"call","cap":"content","op":"write","args":[{"baseVersion":1,"text":"# 冲突\n"}]}`)))
	// 读旧版本
	old := call(`{"v":1,"id":"r2","kind":"call","cap":"content","op":"read","args":[{"version":1}]}`)["value"].(map[string]any)
	assert.Equal(t, "# 第一版\n", old["text"])
	assert.EqualValues(t, 2, old["head"])
	// 内容校验失败 → invalid，带问题列表
	inv := call(`{"v":1,"id":"w3","kind":"call","cap":"content","op":"write","args":[{"baseVersion":2,"text":"![x](images/none.png)"}]}`)
	assert.Equal(t, CodeInvalid, errCode(inv))
	assert.Contains(t, inv["error"].(map[string]any)["message"], "document.md:1")
	assert.Equal(t, CodeInvalid, errCode(call(`{"v":1,"id":"w4","kind":"call","cap":"content","op":"write","args":[{"text":"x"}]}`)))
	assert.Equal(t, CodeInvalid, errCode(call(`{"v":1,"id":"w5","kind":"call","cap":"content","op":"write","args":[{"baseVersion":2,"text":"x","extra":1}]}`)))
	assert.Equal(t, CodeInvalid, errCode(call(`{"v":1,"id":"w6","kind":"call","cap":"content","op":"write","args":[{},{}]}`)))
	assert.Equal(t, CodeTooLarge, errCode(call(`{"v":1,"id":"w7","kind":"call","cap":"content","op":"write","args":[{"baseVersion":2,"text":"`+strings.Repeat("x", maxContentText+1)+`"}]}`)))
	assert.Equal(t, codeUnsupported, errCode(call(`{"v":1,"id":"x","kind":"call","cap":"content","op":"delete"}`)))
	assert.Equal(t, codeUnsupported, errCode(call(`{"v":1,"id":"x","kind":"call","cap":"nope","op":"get"}`)))
	assert.Equal(t, codeNotDeclared, errCode(call(`{"v":1,"id":"x","kind":"call","cap":"db","op":"get"}`)))
	assert.Nil(t, call(`{"v":1,"id":"x","kind":"use","cap":"db"}`)["value"])
	cur, err := e.s.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, cur.Version, "失败的写入不落版本")

	// 消息形状不对 → 400
	for _, bad := range []string{`not json`, `{"v":2,"id":"1","kind":"use","cap":"content"}`, `{"v":1,"id":"","kind":"use","cap":"content"}`,
		`{"v":1,"id":"1","kind":"stream","cap":"content"}`, `{"v":1,"id":"1","kind":"call","cap":"content"}`, `{"v":1,"id":"1","kind":"use","cap":"content","x":1}`} {
		rec := postRuntime(mw, host, "/_oryn/v1", "null", bad)
		assert.Equal(t, http.StatusBadRequest, rec.Code, bad)
		assert.Equal(t, CodeInvalid, errCode(replyOf(t, rec)), bad)
	}
	// 别的源 → 403；帧自己的源可以；预检；GET 不行；太大 413
	assert.Equal(t, http.StatusForbidden, postRuntime(mw, host, "/_oryn/v1", "http://localhost:7600", `{}`).Code)
	assert.Equal(t, http.StatusOK, postRuntime(mw, host, "/_oryn/v1", "http://"+host, `{"v":1,"id":"1","kind":"use","cap":"content"}`).Code)
	pre := doRaw(mw, hostReq{method: http.MethodOptions, host: host, path: "/_oryn/v1", header: map[string]string{"Origin": "null"}})
	assert.Equal(t, http.StatusNoContent, pre.Code)
	assert.Equal(t, "null", pre.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "POST", pre.Header().Get("Access-Control-Allow-Methods"))
	assert.Equal(t, http.StatusMethodNotAllowed, doRaw(mw, hostReq{host: host, path: "/_oryn/v1"}).Code)
	assert.Equal(t, http.StatusRequestEntityTooLarge, postRuntime(mw, host, "/_oryn/v1", "null", strings.Repeat(" ", maxRuntimeMessage+1)).Code)

	// 钉住旧版本的帧：use 回 null，call 回 not_allowed
	pinned, _, err := h.OpenFrame(ctx, a.ID, 1, "", "")
	require.NoError(t, err)
	ph := "a-" + pinned + ".localhost:7600"
	assert.Nil(t, replyOf(t, postRuntime(mw, ph, "/_oryn/v1", "null", `{"v":1,"id":"1","kind":"use","cap":"content"}`))["value"])
	assert.Equal(t, codeNotAllowed, errCode(replyOf(t, postRuntime(mw, ph, "/_oryn/v1", "null", `{"v":1,"id":"1","kind":"call","cap":"content","op":"read"}`))))

	// 普通页面没有 content 能力
	page := mkPage(t, e)
	pt, _, err := h.OpenFrame(ctx, page.ID, 0, "", "")
	require.NoError(t, err)
	pgh := "a-" + pt + ".localhost:7600"
	assert.Nil(t, replyOf(t, postRuntime(mw, pgh, "/_oryn/v1", "null", `{"v":1,"id":"1","kind":"use","cap":"content"}`))["value"])
	assert.Equal(t, codeUnsupported, errCode(replyOf(t, postRuntime(mw, pgh, "/_oryn/v1", "null", `{"v":1,"id":"1","kind":"call","cap":"content","op":"read"}`))))

	// 回收站里：use 回 null，call 回 not_found
	_, err = e.s.Delete(ctx, a.ID)
	require.NoError(t, err)
	assert.Nil(t, call(`{"v":1,"id":"1","kind":"use","cap":"content"}`)["value"])
	assert.Equal(t, CodeNotFound, errCode(call(`{"v":1,"id":"1","kind":"call","cap":"content","op":"read"}`)))

	// 未知令牌
	assert.Equal(t, http.StatusNotFound, postRuntime(mw, "a-"+strings.Repeat("a", 26)+".localhost:7600", "/_oryn/v1", "null", `{}`).Code)
}

// 幻灯片的 content.write 走同一套校验：问题位置写进 message。
func TestRuntimeSlidesWriteValidates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h := newHost(t, e, "localhost", fakeViewerAssets)
	mw := h.Middleware(http.NotFoundHandler())
	a, err := e.s.Create(ctx, CreateParams{Title: "片", Type: TypeSlides, Files: []FileChange{put("slides.json", `{"slides":[{"layout":"section","title":"x"}]}`)}})
	require.NoError(t, err)
	token, _, err := h.OpenFrame(ctx, a.ID, 0, "", "")
	require.NoError(t, err)
	body, _ := json.Marshal(map[string]any{"v": 1, "id": "w", "kind": "call", "cap": "content", "op": "write",
		"args": []any{map[string]any{"baseVersion": 1, "text": `{"slides":[{"layout":"title"}]}`}}})
	r := replyOf(t, postRuntime(mw, "a-"+token+".localhost:7600", "/_oryn/v1", "null", string(body)))
	require.Equal(t, false, r["ok"])
	assert.Contains(t, r["error"].(map[string]any)["message"], "slides[0].title")
	cfg := viewerConfigOf(t, do(mw, hostReq{host: "a-" + token + ".localhost:7600", path: "/"}).Body.String())
	assert.Equal(t, "slides", cfg["type"])
	assert.Equal(t, "slides.json", cfg["entry"])
}
