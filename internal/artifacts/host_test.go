package artifacts

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/portalhost"
)

const page = "<!doctype html><html><head><title>周报</title></head><body><h1>你好</h1><a href=\"https://example.com\">x</a></body></html>"

// fakeRuntime 模拟嵌入的运行时；带一个 </script 字样检查转义。
func fakeRuntime(name string) ([]byte, bool) {
	if name != runtimeAsset {
		return nil, false
	}
	return []byte(`window.__orynStart=function(c){};var s="</script>";`), true
}

func noAsset(string) ([]byte, bool) { return nil, false }

func newHost(t *testing.T, e *env, domain string, asset func(string) ([]byte, bool)) *Host {
	t.Helper()
	base, err := portalhost.NewBase("http://localhost:7600", domain)
	require.NoError(t, err)
	return NewHost(e.s, HostConfig{Base: base, Now: e.clk.now, Asset: asset})
}

func mkPage(t *testing.T, e *env, files ...FileChange) Artifact {
	t.Helper()
	if len(files) == 0 {
		files = []FileChange{put("index.html", page), put("style.css", "h1{color:red}")}
	}
	a, err := e.s.Create(context.Background(), CreateParams{Title: "周报", Files: files})
	require.NoError(t, err)
	return a
}

type hostReq struct {
	method, host, path string
	header             map[string]string
}

// do 发一个请求；跟随最新版本的帧把请求 302 到 `/_oryn/v/<head>/…` 时照着 Location 再取一次（同一主机），
// 其余状态原样返回。要断言重定向本身用 doRaw。
func do(h http.Handler, q hostReq) *httptest.ResponseRecorder {
	rec := doRaw(h, q)
	if loc := rec.Header().Get("Location"); rec.Code == http.StatusFound && strings.Contains(loc, "/"+ReservedPrefix+"/v/") {
		q.path = loc
		return doRaw(h, q)
	}
	return rec
}

