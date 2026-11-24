package artifacts

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/portalhost"
)

// publicAsset 模拟嵌入的运行时与访客心跳脚本。
func publicAsset(name string) ([]byte, bool) {
	switch name {
	case runtimeAsset:
		return []byte(`window.__orynStart=function(c){};`), true
	case beaconAsset:
		return []byte(`/*beacon*/var s="</script>";`), true
	}
	return nil, false
}

// pubEnv 是打开了发布与发布页的测试环境；domain 为空时是主域回退模式。
type pubEnv struct {
	*env
	h  *Host
	mw http.Handler
}

func newPubEnv(t *testing.T, domain string) pubEnv {
	t.Helper()
	e := newEnv(t)
	base, err := portalhost.NewBase("http://localhost:7600", domain)
	require.NoError(t, err)
	h := NewHost(e.s, HostConfig{Base: base, Now: e.clk.now, Asset: publicAsset})
	e.s.ConfigurePublishing(PublishConfig{Base: base, GrantKey: DeriveGrantKey([]byte("master")), Visitors: h.Visitors})
	e.s.SetActivityVisitors(h.ArtifactVisitors)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "NEXT") })
	mw := h.Middleware(next)
	if domain == "" {
		mux := http.NewServeMux()
		mux.Handle(PublishPathPrefix, h.PublicHandler())
		mux.Handle("/", next)
		mw = mux
	}
	return pubEnv{env: e, h: h, mw: mw}
}

// publish 建一个两文件的页面并按 access 发布，返回 artifact 与发布记录。
func (pe pubEnv) publish(t *testing.T, access string) (Artifact, Publication) {
	t.Helper()
	a := mkPage(t, pe.env)
	p, err := pe.s.Publish(context.Background(), a.ID, PublishParams{Access: access})
	require.NoError(t, err)
	return a, p
}

