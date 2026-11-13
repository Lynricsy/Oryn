package portal

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 主机解析：非 portal 主机交给主站；非法标签、未知短码、保留路径 404；orb 已删除 410；
// portal 主机上的 /api/v1/me 永远到不了主站。
func TestHostRouting(t *testing.T) {
	e := newEnv(t)
	do := func(host, path string, kv ...string) *http.Response {
		req, err := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
		require.NoError(t, err)
		req.Host = host
		for i := 0; i+1 < len(kv); i += 2 {
			req.Header.Set(kv[i], kv[i+1])
		}
		resp, err := http.DefaultTransport.RoundTrip(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	assert.Equal(t, "MAIN", body(t, do("oryn.test", "/api/v1/me")))
	assert.Equal(t, "MAIN", body(t, do("a.b.portal.test", "/")), "多级子域不是 portal 主机")

	for _, h := range []string{"x.portal.test", "t-zzzzzzzzzzzz-p1.portal.test", "t-" + e.code + "-p0.portal.test", "t-000000000000-p3000.portal.test"} {
		resp := do(h, "/")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, h)
		assert.Equal(t, "portal_not_found", errCode(t, resp), h)
	}

	// /api/v1/me：未登录 401（不是主站的响应）；带设备令牌时交给应用
	resp := e.get(portA, "/api/v1/me")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "portal_unauthorized", errCode(t, resp))
	assert.Equal(t, "app:/api/v1/me", body(t, e.get(portA, "/api/v1/me", e.bearer()...)))

	// 保留路径：应用永远收不到
	for _, p := range []string{"/__oryn/nope", "/__oryn/", "/a/../__oryn/x"} {
		resp := e.get(portA, p, e.bearer()...)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, p)
		assert.NotContains(t, body(t, resp), "app:", p)
	}

	// 线程无 orb → 404；orb 已删除 → 410
	require.NoError(t, e.st.Q.SetOrbState(e.ctx, db.SetOrbStateParams{State: orbs.StateDeleted, StateChangedAt: 1, ID: e.orbID}))
	resp = e.get(portA, "/", e.bearer()...)
	assert.Equal(t, http.StatusGone, resp.StatusCode)
	assert.Equal(t, "orb_deleted", errCode(t, resp))
}

// 未授权：导航 302 到主域名（Location 精确匹配）、XHR 401、WebSocket 握手 401；都 no-store，不计交互。
func TestUnauthorized(t *testing.T) {
	e := newEnv(t)
	resp := e.get(portA, "/a?b=1", "Accept", "text/html")
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "https://oryn.test/portal/auth?t="+e.code+"&p=3000&next=%2Fa%3Fb%3D1", resp.Header.Get("Location"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	resp = e.get(portA, "/a", "Sec-Fetch-Mode", "navigate", "Accept", "*/*")
	assert.Equal(t, http.StatusFound, resp.StatusCode, "Sec-Fetch-Mode: navigate 即导航")

	resp = e.get(portA, "/a?b=1", "Accept", "application/json")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "portal_unauthorized", errCode(t, resp))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	resp = e.get(portA, "/a", "Sec-Fetch-Mode", "cors", "Accept", "text/html")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "Sec-Fetch-Mode 优先于 Accept")

	resp = e.do(http.MethodPost, portA, "/a", headers("Accept", "text/html"))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "POST 不是导航")

	_, wsResp, err := e.dialWS(portA, nil)
	require.Error(t, err)
	require.NotNil(t, wsResp)
	assert.Equal(t, http.StatusUnauthorized, wsResp.StatusCode)

	// 无效的设备令牌：401，不再往下判定
	resp = e.get(portA, "/", "Authorization", "Bearer odt_bogus", "Accept", "text/html")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	calls, touches, _ := e.orbs.snapshot()
	assert.Zero(t, touches, "未授权请求不计交互")
	assert.Empty(t, calls, "未授权请求不判定可用性")
}