func doRaw(h http.Handler, q hostReq) *httptest.ResponseRecorder {
	if q.method == "" {
		q.method = http.MethodGet
	}
	req := httptest.NewRequest(q.method, q.path, nil)
	req.Host = q.host
	for k, v := range q.header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var configRe = regexp.MustCompile(`__orynStart\((\{[^)]*\})\);</script>`)

// 泛域名模式：a-<令牌> 主机进帧服务，t-… 与主域交给 next；CSP、frame-ancestors、注入、未知令牌。
func TestHostWildcard(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h := newHost(t, e, "localhost", fakeRuntime)
	a := mkPage(t, e)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "NEXT") })
	mw := h.Middleware(next)

	token, url, err := h.OpenFrame(ctx, a.ID, 0, "")
	require.NoError(t, err)
	assert.Regexp(t, `^[a-z2-7]{26}$`, token)
	assert.Equal(t, "http://a-"+token+".localhost:7600/", url)
	host := "a-" + token + ".localhost:7600"

	rec := do(mw, hostReq{host: host, path: "/"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	csp := rec.Header().Get("Content-Security-Policy")
	assert.True(t, strings.HasPrefix(csp, "sandbox allow-scripts allow-forms allow-downloads;"), csp)
	assert.Contains(t, csp, "script-src 'self' 'unsafe-inline' 'unsafe-eval' blob: https://cdnjs.cloudflare.com")
	assert.Contains(t, csp, "connect-src 'self' https://cdnjs.cloudflare.com")
	assert.Contains(t, csp, "form-action 'none'; base-uri 'self'; webrtc 'block'; frame-ancestors http://localhost:7600")
	assert.NotContains(t, csp, "allow-same-origin")
	hd := rec.Header()
	assert.Contains(t, hd.Get("Permissions-Policy"), "camera=()")
	assert.Equal(t, "nosniff", hd.Get("X-Content-Type-Options"))
	assert.Equal(t, "no-referrer", hd.Get("Referrer-Policy"))
	assert.Equal(t, "no-store", hd.Get("Cache-Control"))
	assert.Equal(t, "noindex, nofollow", hd.Get("X-Robots-Tag"))
	assert.Empty(t, hd.Get("Cross-Origin-Resource-Policy"))
	assert.Empty(t, hd.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "text/html; charset=utf-8", hd.Get("Content-Type"))

	body := rec.Body.String()
	assert.Contains(t, body, `<h1 data-oryn-src="`)
	assert.Contains(t, body, `var s="<\/script>";`, "运行时里的 </script 被转义")
	m := configRe.FindStringSubmatch(body)
	require.NotNil(t, m, body)
	assert.Regexp(t, `^\{"v":1,"mark":"[a-z0-9]{8}","path":"index.html","runtime":true\}$`, m[1])
	assert.Less(t, strings.Index(body, "<script>"), strings.Index(body, "<title"), "运行时在页面自己的内容之前")
	assert.Greater(t, strings.Index(body, "<script>"), strings.Index(body, "<head"))

	// 申请帧时带 Origin（vite 开发服务器）→ frame-ancestors 用它；不合法的 Origin 回落到 public_url
	t2, _, err := h.OpenFrame(ctx, a.ID, 0, "http://localhost:5173")
	require.NoError(t, err)
	assert.Contains(t, do(mw, hostReq{host: "a-" + t2 + ".localhost:7600", path: "/"}).Header().Get("Content-Security-Policy"),
		"frame-ancestors http://localhost:5173")
	t3, _, err := h.OpenFrame(ctx, a.ID, 0, "http://evil; script-src *")
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(do(mw, hostReq{host: "a-" + t3 + ".localhost:7600", path: "/"}).Header().Get("Content-Security-Policy"),
		"frame-ancestors http://localhost:7600"))

	// 子资源：原样提供、支持 Range；Origin: null 得到 ACAO null
	rec = do(mw, hostReq{host: host, path: "/style.css", header: map[string]string{"Origin": "null", "Range": "bytes=0-1"}})
	assert.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Equal(t, "h1", rec.Body.String())
	assert.Equal(t, "text/css; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "null", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "Origin", rec.Header().Get("Vary"))

	assert.Equal(t, http.StatusNotFound, do(mw, hostReq{host: host, path: "/nope.js"}).Code)
	assert.Equal(t, http.StatusNotFound, do(mw, hostReq{host: host, path: "/_oryn/x"}).Code)
	assert.Equal(t, http.StatusMethodNotAllowed, do(mw, hostReq{method: http.MethodPost, host: host, path: "/"}).Code)
	assert.Equal(t, http.StatusNotFound, do(mw, hostReq{host: "a-" + strings.Repeat("a", 26) + ".localhost:7600", path: "/"}).Code)
	assert.Equal(t, "NEXT", do(mw, hostReq{host: "t-0123456789ab-p3000.localhost:7600", path: "/"}).Body.String())
	assert.Equal(t, "NEXT", do(mw, hostReq{host: "p-" + token + ".localhost:7600", path: "/"}).Body.String())
	assert.Equal(t, "NEXT", do(mw, hostReq{host: "localhost:7600", path: "/"}).Body.String())

	h.CloseFrame(token)
	assert.Equal(t, http.StatusNotFound, do(mw, hostReq{host: host, path: "/"}).Code)
}

// /_oryn/v/<n>/ 钉住版本；钉住旧版本的帧不带运行时；回收站里的回 410。
func TestHostVersions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h := newHost(t, e, "localhost", fakeRuntime)
	a := mkPage(t, e)
	_, err := e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("index.html", "<p>v2</p>")}})
	require.NoError(t, err)
	mw := h.Middleware(http.NotFoundHandler())
	token, _, err := h.OpenFrame(ctx, a.ID, 0, "")
	require.NoError(t, err)
	host := "a-" + token + ".localhost:7600"

	assert.Contains(t, do(mw, hostReq{host: host, path: "/"}).Body.String(), "v2")
	v1 := do(mw, hostReq{host: host, path: "/_oryn/v/1/index.html"})
	require.Equal(t, http.StatusOK, v1.Code)
	assert.Contains(t, v1.Body.String(), "你好")
	assert.Contains(t, do(mw, hostReq{host: host, path: "/_oryn/v/1/"}).Body.String(), "你好")
	for _, p := range []string{"/_oryn/v/3/index.html", "/_oryn/v/01/index.html", "/_oryn/v/0/index.html", "/_oryn/v/x/index.html", "/_oryn/v/1"} {
		assert.Equal(t, http.StatusNotFound, do(mw, hostReq{host: host, path: p}).Code, p)
	}

	pinned, _, err := h.OpenFrame(ctx, a.ID, 1, "")
	require.NoError(t, err)
	body := do(mw, hostReq{host: "a-" + pinned + ".localhost:7600", path: "/"}).Body.String()
	assert.Contains(t, body, "你好")
	assert.Contains(t, body, `"runtime":false`)
	_, _, err = h.OpenFrame(ctx, a.ID, 5, "")
	assert.Equal(t, CodeNotFound, code(err))

	_, err = e.s.Delete(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusGone, do(mw, hostReq{host: host, path: "/"}).Code)
	_, _, err = h.OpenFrame(ctx, a.ID, 0, "")
	assert.Equal(t, CodeDeleted, code(err))
}

