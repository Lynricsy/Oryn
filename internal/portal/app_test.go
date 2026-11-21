package portal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- 假 Apps ----

type fakeApps struct {
	mu    sync.Mutex
	apps  map[string]AppTarget
	calls int
	// after 在每次解析得出结果之后、返回之前调用（n 从 1 起），用来在判定中途改状态。
	after func(n int)
}

func (f *fakeApps) ResolveApp(_ context.Context, name string) (AppTarget, bool, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	at, ok := f.apps[name]
	after := f.after
	f.mu.Unlock()
	if after != nil {
		after(n)
	}
	return at, ok, nil
}

func (f *fakeApps) set(name string, fn func(a *AppTarget)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.apps[name]
	fn(&a)
	f.apps[name] = a
}

const appName = "hello"

// newAppEnv 在 newEnv 之上接入假 Apps：app hello 在夹具的 orb 上，端口 30000，默认公开且已部署。
func newAppEnv(t *testing.T) (*env, *fakeApps) {
	t.Helper()
	e := newEnv(t)
	fa := &fakeApps{apps: map[string]AppTarget{
		appName: {BoxID: "box_1", App: appName, OrbID: e.orbID, Port: 30000, Public: true, Live: true},
	}}
	e.svc.SetApps(fa)
	return e, fa
}

func appHost(name string) string { return name + ".portal.test" }

// appDo 向 app 主机发请求（不跟随重定向）。
func (e *env) appDo(name, path string, kv ...string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	require.NoError(e.t, err)
	req.Host = appHost(name)
	req.Header = headers(kv...)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 10 * time.Second}
	resp, err := c.Do(req)
	require.NoError(e.t, err)
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (e *env) appWS(name string, hdr http.Header) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws://"+e.srv.Listener.Addr().String()+"/ws", &websocket.DialOptions{Host: appHost(name), HTTPHeader: hdr})
}

func appAuthURL(name, next string) string {
	return "https://oryn.test/portal/auth?h=" + name + "&next=" + url.QueryEscape(next)
}

func (e *env) dials() int {
	e.orbs.mu.Lock()
	defer e.orbs.mu.Unlock()
	return e.orbs.dials
}

// 公开 app：匿名请求透传、owner=no；不计交互、不唤醒；普通响应保留 app 自己的防嵌套头，不加 noindex。
func TestAppPublicAnonymous(t *testing.T) {
	e, _ := newAppEnv(t)
	resp := e.appDo(appName, "/echo")
	h := resp.Header
	ec := readEcho(t, resp)
	assert.Equal(t, appHost(appName), ec.Host)
	assert.Equal(t, "owner=no", ec.Header.Get(AuthenticatedHeader))
	assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
	assert.Equal(t, []string{"default-src 'self'; frame-ancestors 'none'", "Frame-Ancestors 'none'"}, h.Values("Content-Security-Policy"))
	assert.Empty(t, h.Get("X-Robots-Tag"))
	// Set-Cookie 的处理照做：portal Cookie 被丢弃，其余去掉 Domain
	assert.Equal(t, []string{"app=1; Path=/; Secure"}, h.Values("Set-Cookie"))

	calls, touches, _ := e.orbs.snapshot()
	assert.Zero(t, touches, "app 请求不计用户交互")
	for _, c := range calls {
		assert.False(t, c.wake, "app 请求不唤醒")
	}
}

// 带 oryn-preview 的请求（主站悬停预览）沿用 thread portal 的整套改写。
func TestAppPreviewRewritten(t *testing.T) {
	e, _ := newAppEnv(t)
	resp := e.appDo(appName, "/echo?oryn-preview=dark")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	h := resp.Header
	assert.Empty(t, h.Get("X-Frame-Options"))
	csp := h.Values("Content-Security-Policy")
	assert.Contains(t, csp, "default-src 'self'")
	assert.Contains(t, csp, "frame-ancestors 'self' https://oryn.test")
	for _, v := range csp {
		assert.NotContains(t, strings.ToLower(v), "'none'")
	}
	assert.Equal(t, "noindex, nofollow", h.Get("X-Robots-Tag"))
}