// 握手 A → B → C → D → E 全流程，以及 Cookie 属性、码单次使用。
func TestHandshake(t *testing.T) {
	e := newEnv(t)

	// A
	resp := e.get(portA, "/a?b=1", "Accept", "text/html")
	locA := resp.Header.Get("Location")
	require.Equal(t, e.authURL(portA, "/a?b=1"), locA)
	query := strings.TrimPrefix(locA, "https://oryn.test/portal/auth?")

	// B 未登录 → /login?next=<本请求>
	rec := e.stepB(query)
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/login?next=%2Fportal%2Fauth%3Ft%3D"+e.code+"%26p%3D3000%26next%3D%252Fa%253Fb%253D1", rec.Header().Get("Location"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))

	// B 已登录 → portal /__oryn/auth?code=&next=
	rec = e.stepB(query, e.webSessionCookie())
	require.Equal(t, http.StatusFound, rec.Code)
	locB := rec.Header().Get("Location")
	prefix := "https://" + e.host(portA) + "/__oryn/auth?code=opc_"
	require.True(t, strings.HasPrefix(locB, prefix), locB)
	require.True(t, strings.HasSuffix(locB, "&next=%2Fa%3Fb%3D1"), locB)
	pathC := strings.TrimPrefix(locB, "https://"+e.host(portA))

	// C → Cookie + /__oryn/auth/check
	resp = e.get(portA, pathC)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/__oryn/auth/check?next=%2Fa%3Fb%3D1", resp.Header.Get("Location"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "no-referrer", resp.Header.Get("Referrer-Policy"))
	raw := resp.Header.Get("Set-Cookie")
	var pc *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			pc = c
		}
	}
	require.NotNil(t, pc, raw)
	assert.True(t, strings.HasPrefix(pc.Value, "ops_"))
	assert.Equal(t, "/", pc.Path)
	assert.Empty(t, pc.Domain, "host-only")
	assert.True(t, pc.Secure)
	assert.True(t, pc.HttpOnly)
	assert.Equal(t, http.SameSiteNoneMode, pc.SameSite)
	assert.True(t, pc.Partitioned)
	assert.InDelta(t, int(SessionTTL/time.Second), pc.MaxAge, 5)

	// 码单次使用：再兑换回到 A
	resp = e.get(portA, pathC)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, locA, resp.Header.Get("Location"))

	// D：Cookie 带回 → next；没带回 → 说明页（不重定向）
	resp = e.get(portA, "/__oryn/auth/check?next=%2Fa%3Fb%3D1", portalCookie(pc.Value)...)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/a?b=1", resp.Header.Get("Location"))
	resp = e.get(portA, "/__oryn/auth/check?next=%2Fa%3Fb%3D1", "Accept", "text/html")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	page := body(t, resp)
	assert.Contains(t, page, `target="_blank"`)
	assert.Contains(t, page, `href="https://`+e.host(portA)+`/a?b=1"`)

	// 凭 portal Cookie 访问应用
	ec := readEcho(t, e.get(portA, "/echo", portalCookie(pc.Value)...))
	assert.Equal(t, "owner=yes", ec.Header.Get(AuthenticatedHeader))

	// Cookie 只对签发它的主机有效
	resp = e.get(portB, "/echo", portalCookie(pc.Value)...)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// E：所有者 → next；否则等同 A；设备令牌同样被认出
	resp = e.get(portA, "/__oryn/login?next=%2Fx", portalCookie(pc.Value)...)
	assert.Equal(t, "/x", resp.Header.Get("Location"))
	resp = e.get(portA, "/__oryn/login?next=%2Fx")
	assert.Equal(t, e.authURL(portA, "/x"), resp.Header.Get("Location"))
	resp = e.get(portA, "/__oryn/login?next=%2Fx", e.bearer()...)
	assert.Equal(t, "/x", resp.Header.Get("Location"))
	resp = e.get(portA, "/__oryn/login?next=%2Fx", append(portalCookie(pc.Value), "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors")...)
	assert.Equal(t, e.authURL(portA, "/x"), resp.Header.Get("Location"), "跨站的非导航请求不认 Cookie")

	_, touches, _ := e.orbs.snapshot()
	assert.Equal(t, 1, touches, "握手端点不计交互，只有 /echo 那一次")
}