// 跟随最新版本的帧：导航先 302 到当次 head 的 /_oryn/v/<n>/；取到 HTML 之后 head 升级，页面按相对路径再取的
// 子资源仍是同一版本；重新导航帧根才落到新 head。两种寻址模式一致。
func TestHostFollowPinsLoadToOneVersion(t *testing.T) {
	for _, mode := range []struct {
		name, domain, host, root string
		handler                  func(h *Host) http.Handler
	}{
		{"wildcard", "localhost", "", "/", func(h *Host) http.Handler { return h.Middleware(http.NotFoundHandler()) }},
		{"fallback", "", "localhost:7600", "", func(h *Host) http.Handler {
			mux := http.NewServeMux()
			mux.Handle(FramePathPrefix, h.Handler())
			return mux
		}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			h := newHost(t, e, mode.domain, fakeRuntime)
			a := mkPage(t, e)
			token, _, err := h.OpenFrame(ctx, a.ID, 0, "")
			require.NoError(t, err)
			srv := mode.handler(h)
			host, root := mode.host, mode.root
			if mode.domain != "" {
				host = "a-" + token + ".localhost:7600"
			} else {
				root = FramePathPrefix + token + "/"
			}

			nav := doRaw(srv, hostReq{host: host, path: root + "?x=1"})
			require.Equal(t, http.StatusFound, nav.Code)
			loc := nav.Header().Get("Location")
			assert.Equal(t, root+"_oryn/v/1/?x=1", loc)
			html := doRaw(srv, hostReq{host: host, path: loc})
			require.Equal(t, http.StatusOK, html.Code)
			assert.Contains(t, html.Body.String(), `"runtime":true`, "跟随帧落在钉住的路径上仍带运行时")

			// HTML 已取到，head 升到 v2（样式变了）
			_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("style.css", "h1{color:blue}")}})
			require.NoError(t, err)
			// 页面里的 <link href="style.css"> 相对于 Location 解析
			css := doRaw(srv, hostReq{host: host, path: root + "_oryn/v/1/style.css"})
			require.Equal(t, http.StatusOK, css.Code)
			assert.Equal(t, "h1{color:red}", css.Body.String(), "同一次加载仍读 v1")

			// 热更新：重新导航帧根落到 v2
			assert.Equal(t, root+"_oryn/v/2/", doRaw(srv, hostReq{host: host, path: root}).Header().Get("Location"))
			assert.Equal(t, "h1{color:blue}", do(srv, hostReq{host: host, path: root + "style.css"}).Body.String())
		})
	}
}

// 主域回退：/_af/<令牌>/ 提供帧，CSP 用路径源而不是 'self'。
func TestHostFallbackPath(t *testing.T) {
	e := newEnv(t)
	h := newHost(t, e, "", fakeRuntime)
	assert.Equal(t, FramesPath, h.Mode())
	a := mkPage(t, e)
	token, url, err := h.OpenFrame(context.Background(), a.ID, 0, "")
	require.NoError(t, err)
	root := "http://localhost:7600/_af/" + token + "/"
	assert.Equal(t, root, url)
	mux := http.NewServeMux()
	mux.Handle(FramePathPrefix, h.Handler())

	rec := do(mux, hostReq{host: "localhost:7600", path: "/_af/" + token + "/"})
	require.Equal(t, http.StatusOK, rec.Code)
	csp := rec.Header().Get("Content-Security-Policy")
	assert.NotContains(t, csp, "'self'")
	assert.Contains(t, csp, "script-src "+root+" 'unsafe-inline'")
	assert.Contains(t, csp, "frame-src "+root+";")
	assert.Contains(t, rec.Body.String(), "__orynStart(")
	assert.Equal(t, "h1{color:red}", do(mux, hostReq{host: "localhost:7600", path: "/_af/" + token + "/style.css"}).Body.String())

	rec = do(mux, hostReq{host: "localhost:7600", path: "/_af/" + token})
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/_af/"+token+"/", rec.Header().Get("Location"))
	assert.Equal(t, http.StatusNotFound, do(mux, hostReq{host: "localhost:7600", path: "/_af/zzzz/"}).Code)
}