// pubReq 发一个发布页请求（泛域名模式的主机，或回退模式的 /p/<slug> 前缀）。
func pubReq(pe pubEnv, slug, method, path string, form url.Values) *httptest.ResponseRecorder {
	host := "localhost:7600"
	if pe.h.base.Enabled() {
		host = "p-" + slug + ".localhost:7600"
	} else {
		path = "/p/" + slug + path
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	req.Host = host
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	pe.mw.ServeHTTP(rec, req)
	return rec
}

func TestPublicPageWildcard(t *testing.T) {
	pe := newPubEnv(t, "localhost")
	_, p := pe.publish(t, AccessPublic)

	rec := pubReq(pe, p.Slug, "GET", "/", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "<h1>你好</h1>")
	assert.NotContains(t, body, "__orynStart", "发布页不注入页面运行时")
	assert.NotContains(t, body, "data-oryn-src", "发布页不标源码")
	assert.Contains(t, body, `<meta name="robots" content="noindex">`)
	assert.Contains(t, body, `<script data-oryn-beat="/">/*beacon*/var s="<\/script>";</script>`)
	assert.Less(t, strings.Index(body, "noindex"), strings.Index(body, "<title"), "插在页面自己的内容之前")

	csp := rec.Header().Get("Content-Security-Policy")
	assert.True(t, strings.HasPrefix(csp, sandboxTopLevel+";"), csp)
	assert.Equal(t, "sandbox allow-scripts allow-forms allow-downloads allow-popups allow-popups-to-escape-sandbox allow-modals allow-top-navigation-by-user-activation", sandboxTopLevel)
	self := "http://p-" + p.Slug + ".localhost:7600/"
	assert.Contains(t, csp, "script-src "+self+" 'unsafe-inline' 'unsafe-eval' blob: https://cdnjs.cloudflare.com", "非严格策略、显式源（WebKit 不透明源里 'self' 不匹配）")
	assert.NotContains(t, csp, "script-src 'self'")
	assert.True(t, strings.HasSuffix(csp, "; frame-ancestors 'self'"), csp)
	assert.Equal(t, "noindex, nofollow", rec.Header().Get("X-Robots-Tag"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	rec = pubReq(pe, p.Slug, "GET", "/style.css", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "h1{color:red}", rec.Body.String())
	assert.NotContains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors", "根之外的文件可被概览页内嵌")

	assert.Equal(t, http.StatusNotFound, pubReq(pe, p.Slug, "GET", "/_oryn/v/1/index.html", nil).Code, "只提供发布的版本")
	assert.Equal(t, http.StatusMethodNotAllowed, pubReq(pe, p.Slug, "POST", "/", nil).Code)

	// 其它主机照旧交给 next；a- 帧标签不会进发布页
	rec = doRaw(pe.mw, hostReq{host: "t-abcdefgh-p3000.localhost:7600", path: "/"})
	assert.Equal(t, "NEXT", rec.Body.String())
}

func TestPublicPageFallback(t *testing.T) {
	pe := newPubEnv(t, "")
	_, p := pe.publish(t, AccessPublic)
	self := "http://localhost:7600/p/" + p.Slug + "/"

	rec := doRaw(pe.mw, hostReq{host: "localhost:7600", path: "/p/" + p.Slug})
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/p/"+p.Slug+"/", rec.Header().Get("Location"))

	rec = pubReq(pe, p.Slug, "GET", "/", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	csp := rec.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "script-src "+self+" 'unsafe-inline'")
	assert.Contains(t, csp, "base-uri "+self+";")
	assert.NotContains(t, csp, "script-src 'self'")
	assert.Contains(t, csp, "frame-ancestors 'self'")
	assert.Contains(t, rec.Body.String(), `data-oryn-beat="/p/`+p.Slug+`/"`)
	assert.Equal(t, "h1{color:red}", pubReq(pe, p.Slug, "GET", "/style.css", nil).Body.String())

	assert.Equal(t, http.StatusNotFound, doRaw(pe.mw, hostReq{host: "localhost:7600", path: "/p/NOTASLUG/"}).Code)
}

func TestPublicGateCode(t *testing.T) {
	for _, domain := range []string{"localhost", ""} {
		t.Run("domain="+domain, func(t *testing.T) {
			pe := newPubEnv(t, domain)
			_, p := pe.publish(t, AccessCode)
			root := "/"
			if domain == "" {
				root = "/p/" + p.Slug + "/"
			}

			for _, path := range []string{"/", "/style.css", "/~bogus/", "/~bogus/style.css"} {
				rec := pubReq(pe, p.Slug, "GET", path, nil)
				require.Equal(t, http.StatusOK, rec.Code, path)
				body := rec.Body.String()
				assert.Contains(t, body, `action="`+root+`_oryn/unlock"`, path)
				assert.NotContains(t, body, "你好", "访问码页不含 artifact 内容")
				assert.NotContains(t, body, "周报", "访问码页不含标题")
				assert.NotContains(t, body, "color:red")
				assert.NotContains(t, body, " disabled")
				assert.Equal(t, gatePolicy, rec.Header().Get("Content-Security-Policy"))
				assert.True(t, strings.HasPrefix(gatePolicy, "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'"))
			}

			// 码错：提示并计数
			rec := pubReq(pe, p.Slug, "POST", "/_oryn/unlock", url.Values{"code": {"zzzz"}})
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Contains(t, rec.Body.String(), "访问码不对")

			// 码对：303 到带授权段的根，内容与子资源都能取到
			rec = pubReq(pe, p.Slug, "POST", "/_oryn/unlock", url.Values{"code": {strings.ToUpper(p.Code)}})
			require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
			loc := rec.Header().Get("Location")
			require.True(t, strings.HasPrefix(loc, root+"~"), loc)
			grantPath := strings.TrimPrefix(loc, root[:len(root)-1])
			rec = pubReq(pe, p.Slug, "GET", grantPath, nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "<h1>你好</h1>")
			assert.Contains(t, rec.Body.String(), `data-oryn-beat="`+loc+`"`, "心跳也带着授权")
			rec = pubReq(pe, p.Slug, "GET", grantPath+"style.css", nil)
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "h1{color:red}", rec.Body.String())

			// 授权段缺结尾斜杠时补上
			rec = pubReq(pe, p.Slug, "GET", strings.TrimSuffix(grantPath, "/"), nil)
			assert.Equal(t, http.StatusFound, rec.Code)
			assert.Equal(t, loc, rec.Header().Get("Location"))

			// 换码后旧授权失效
			_, err := pe.s.RotateCode(context.Background(), p.ArtifactID)
			require.NoError(t, err)
			rec = pubReq(pe, p.Slug, "GET", grantPath, nil)
			assert.Contains(t, rec.Body.String(), `name="code"`)
			assert.NotContains(t, rec.Body.String(), "你好")
		})
	}
}

func TestPublicGateLocked(t *testing.T) {
	pe := newPubEnv(t, "localhost")
	_, p := pe.publish(t, AccessCode)
	for range MaxCodeFailures {
		pubReq(pe, p.Slug, "POST", "/_oryn/unlock", url.Values{"code": {"zzzz"}})
	}
	const locked = "访问码被输错太多次，暂时不能输入。请向分享者要一个新的访问码。"
	for _, rec := range []*httptest.ResponseRecorder{
		pubReq(pe, p.Slug, "GET", "/", nil),
		pubReq(pe, p.Slug, "POST", "/_oryn/unlock", url.Values{"code": {p.Code}}),
	} {
		body := rec.Body.String()
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, body, locked)
		assert.Contains(t, body, `<fieldset style="border:0;margin:0;padding:0" disabled>`, "锁定时表单禁用")
	}

	// 英文浏览器看到英文文案
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "p-" + p.Slug + ".localhost:7600"
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,zh;q=0.5")
	rec := httptest.NewRecorder()
	pe.mw.ServeHTTP(rec, req)
	assert.Contains(t, rec.Body.String(), "Ask the person who shared it for a new access code.")

	// 换码解锁
	np, err := pe.s.RotateCode(context.Background(), p.ArtifactID)
	require.NoError(t, err)
	rec = pubReq(pe, p.Slug, "POST", "/_oryn/unlock", url.Values{"code": {np.Code}})
	assert.Equal(t, http.StatusSeeOther, rec.Code)
}

func TestPublicGatePrivate(t *testing.T) {
	for _, domain := range []string{"localhost", ""} {
		t.Run("domain="+domain, func(t *testing.T) {
			pe := newPubEnv(t, domain)
			_, p := pe.publish(t, AccessPrivate)
			for _, path := range []string{"/", "/style.css", "/~bogus/"} {
				rec := pubReq(pe, p.Slug, "GET", path, nil)
				assert.Equal(t, http.StatusFound, rec.Code, path)
				assert.Equal(t, "http://localhost:7600/artifacts/open/"+p.Slug, rec.Header().Get("Location"))
			}
			u, err := pe.s.Grant(context.Background(), p.Slug)
			require.NoError(t, err)
			pu, err := url.Parse(u)
			require.NoError(t, err)
			path := pu.Path
			if domain == "" {
				path = strings.TrimPrefix(path, "/p/"+p.Slug)
			}
			rec := pubReq(pe, p.Slug, "GET", path, nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "<h1>你好</h1>")

			// 授权过期后回到登录跳转
			pe.clk.add(GrantTTL + time.Second)
			assert.Equal(t, http.StatusFound, pubReq(pe, p.Slug, "GET", path, nil).Code)
		})
	}
}

func TestPublicUnpublishedAndDeleted(t *testing.T) {
	pe := newPubEnv(t, "localhost")
	ctx := context.Background()
	a, p := pe.publish(t, AccessPublic)

	// 回收站里的 artifact 发布页照常可看
	_, err := pe.s.Delete(ctx, a.ID)
	require.NoError(t, err)
	rec := pubReq(pe, p.Slug, "GET", "/", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "你好")

	require.NoError(t, pe.s.Unpublish(ctx, a.ID))
	rec = pubReq(pe, p.Slug, "GET", "/", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "这个页面已不在线上")
	assert.NotContains(t, rec.Body.String(), "你好")
	assert.Equal(t, http.StatusNotFound, pubReq(pe, p.Slug, "GET", "/style.css", nil).Code)
}

func TestPublicPublishedVersion(t *testing.T) {
	pe := newPubEnv(t, "localhost")
	ctx := context.Background()
	a := mkPage(t, pe.env)
	_, err := pe.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("index.html", "<h1>第二版</h1>")}})
	require.NoError(t, err)
	one := 1
	p, err := pe.s.Publish(ctx, a.ID, PublishParams{Access: AccessPublic, Version: &one})
	require.NoError(t, err)
	body := pubReq(pe, p.Slug, "GET", "/", nil).Body.String()
	assert.Contains(t, body, "<h1>你好</h1>")
	assert.NotContains(t, body, "第二版")
}