// 私有 app：导航 302 到 /portal/auth?h=，非导航 401，设备令牌直通；握手完成后凭 Cookie 是所有者。
func TestAppPrivateHandshake(t *testing.T) {
	e, fa := newAppEnv(t)
	fa.set(appName, func(a *AppTarget) { a.Public = false })

	resp := e.appDo(appName, "/x?y=1", "Accept", "text/html")
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, appAuthURL(appName, "/x?y=1"), resp.Header.Get("Location"))

	resp = e.appDo(appName, "/echo")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "portal_unauthorized", errCode(t, resp))

	ec := readEcho(t, e.appDo(appName, "/echo", e.bearer()...))
	assert.Equal(t, "owner=yes", ec.Header.Get(AuthenticatedHeader))
	assert.Empty(t, ec.Header.Get("Authorization"))

	// /__oryn/login 未登录时走 A（app 形式）
	resp = e.appDo(appName, "/__oryn/login?next=%2Fecho")
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, appAuthURL(appName, "/echo"), resp.Header.Get("Location"))

	// 步骤 B → C → D
	rec := e.stepB("h="+appName+"&next=%2Fecho", e.webSessionCookie())
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	loc := rec.Header().Get("Location")
	prefix := "https://" + appHost(appName) + "/"
	require.True(t, strings.HasPrefix(loc, prefix+"__oryn/auth?code="), loc)
	resp = e.appDo(appName, "/"+strings.TrimPrefix(loc, prefix))
	require.Equal(t, http.StatusFound, resp.StatusCode)
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			cookie = c.Value
		}
	}
	require.NotEmpty(t, cookie)
	check := resp.Header.Get("Location")
	resp = e.appDo(appName, check, portalCookie(cookie)...)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/echo", resp.Header.Get("Location"))

	ec = readEcho(t, e.appDo(appName, "/echo", append(portalCookie(cookie), "Accept", "text/html", "Sec-Fetch-Mode", "navigate")...))
	assert.Equal(t, "owner=yes", ec.Header.Get(AuthenticatedHeader))

	// app 的 Cookie 不能用在 thread portal 上（标签不同）
	resp = e.get(portA, "/", portalCookie(cookie)...)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// 说明页里的链接指向 app 主机
	resp = e.appDo(appName, "/__oryn/auth/check?next=%2Fa")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body(t, resp), `href="https://hello.portal.test/a"`)
}

// 步骤 B 的 app 分支：名字不合规 400，不存在 404，未登录去登录页。
func TestAppAuthHandler(t *testing.T) {
	e, _ := newAppEnv(t)
	assert.Equal(t, http.StatusBadRequest, e.stepB("h=bad--name&next=%2F", e.webSessionCookie()).Code)
	assert.Equal(t, http.StatusBadRequest, e.stepB("h="+portalhostLabel(e)+"&next=%2F", e.webSessionCookie()).Code)
	assert.Equal(t, http.StatusNotFound, e.stepB("h=nope&next=%2F", e.webSessionCookie()).Code)
	rec := e.stepB("h=" + appName + "&next=%2F")
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/login?next=")

	// 没有接入 Apps 时 app 分支 404
	e.svc.SetApps(nil)
	assert.Equal(t, http.StatusNotFound, e.stepB("h="+appName+"&next=%2F", e.webSessionCookie()).Code)
}

// portalhostLabel 是夹具线程 portal 的标签（不能当 app 名）。
func portalhostLabel(e *env) string { return strings.TrimSuffix(e.host(portA), ".portal.test") }

// DropApp 断开在途的匿名 WebSocket；所有者的连接不受影响。
func TestAppDropDisconnectsAnonymous(t *testing.T) {
	e, _ := newAppEnv(t)
	anon, _, err := e.appWS(appName, nil)
	require.NoError(t, err)
	defer anon.CloseNow()
	echoOnce(t, anon, "a")
	owner, _, err := e.appWS(appName, headers(e.bearer()...))
	require.NoError(t, err)
	defer owner.CloseNow()
	echoOnce(t, owner, "o")

	e.svc.DropApp(appName)
	requireClosed(t, anon)
	echoOnce(t, owner, "still")

	// 吊销设备令牌同样断开 app 主机上的所有者连接
	e.svc.Revoke("device", deviceID(t, e))
	requireClosed(t, owner)
}