// 「在浏览器中打开」的顶层帧：更宽的 sandbox、frame-ancestors 'none'、不标源码、不注入；10 分钟空闲过期。
func TestHostTopLevel(t *testing.T) {
	e := newEnv(t)
	h := newHost(t, e, "localhost", fakeRuntime)
	a := mkPage(t, e)
	url, err := h.OpenLink(context.Background(), a.ID, 0)
	require.NoError(t, err)
	token := strings.TrimSuffix(strings.TrimPrefix(url, "http://a-"), ".localhost:7600/")
	mw := h.Middleware(http.NotFoundHandler())
	host := "a-" + token + ".localhost:7600"
	rec := do(mw, hostReq{host: host, path: "/"})
	require.Equal(t, http.StatusOK, rec.Code)
	csp := rec.Header().Get("Content-Security-Policy")
	assert.True(t, strings.HasPrefix(csp, sandboxTopLevel+";"), csp)
	assert.True(t, strings.HasSuffix(csp, "frame-ancestors 'none'"), csp)
	assert.Equal(t, page, rec.Body.String())

	e.clk.add(11 * time.Minute)
	assert.Equal(t, http.StatusNotFound, do(mw, hostReq{host: host, path: "/"}).Code)
}

// 运行时资源不存在（没跑过 make web）时只标源码、不注入脚本。
func TestHostWithoutRuntimeAsset(t *testing.T) {
	e := newEnv(t)
	h := newHost(t, e, "localhost", noAsset)
	a := mkPage(t, e)
	token, _, err := h.OpenFrame(context.Background(), a.ID, 0, "")
	require.NoError(t, err)
	body := do(h.Middleware(http.NotFoundHandler()), hostReq{host: "a-" + token + ".localhost:7600", path: "/"}).Body.String()
	assert.Contains(t, body, "data-oryn-src=")
	assert.NotContains(t, body, "<script>")
}

// 最新版本声明 files / sample 时用不含 CDN 的严格策略，之后只升不降。
func TestHostStrictPolicy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h := newHost(t, e, "localhost", fakeRuntime)
	a, err := e.s.Create(ctx, CreateParams{Title: "页", Files: []FileChange{put("index.html", page)}, Capabilities: []string{"db"}})
	require.NoError(t, err)
	token, _, err := h.OpenFrame(ctx, a.ID, 0, "")
	require.NoError(t, err)
	mw := h.Middleware(http.NotFoundHandler())
	cspOf := func() string {
		return do(mw, hostReq{host: "a-" + token + ".localhost:7600", path: "/"}).Header().Get("Content-Security-Policy")
	}
	assert.Contains(t, cspOf(), "https://esm.sh")
	files := []string{"files"}
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Capabilities: &files})
	require.NoError(t, err)
	assert.NotContains(t, cspOf(), "https://")
	none := []string{}
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 2, Capabilities: &none})
	require.NoError(t, err)
	assert.NotContains(t, cspOf(), "https://", "严格模式只升不降")
}

// 帧表：满 256 个时淘汰最久未用的；普通帧空闲 12 小时过期。
func TestFrameTable(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h := newHost(t, e, "localhost", noAsset)
	a := mkPage(t, e)
	first, _, err := h.OpenFrame(ctx, a.ID, 0, "")
	require.NoError(t, err)
	e.clk.add(time.Second)
	second, _, err := h.OpenFrame(ctx, a.ID, 0, "")
	require.NoError(t, err)
	e.clk.add(time.Second)
	_, ok := h.use(first) // first 刚用过，second 成为最久未用
	require.True(t, ok)
	for range maxFrames - 1 {
		e.clk.add(time.Millisecond)
		_, _, err := h.OpenFrame(ctx, a.ID, 0, "")
		require.NoError(t, err)
	}
	assert.Len(t, h.frames, maxFrames)
	_, ok = h.use(second)
	assert.False(t, ok)
	_, ok = h.use(first)
	assert.True(t, ok)

	e.clk.add(frameIdle + time.Minute)
	_, ok = h.use(first)
	assert.False(t, ok)
}

func TestAncestorOrigin(t *testing.T) {
	const fb = "http://localhost:7600"
	cases := map[string]string{
		"":                       fb,
		"null":                   fb,
		"http://localhost:5173":  "http://localhost:5173",
		"https://Oryn.Example":   "https://oryn.example",
		"https://a.test/path":    fb,
		"javascript:alert(1)":    fb,
		"http://a.test; img-src": fb,
		"http://u@a.test":        fb,
	}
	for in, want := range cases {
		assert.Equal(t, want, ancestorOrigin(in, fb), in)
	}
}
