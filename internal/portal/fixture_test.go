package portal

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- 假时钟与假定时器 ----

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	mu      sync.Mutex
	stopped bool
}

func (t *fakeTimer) isStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

type fakeTimers struct {
	mu    sync.Mutex
	list  []*fakeTimer
	armed chan *fakeTimer
}

func (ft *fakeTimers) AfterFunc(d time.Duration, f func()) func() {
	t := &fakeTimer{d: d, f: f}
	ft.mu.Lock()
	ft.list = append(ft.list, t)
	ft.mu.Unlock()
	select {
	case ft.armed <- t:
	default:
	}
	return func() {
		t.mu.Lock()
		t.stopped = true
		t.mu.Unlock()
	}
}

func (ft *fakeTimers) last() *fakeTimer {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if len(ft.list) == 0 {
		return nil
	}
	return ft.list[len(ft.list)-1]
}

// ---- 假 Orbs ----

type ensureCall struct {
	wake bool
	wait time.Duration
}

type fakeOrbs struct {
	st *store.Store

	mu        sync.Mutex
	upstream  string // 全部端口都拨到这个地址
	usable    error  // nil = 可用
	gate      chan struct{}
	waiting   chan struct{}
	calls     []ensureCall
	touches   int
	services  map[int]apigen.OrbService
	refreshes []string
	dialErr   error
}

func (f *fakeOrbs) Get(ctx context.Context, orbID string) (db.Orb, error) {
	o, err := f.st.Q.GetOrb(ctx, orbID)
	if errors.Is(err, sql.ErrNoRows) {
		return o, orbs.ErrNotFound
	}
	return o, err
}

func (f *fakeOrbs) EnsureUsable(ctx context.Context, _, feature string, wake bool, wait time.Duration) error {
	f.mu.Lock()
	f.calls = append(f.calls, ensureCall{wake, wait})
	usable, gate, waiting := f.usable, f.gate, f.waiting
	f.mu.Unlock()
	if feature != featurePortal {
		return fmt.Errorf("unexpected feature %q", feature)
	}
	if usable == nil || !wake || wait == 0 || gate == nil {
		return usable
	}
	if waiting != nil {
		close(waiting)
	}
	select {
	case <-gate:
		f.mu.Lock()
		f.usable = nil
		f.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeOrbs) DialPortal(ctx context.Context, _ string, _ int) (net.Conn, error) {
	f.mu.Lock()
	addr, derr := f.upstream, f.dialErr
	f.mu.Unlock()
	if derr != nil {
		return nil, derr
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

func (f *fakeOrbs) PortalService(_ context.Context, _ string, port int) (apigen.OrbService, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	svc, ok := f.services[port]
	return svc, ok, nil
}

func (f *fakeOrbs) TouchUserCoalesced(context.Context, string) {
	f.mu.Lock()
	f.touches++
	f.mu.Unlock()
}

func (f *fakeOrbs) Refresh(_ context.Context, orbID string) {
	f.mu.Lock()
	f.refreshes = append(f.refreshes, orbID)
	f.mu.Unlock()
}

func (f *fakeOrbs) set(fn func(f *fakeOrbs)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeOrbs) snapshot() (calls []ensureCall, touches int, refreshes []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ensureCall(nil), f.calls...), f.touches, append([]string(nil), f.refreshes...)
}

// ---- 夹具 ----

const (
	portA = 3000 // 有 portal
	portB = 3001 // 有 portal
	portC = 3002 // 镜像里没有
)

type env struct {
	t        *testing.T
	ctx      context.Context
	st       *store.Store
	orbs     *fakeOrbs
	clock    *fakeClock
	timers   *fakeTimers
	svc      *Service
	srv      *httptest.Server // portal 数据面 + 主站兜底（返回 MAIN）
	upstream *httptest.Server
	sse      chan string
	base     portalhost.Base

	threadID, code, orbID string
	device                string // odt_…
	webCookie             string // 主站会话 Cookie（__Host-oryn_session）的值
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	e := &env{t: t, ctx: ctx, st: st, sse: make(chan string)}
	e.upstream = httptest.NewServer(http.HandlerFunc(e.app))
	t.Cleanup(e.upstream.Close)

	now := time.Now().Truncate(time.Millisecond)
	provID := ids.New("prov")
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: provID, Name: "p", TokenHash: "h", CreatedAt: now.UnixMilli()}))
	e.orbID = ids.Orb()
	require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{
		ID: e.orbID, ProviderID: provID, Type: "small", Cpus: 1, MemoryMb: 1024, Image: "img",
		State: "ready", StateChangedAt: now.UnixMilli(), TokenHash: "t", CreatedAt: now.UnixMilli(),
	}))
	e.threadID = ids.Thread()
	code, ok := portalhost.Code(e.threadID)
	require.True(t, ok)
	e.code = code
	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{
		Kind: "work", ID: e.threadID, OrbID: &e.orbID, Title: "t", TitleSource: "auto", State: "idle", StateChangedAt: now.UnixMilli(),
		SessionDir: "/s", CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(), ShortCode: &code,
	}))
	_, e.device, err = auth.CreateDevice(ctx, st, "dev", "cli", "linux")
	require.NoError(t, err)
	e.webCookie, err = auth.CreateWebSession(ctx, st, "ua")
	require.NoError(t, err)

	e.base, err = portalhost.NewBase("https://oryn.test", "portal.test")
	require.NoError(t, err)
	e.clock = &fakeClock{t: now}
	e.timers = &fakeTimers{armed: make(chan *fakeTimer, 16)}
	e.orbs = &fakeOrbs{st: st, upstream: e.upstream.Listener.Addr().String(), services: map[int]apigen.OrbService{
		portA: {Name: "web", Port: new(portA), Links: []apigen.OrbPortalLink{{Title: "web", Path: "/"}}},
		portB: {Name: "api", Port: new(portB), Links: []apigen.OrbPortalLink{{Title: "api", Path: "/"}}},
	}}
	e.svc = New(Config{Base: e.base, Now: e.clock.Now, AfterFunc: e.timers.AfterFunc}, st, e.orbs)
	main := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "MAIN") })
	e.srv = httptest.NewServer(e.svc.Middleware(main))
	t.Cleanup(e.srv.Close)
	return e
}