// 一次性码：过期（假时钟）与标签不符都回到 A。
func TestHandshakeCodeRules(t *testing.T) {
	e := newEnv(t)
	issue := func(port int) string {
		rec := e.stepB(fmt.Sprintf("t=%s&p=%d&next=%%2F", e.code, port), e.webSessionCookie())
		require.Equal(t, http.StatusFound, rec.Code)
		loc := rec.Header().Get("Location")
		return loc[strings.Index(loc, "/__oryn/"):]
	}
	p := issue(portA)
	e.clock.Advance(CodeTTL)
	resp := e.get(portA, p)
	assert.Equal(t, e.authURL(portA, "/"), resp.Header.Get("Location"), "过期")
	assert.Empty(t, resp.Cookies())

	p = issue(portA)
	resp = e.get(portB, p)
	assert.Equal(t, e.authURL(portB, "/"), resp.Header.Get("Location"), "标签不符")
	assert.Empty(t, resp.Cookies())
	resp = e.get(portA, p)
	assert.Equal(t, e.authURL(portA, "/"), resp.Header.Get("Location"), "标签不符的兑换也消耗了码")

	// 网页会话在 B 与 C 之间登出：码作废
	p = issue(portA)
	e.svc.Revoke("web", hashOf(e.webCookie))
	resp = e.get(portA, p)
	assert.Equal(t, e.authURL(portA, "/"), resp.Header.Get("Location"))
}