func TestPublicTypedAndCanvasRoot(t *testing.T) {
	pe := newPubEnv(t, "localhost")
	ctx := context.Background()
	doc, err := pe.s.Create(ctx, CreateParams{Title: "文档", Type: TypeDocument, Files: []FileChange{put("document.md", "# 标题\n")}})
	require.NoError(t, err)
	p, err := pe.s.Publish(ctx, doc.ID, PublishParams{Access: AccessPublic})
	require.NoError(t, err)
	rec := pubReq(pe, p.Slug, "GET", "/", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `id="oryn-viewer"`)
	assert.Contains(t, body, `"root":"/"`)
	assert.Contains(t, body, `noindex`)
	assert.NotContains(t, body, "__orynStart", "查看器没有运行时，只读显示")
	assert.Equal(t, "# 标题\n", pubReq(pe, p.Slug, "GET", "/document.md", nil).Body.String())

	c := mkCanvas(t, pe.env)
	p, err = pe.s.Publish(ctx, c.ID, PublishParams{Access: AccessPublic})
	require.NoError(t, err)
	rec = pubReq(pe, p.Slug, "GET", "/", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, strings.HasSuffix(rec.Header().Get("Content-Security-Policy"), "; frame-ancestors 'self'"), "根带 frame-ancestors")
	frame := pubReq(pe, p.Slug, "GET", "/a.html", nil)
	assert.NotContains(t, frame.Header().Get("Content-Security-Policy"), "frame-ancestors", "被概览页内嵌的画框不带 frame-ancestors")
	assert.Equal(t, "<p>a</p>", stripInjected(frame.Body.String()))
	assert.Contains(t, rec.Body.String(), `id="oryn-canvas"`)
	assert.Contains(t, rec.Body.String(), `data-oryn-beat="/"`)
}