// app 是被代理的应用：/echo 回显请求头并设置会被网关改写的响应头；/sse 按通道节奏推送；/ws WebSocket 回显。
func (e *env) app(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/echo":
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Add("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		w.Header().Add("Content-Security-Policy", "Frame-Ancestors 'none'")
		w.Header().Add("Set-Cookie", "app=1; Domain=oryn.test; Path=/; domain=.test; Secure")
		w.Header().Add("Set-Cookie", CookieName+"=evil; Path=/")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"host": r.Host, "header": r.Header})
	case "/sse":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for msg := range e.sse {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", msg)
			w.(http.Flusher).Flush()
		}
	case "/ws":
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			_, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), websocket.MessageText, append([]byte("echo:"), b...)); err != nil {
				return
			}
		}
	default:
		_, _ = io.WriteString(w, "app:"+r.URL.RequestURI())
	}
}

func (e *env) host(port int) string { return portalhost.Label(e.code, port) + ".portal.test" }

func (e *env) origin(port int) string { return "https://" + e.host(port) }

// do 向 portal 主机发请求（不跟随重定向）。
func (e *env) do(method string, port int, path string, hdr http.Header) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, nil)
	require.NoError(e.t, err)
	req.Host = e.host(port)
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 10 * time.Second}
	resp, err := c.Do(req)
	require.NoError(e.t, err)
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (e *env) get(port int, path string, kv ...string) *http.Response {
	e.t.Helper()
	return e.do(http.MethodGet, port, path, headers(kv...))
}

func headers(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

func errCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var v struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&v))
	return v.Code
}

type echo struct {
	Host   string      `json:"host"`
	Header http.Header `json:"header"`
}

func readEcho(t *testing.T, resp *http.Response) echo {
	t.Helper()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var v echo
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&v))
	return v
}

func (e *env) bearer() []string { return []string{"Authorization", "Bearer " + e.device} }

// authURL 是步骤 A 的期望地址。
func (e *env) authURL(port int, next string) string {
	return "https://oryn.test/portal/auth?t=" + e.code + "&p=" + fmt.Sprint(port) + "&next=" + url.QueryEscape(next)
}

// stepB 以主域名 Cookie 调 /portal/auth，返回响应。
func (e *env) stepB(query string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "https://oryn.test/portal/auth?"+query, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.svc.AuthHandler().ServeHTTP(rec, req)
	return rec
}

func (e *env) webSessionCookie() *http.Cookie {
	return &http.Cookie{Name: auth.CookieName, Value: e.webCookie}
}

// login 走完握手 B → C → D，返回 portal Cookie 的值。
func (e *env) login(port int) string {
	e.t.Helper()
	rec := e.stepB(fmt.Sprintf("t=%s&p=%d&next=%%2F", e.code, port), e.webSessionCookie())
	require.Equal(e.t, http.StatusFound, rec.Code, rec.Body.String())
	loc := rec.Header().Get("Location")
	prefix := "https://" + e.host(port) + "/"
	require.True(e.t, strings.HasPrefix(loc, prefix), loc)
	resp := e.get(port, "/"+strings.TrimPrefix(loc, prefix))
	require.Equal(e.t, http.StatusFound, resp.StatusCode)
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			return c.Value
		}
	}
	e.t.Fatal("没有 portal Cookie")
	return ""
}

func portalCookie(v string) []string { return []string{"Cookie", CookieName + "=" + v} }

// dialWS 在 portal 主机上建立 WebSocket（/ws 回显）。
func (e *env) dialWS(port int, hdr http.Header) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws://"+e.srv.Listener.Addr().String()+"/ws", &websocket.DialOptions{Host: e.host(port), HTTPHeader: hdr})
}

func echoOnce(t *testing.T, c *websocket.Conn, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte(msg)))
	_, b, err := c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "echo:"+msg, string(b))
}

// requireClosed 断言连接已被网关断开：读立即出错（而不是等到超时）。
func requireClosed(t *testing.T, c *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	require.Error(t, err)
	require.NoError(t, ctx.Err(), "连接应被立即断开，而不是等到超时")
}

func readLine(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	return line
}