// B 的输入校验与主体限制。
func TestHandshakeStepB(t *testing.T) {
	e := newEnv(t)
	for _, q := range []string{"t=zz&p=1", "t=" + e.code + "&p=0", "t=" + e.code + "&p=65536", "t=" + e.code + "&p=03000", "p=3000", "t=" + strings.ToUpper(e.code) + "&p=3000"} {
		rec := e.stepB(q, e.webSessionCookie())
		assert.Equal(t, http.StatusBadRequest, rec.Code, q)
	}
	rec := e.stepB("t=000000000000&p=3000", e.webSessionCookie())
	assert.Equal(t, http.StatusNotFound, rec.Code, "短码不存在")

	// 设备主体 → 400
	req := httptestReq("https://oryn.test/portal/auth?t=" + e.code + "&p=3000")
	req.Header.Set("Authorization", "Bearer "+e.device)
	rr := serve(e.svc.AuthHandler(), req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	// 恢复会话不能换码 → 去登录
	tok := "ows_recovery_" + e.code
	now := time.Now()
	require.NoError(t, e.st.Q.CreateWebSession(e.ctx, db.CreateWebSessionParams{
		IDHash: hashOf(tok), Kind: "recovery", CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(), LastSeenAt: now.UnixMilli(),
	}))
	rec = e.stepB("t="+e.code+"&p=3000", &http.Cookie{Name: "oryn_session", Value: tok})
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.True(t, strings.HasPrefix(rec.Header().Get("Location"), "/login?next="))

	// 恶意 next 落到 /
	rec = e.stepB("t="+e.code+"&p=3000&next=%2F%2Fevil.example", e.webSessionCookie())
	assert.True(t, strings.HasSuffix(rec.Header().Get("Location"), "&next=%2F"), rec.Header().Get("Location"))
	assert.True(t, strings.HasPrefix(rec.Header().Get("Location"), "https://"+e.host(portA)+"/"), "回跳主机由配置重算")

	req = httptestReq("https://oryn.test/portal/auth?t=" + e.code + "&p=3000")
	req.Method = http.MethodPost
	assert.Equal(t, http.StatusMethodNotAllowed, serve(e.svc.AuthHandler(), req).Code)
}

func TestSanitizeNext(t *testing.T) {
	for _, bad := range []string{
		"//evil.example",
		`/\evil.example`,
		"https://evil.example/",
		"evil.example",
		"/a\r\nSet-Cookie: x=1",
		"/" + strings.Repeat("a", maxNext),
		"/\x7f",
		"",
	} {
		assert.Equal(t, "/", sanitizeNext(bad), "%q", bad)
	}
	for _, good := range []string{"/", "/a?b=1", "/%2F%2Fevil", "/a//b", "/" + strings.Repeat("a", maxNext-1)} {
		assert.Equal(t, good, sanitizeNext(good))
	}
}

// 4.4：凭 Cookie 成为所有者的跨站矩阵。不通过时：公开端口降为匿名，否则 403。
func TestCrossSiteMatrix(t *testing.T) {
	e := newEnv(t)
	cookie := e.login(portA)
	cases := []struct {
		name   string
		method string
		hdr    []string
		allow  bool
	}{
		{"非浏览器客户端", "POST", nil, true},
		{"同源 POST", "POST", []string{"Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "cors", "Sec-Fetch-Dest", "empty"}, true},
		{"用户直接发起", "GET", []string{"Sec-Fetch-Site", "none", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}, true},
		{"跨站顶层导航", "GET", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}, true},
		{"主站内嵌 iframe", "GET", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "iframe"}, true},
		{"同站导航", "HEAD", []string{"Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}, true},
		{"同源且 Origin 一致", "POST", []string{"Origin", e.origin(portA)}, true},
		{"跨站表单 POST", "POST", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}, false},
		{"跨站 fetch", "GET", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors", "Sec-Fetch-Dest", "empty"}, false},
		{"同站 fetch", "GET", []string{"Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "no-cors", "Sec-Fetch-Dest", "image"}, false},
		{"跨站 WebSocket", "GET", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "websocket", "Sec-Fetch-Dest", "empty"}, false},
		{"跨站导航到非文档", "GET", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "embed"}, false},
		{"未知 Sec-Fetch-Site", "GET", []string{"Sec-Fetch-Site", "weird"}, false},
		{"老浏览器跨站 Origin", "POST", []string{"Origin", "https://evil.example"}, false},
		{"Origin null", "POST", []string{"Origin", "null"}, false},
	}
	for _, public := range []bool{false, true} {
		if public {
			_, err := e.svc.SetShare(e.ctx, e.orbID, portA, time.Hour)
			require.NoError(t, err)
		}
		for _, c := range cases {
			name := fmt.Sprintf("%s/public=%v", c.name, public)
			resp := e.do(c.method, portA, "/echo", headers(append(portalCookie(cookie), c.hdr...)...))
			switch {
			case c.allow:
				require.Equal(t, http.StatusOK, resp.StatusCode, name)
				if c.method != http.MethodHead {
					assert.Equal(t, "owner=yes", readEcho(t, resp).Header.Get(AuthenticatedHeader), name)
				}
			case public:
				assert.Equal(t, "owner=no", readEcho(t, resp).Header.Get(AuthenticatedHeader), name)
			default:
				assert.Equal(t, http.StatusForbidden, resp.StatusCode, name)
				if c.method != http.MethodHead && resp.Header.Get("Content-Type") == "application/json" {
					assert.Equal(t, "cross_site_blocked", errCode(t, resp), name)
				}
			}
		}
	}
	// 设备令牌不是环境凭据，不受跨站策略约束
	resp := e.do("POST", portA, "/echo", headers(append(e.bearer(), "Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example")...))
	assert.Equal(t, "owner=yes", readEcho(t, resp).Header.Get(AuthenticatedHeader))
}

// 4.5：发给应用的请求头与回给浏览器的响应头。
func TestProxyHeaders(t *testing.T) {
	e := newEnv(t)
	ec := readEcho(t, e.get(portA, "/echo",
		"Authorization", "Bearer "+e.device,
		"Cookie", "foo=bar; "+CookieName+"=zzz",
		AuthenticatedHeader, "owner=forged",
		"X-Oryn-Other", "x",
		"X-Forwarded-Host", "evil.example",
	))
	assert.Equal(t, e.host(portA), ec.Host, "应用看到公开主机名")
	assert.Equal(t, []string{"owner=yes"}, ec.Header.Values(AuthenticatedHeader))
	assert.Empty(t, ec.Header.Values("X-Oryn-Other"))
	assert.Equal(t, []string{"foo=bar"}, ec.Header.Values("Cookie"))
	assert.Empty(t, ec.Header.Values("Authorization"), "设备令牌被剥掉")
	assert.Equal(t, e.host(portA), ec.Header.Get("X-Forwarded-Host"))
	assert.Equal(t, "https", ec.Header.Get("X-Forwarded-Proto"))
	assert.NotEmpty(t, ec.Header.Get("X-Forwarded-For"))

	cookie := e.login(portA)
	resp := e.get(portA, "/echo", "Cookie", CookieName+"="+cookie+"; a=1", "Authorization", "Bearer app")
	assert.Empty(t, resp.Header.Values("X-Frame-Options"))
	assert.Equal(t, []string{"default-src 'self'", "frame-ancestors 'self' https://oryn.test"}, resp.Header.Values("Content-Security-Policy"))
	var setCookies []string
	for _, c := range resp.Cookies() {
		setCookies = append(setCookies, c.Name)
	}
	assert.Equal(t, []string{"app"}, setCookies, "应用不能设置 portal Cookie")
	assert.Empty(t, resp.Header.Get("X-Robots-Tag"), "所有者的响应不加 noindex")
	ec = readEcho(t, resp)
	assert.Equal(t, []string{"a=1"}, ec.Header.Values("Cookie"), "portal Cookie 不给应用")
	assert.Equal(t, "Bearer app", ec.Header.Get("Authorization"), "应用自己的 Authorization 原样")
	assert.Equal(t, "owner=yes", ec.Header.Get(AuthenticatedHeader))

	// 只有 portal Cookie：Cookie 头整个删除
	ec = readEcho(t, e.get(portA, "/echo", portalCookie(cookie)...))
	assert.Empty(t, ec.Header.Values("Cookie"))

	// 匿名访客：伪造的身份头被覆盖，响应带 noindex
	_, err := e.svc.SetShare(e.ctx, e.orbID, portA, time.Hour)
	require.NoError(t, err)
	resp = e.get(portA, "/echo", AuthenticatedHeader, "owner=yes")
	assert.Equal(t, "noindex, nofollow", resp.Header.Get("X-Robots-Tag"))
	assert.Equal(t, []string{"owner=no"}, readEcho(t, resp).Header.Values(AuthenticatedHeader))
}

func TestStripCookieAndCSP(t *testing.T) {
	assert.Equal(t, "a=1; b=2", stripCookie(CookieName+"=x; a=1; b=2", CookieName))
	assert.Equal(t, "a=1; b=2", stripCookie("a=1; "+CookieName+"=x; b=2", CookieName))
	assert.Equal(t, "a=1;b=2", stripCookie("a=1;b=2", CookieName))
	assert.Equal(t, "", stripCookie(CookieName+"=x", CookieName))
	assert.Equal(t, "x"+CookieName+"=1", stripCookie("x"+CookieName+"=1", CookieName))
	assert.Equal(t, "default-src 'self'; img-src *", dropDirective("default-src 'self'; FRAME-ANCESTORS 'none' ; img-src *", "frame-ancestors"))
	assert.Equal(t, "", dropDirective("frame-ancestors 'none'", "frame-ancestors"))
}

// 透传：SSE 逐块到达（上游用通道控制节奏）；WebSocket 回显。
func TestStreamingAndWebSocket(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/sse", nil)
	require.NoError(t, err)
	req.Host = e.host(portA)
	req.Header.Set("Authorization", "Bearer "+e.device)
	resp, err := http.DefaultTransport.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	br := bufio.NewReader(resp.Body)
	for _, m := range []string{"one", "two", "three"} {
		e.sse <- m // 下一块只在上一块被读到之后才发送：未逐块 flush 时这里会卡住直到超时
		assert.Equal(t, "data: "+m+"\n", readLine(t, br))
		assert.Equal(t, "\n", readLine(t, br))
	}
	close(e.sse)

	c, _, err := e.dialWS(portA, headers(e.bearer()...))
	require.NoError(t, err)
	defer c.CloseNow()
	echoOnce(t, c, "hi")
	echoOnce(t, c, strings.Repeat("x", 20_000))
}

// 4.5 ErrorHandler：拨号错误码映射。
func TestDialErrors(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{&orbs.PortalDialError{Code: "not_exposed"}, 404, "portal_not_found"},
		{&orbs.PortalDialError{Code: "forbidden_port"}, 403, "forbidden_port"},
		{&orbs.PortalDialError{Code: "connection_refused"}, 502, "service_unreachable"},
		{&orbs.PortalDialError{Code: "timeout"}, 504, "portal_timeout"},
		{&orbs.PortalDialError{Code: "failed", Message: "x"}, 502, "portal_unreachable"},
		{fmt.Errorf("wrap: %w", orbs.ErrOrbNotReady), 503, "orb_unavailable"},
		{orbs.ErrOrbdOutdated, 409, "orbd_outdated"},
		{fmt.Errorf("boom"), 502, "portal_unreachable"},
	}
	for _, c := range cases {
		e.orbs.set(func(f *fakeOrbs) { f.dialErr = c.err })
		resp := e.get(portA, "/", e.bearer()...)
		assert.Equal(t, c.status, resp.StatusCode, c.code)
		assert.Equal(t, c.code, errCode(t, resp))
	}
	// 导航请求的 502 是 3 秒自动刷新的页面
	e.orbs.set(func(f *fakeOrbs) { f.dialErr = &orbs.PortalDialError{Code: "connection_refused"} })
	resp := e.get(portA, "/", append(e.bearer(), "Accept", "text/html")...)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Contains(t, body(t, resp), `<meta http-equiv="refresh" content="3">`)
}

func hashOf(tok string) string { return ids.Hash(tok) }

func httptestReq(target string) *http.Request {
	return httptest.NewRequest(http.MethodGet, target, nil)
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}