// 赛跑：解析时还是公开的，判定与登记之间被改成私有并 DropApp → 整个重新判定，最终不被代理。
func TestAppPublicToPrivateRace(t *testing.T) {
	for _, nav := range []bool{false, true} {
		t.Run(fmt.Sprintf("nav=%v", nav), func(t *testing.T) {
			e, fa := newAppEnv(t)
			fa.after = func(n int) {
				if n == 1 {
					fa.set(appName, func(a *AppTarget) { a.Public = false })
					e.svc.DropApp(appName)
				}
			}
			var kv []string
			if nav {
				kv = []string{"Accept", "text/html"}
			}
			resp := e.appDo(appName, "/echo", kv...)
			if nav {
				require.Equal(t, http.StatusFound, resp.StatusCode)
				assert.Equal(t, appAuthURL(appName, "/echo"), resp.Header.Get("Location"))
			} else {
				require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			}
			assert.Zero(t, e.dials(), "匿名请求不应被代理")
			fa.mu.Lock()
			assert.Equal(t, 2, fa.calls, "重试从解析起整个重来")
			fa.mu.Unlock()
		})
	}
}

// 没有 live release：所有者 502 app_not_deployed，私有 app 的匿名访客 401；都不拨号。
func TestAppNotDeployed(t *testing.T) {
	e, fa := newAppEnv(t)
	fa.set(appName, func(a *AppTarget) { a.Live, a.Public = false, false })

	resp := e.appDo(appName, "/echo", e.bearer()...)
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Equal(t, "app_not_deployed", errCode(t, resp))

	resp = e.appDo(appName, "/echo")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	fa.set(appName, func(a *AppTarget) { a.Public = true })
	resp = e.appDo(appName, "/echo")
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Equal(t, "app_not_deployed", errCode(t, resp))

	assert.Zero(t, e.dials(), "即使 orb 上有服务开着这个端口也不拨号")
}

// box 的 orb 不可用：503 app_unavailable，导航请求是 5 秒自动刷新的过渡页；不唤醒、不计交互。
func TestAppUnavailableNoWake(t *testing.T) {
	e, _ := newAppEnv(t)
	e.orbs.set(func(f *fakeOrbs) { f.usable = fmt.Errorf("%w: stopped", orbs.ErrOrbNotReady) })

	resp := e.appDo(appName, "/echo", e.bearer()...)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "5", resp.Header.Get("Retry-After"))
	assert.Equal(t, "app_unavailable", errCode(t, resp))

	resp = e.appDo(appName, "/", "Accept", "text/html")
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "5", resp.Header.Get("Retry-After"))
	assert.Contains(t, body(t, resp), `<meta http-equiv="refresh" content="5">`)

	calls, touches, _ := e.orbs.snapshot()
	require.NotEmpty(t, calls)
	for _, c := range calls {
		assert.False(t, c.wake, "app 主机不唤醒 box")
	}
	assert.Zero(t, touches)
	assert.Zero(t, e.dials())
}

// 未知 app、没有接入 Apps、orb 已删除：404 portal_not_found。
func TestAppNotFound(t *testing.T) {
	e, _ := newAppEnv(t)
	resp := e.appDo("nope", "/")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "portal_not_found", errCode(t, resp))

	require.NoError(t, e.st.Q.SetOrbState(e.ctx, db.SetOrbStateParams{State: orbs.StateDeleted, StateChangedAt: 1, ID: e.orbID}))
	resp = e.appDo(appName, "/", e.bearer()...)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "portal_not_found", errCode(t, resp))

	e.svc.SetApps(nil)
	resp = e.appDo(appName, "/")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "portal_not_found", errCode(t, resp))
}

// 连不上 app 的端口：状态码与错误码同 thread portal，文案说应用没有在监听。
func TestAppProxyErrorMessage(t *testing.T) {
	e, _ := newAppEnv(t)
	e.orbs.set(func(f *fakeOrbs) { f.dialErr = &orbs.PortalDialError{Code: "connection_refused"} })
	resp := e.appDo(appName, "/")
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(b), "service_unreachable")
	assert.Contains(t, string(b), "应用没有在监听")
}

// 开在 box 上的线程自己声明的 portal：唤醒被闸门挡下 → 503 box_stopped（导航与非导航都是）。
func TestThreadPortalBoxStopped(t *testing.T) {
	e := newEnv(t)
	e.orbs.set(func(f *fakeOrbs) {
		f.usable = fmt.Errorf("%w: stopped", orbs.ErrOrbNotReady)
		f.wakeErr = orbs.ErrBoxStopped
	})
	resp := e.get(portA, "/", e.bearer()...)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "box_stopped", errCode(t, resp))

	resp = e.get(portA, "/", append(e.bearer(), "Accept", "text/html")...)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(t, body(t, resp), "box_stopped")
}