// stripInjected 去掉发布页插入的 meta 与心跳脚本。
func stripInjected(s string) string {
	s = strings.Replace(s, `<meta name="robots" content="noindex">`, "", 1)
	if i := strings.Index(s, "<script data-oryn-beat="); i >= 0 {
		if j := strings.Index(s[i:], "</script>"); j >= 0 {
			s = s[:i] + s[i+j+len("</script>"):]
		}
	}
	return s
}

func TestPublicVisitors(t *testing.T) {
	pe := newPubEnv(t, "localhost")
	ctx := context.Background()
	a, p := pe.publish(t, AccessPublic)

	beat := func(id string) int {
		req := httptest.NewRequest("POST", "/_oryn/beat", strings.NewReader(id))
		req.Host = "p-" + p.Slug + ".localhost:7600"
		req.Header.Set("Origin", "null")
		rec := httptest.NewRecorder()
		pe.mw.ServeHTTP(rec, req)
		return rec.Code
	}
	assert.Equal(t, http.StatusNoContent, beat("aaaaaaaa11"))
	assert.Equal(t, http.StatusNoContent, beat("bbbbbbbb22"))
	assert.Equal(t, http.StatusNoContent, beat("aaaaaaaa11"), "同一实例重复心跳不加数")
	assert.Equal(t, http.StatusBadRequest, beat("短"))
	assert.Equal(t, http.StatusBadRequest, beat(strings.Repeat("a", 200)))
	assert.Equal(t, 2, pe.h.Visitors(p.Slug))
	assert.Equal(t, 2, pe.h.ArtifactVisitors(a.ID))
	got, err := pe.s.Publication(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, got.Visitors, "发布状态带访客数")

	pe.clk.add(VisitorWindow + time.Second)
	assert.Equal(t, 0, pe.h.Visitors(p.Slug))
	assert.Equal(t, 0, pe.h.ArtifactVisitors(a.ID))

	// 访问码页后面的心跳不计数
	_, cp := pe.publish(t, AccessCode)
	req := httptest.NewRequest("POST", "/_oryn/beat", strings.NewReader("cccccccc33"))
	req.Host = "p-" + cp.Slug + ".localhost:7600"
	rec := httptest.NewRecorder()
	pe.mw.ServeHTTP(rec, req)
	assert.Contains(t, rec.Body.String(), `name="code"`)
	assert.Equal(t, 0, pe.h.Visitors(cp.Slug))
}
