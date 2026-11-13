package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// rawOrbd 是只会照脚本写原始字节的恶意 orbd：每条连接读一个请求，记下它，再交给 respond 处理连接。
type rawOrbd struct {
	ln   net.Listener
	reqs chan *http.Request
}

func newRawOrbd(t *testing.T, respond func(c net.Conn)) *rawOrbd {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	o := &rawOrbd{ln: ln, reqs: make(chan *http.Request, 16)}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				o.reqs <- req
				respond(c)
				// 保持连接直到测试结束（「永不应答」的场景靠截止时间结束请求）
				<-done
			}()
		}
	}()
	return o
}

// transport 返回把任意主机名都拨到该 orbd 的 Transport（取代 orb link 的 fs 流）。
func (o *rawOrbd) transport() http.RoundTripper {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", o.ln.Addr().String())
		},
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 64 << 10,
	}
}

// proxyServer 起一个只做 serveOrbProxy 的前端服务。
func proxyServer(t *testing.T, o *rawOrbd, upstream string, opt orbProxyOpt) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveOrbProxy(w, r, o.transport(), "orb_test", upstream, r.URL.RawQuery, opt)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeRaw(c net.Conn, s string) { _, _ = io.WriteString(c, strings.ReplaceAll(s, "\n", "\r\n")) }

func errCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(body, &e), string(body))
	return e.Code
}

func doGet(t *testing.T, url string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

// orbd 返回白名单外的状态码（401 会让前端登出、3xx、未经请求的 101）：一律 502 orbd_bad_response，
// 原状态码与响应头都不到达客户端；101 不得把客户端连接劫持成直通 orb 的隧道。
func TestOrbProxyRejectsUnexpectedStatus(t *testing.T) {
	for name, raw := range map[string]string{
		"401": "HTTP/1.1 401 Unauthorized\nContent-Type: application/json\nContent-Length: 23\n\n{\"code\":\"unauthorized\"}",
		"302": "HTTP/1.1 302 Found\nLocation: https://evil.example/\nContent-Length: 0\n\n",
		"101": "HTTP/1.1 101 Switching Protocols\nConnection: Upgrade\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			o := newRawOrbd(t, func(c net.Conn) {
				writeRaw(c, raw)
				if name == "101" {
					_, _ = io.WriteString(c, "tunnel-bytes")
				}
			})
			srv := proxyServer(t, o, "/git/status", orbProxyOpt{})
			res, body := doGet(t, srv.URL+"/x", nil)
			assert.Equal(t, http.StatusBadGateway, res.StatusCode, string(body))
			assert.Equal(t, "orbd_bad_response", errCode(t, body))
			assert.Empty(t, res.Header.Get("Location"))
			assert.NotContains(t, string(body), "tunnel-bytes")
		})
	}
}

// 1xx 中间响应（如带 Set-Cookie 的 103）不得绕过响应头白名单到达客户端。
func TestOrbProxyDrops1xx(t *testing.T) {
	o := newRawOrbd(t, func(c net.Conn) {
		writeRaw(c, "HTTP/1.1 103 Early Hints\nSet-Cookie: oryn_session=evil\nLink: <https://evil/>; rel=preconnect\n\n"+
			"HTTP/1.1 200 OK\nContent-Type: application/json\nSet-Cookie: oryn_session=evil2\nContent-Length: 2\n\n{}")
	})
	srv := proxyServer(t, o, "/fs/list", orbProxyOpt{})
	var got1xx []textproto.MIMEHeader
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		Got1xxResponse: func(_ int, h textproto.MIMEHeader) error { got1xx = append(got1xx, h); return nil },
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/x", nil)
	require.NoError(t, err)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "{}", string(body))
	assert.Empty(t, got1xx, "1xx 中间响应不得转发给客户端")
	assert.Empty(t, res.Header.Values("Set-Cookie"))
	assert.Empty(t, res.Header.Values("Link"))
}

// 请求头只转发白名单：Cookie、Authorization、前置代理加的身份头、浏览器指纹头都不进 orb。
func TestOrbProxyRequestHeaderAllowlist(t *testing.T) {
	o := newRawOrbd(t, func(c net.Conn) {
		writeRaw(c, "HTTP/1.1 206 Partial Content\nContent-Length: 2\n\nok")
	})
	srv := proxyServer(t, o, "/fs/file", orbProxyOpt{raw: true, filename: "a.txt"})
	res, _ := doGet(t, srv.URL+"/x?path=a", map[string]string{
		"Cookie": "oryn_session=s", "Authorization": "Bearer x", "Cf-Access-Jwt-Assertion": "jwt", "Cf-Connecting-Ip": "1.2.3.4",
		"True-Client-Ip": "1.2.3.4", "User-Agent": "Mozilla/5.0", "Accept-Language": "zh", "Sec-Fetch-Site": "same-origin",
		"X-Oryn-Csrf": "c", "X-Forwarded-For": "1.2.3.4", "Range": "bytes=0-1", "If-Range": `"e"`, "Accept": "*/*",
	})
	assert.Equal(t, http.StatusPartialContent, res.StatusCode)
	req := <-o.reqs
	var names []string
	for k := range req.Header {
		if k != "Connection" { // Transport 自己加的 Connection: close（DisableKeepAlives）
			names = append(names, k)
		}
	}
	assert.ElementsMatch(t, []string{"Range", "If-Range", "Accept"}, names, "转发到 orbd 的请求头：%v", req.Header)
}

// 不应答的 orbd 不得无限占住请求：JSON 路由与 GET fs/file 等响应头超时后 504 orbd_timeout。
func TestOrbProxyDeadlines(t *testing.T) {
	oldCall, oldHdr := orbCallTimeout, orbReadHeaderTimeout
	orbCallTimeout, orbReadHeaderTimeout = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { orbCallTimeout, orbReadHeaderTimeout = oldCall, oldHdr })

	silent := newRawOrbd(t, func(net.Conn) {})
	for name, opt := range map[string]orbProxyOpt{"json": {}, "raw": {raw: true, filename: "a"}} {
		t.Run(name, func(t *testing.T) {
			srv := proxyServer(t, silent, "/git/status", opt)
			start := time.Now()
			res, body := doGet(t, srv.URL+"/x", nil)
			assert.Equal(t, http.StatusGatewayTimeout, res.StatusCode, string(body))
			assert.Equal(t, "orbd_timeout", errCode(t, body))
			assert.Less(t, time.Since(start), 5*time.Second)
		})
	}

	// GET fs/file：响应头按时到达后，下载本身不受响应头截止时间限制
	slow := newRawOrbd(t, func(c net.Conn) {
		writeRaw(c, "HTTP/1.1 200 OK\nContent-Length: 4\n\n")
		_, _ = io.WriteString(c, "ab")
		time.Sleep(3 * orbReadHeaderTimeout)
		_, _ = io.WriteString(c, "cd")
	})
	srv := proxyServer(t, slow, "/fs/file", orbProxyOpt{raw: true, filename: "a"})
	res, body := doGet(t, srv.URL+"/x", nil)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "abcd", string(body))
	assert.Equal(t, "application/octet-stream", res.Header.Get("Content-Type"))
}

// 声明长度超过上限的 JSON 响应：不转发、502 orbd_bad_response。
func TestOrbProxyJSONResponseCap(t *testing.T) {
	o := newRawOrbd(t, func(c net.Conn) {
		writeRaw(c, "HTTP/1.1 200 OK\nContent-Type: application/json\nContent-Length: 40000000\n\n[")
	})
	srv := proxyServer(t, o, "/fs/list", orbProxyOpt{})
	res, body := doGet(t, srv.URL+"/x", nil)
	assert.Equal(t, http.StatusBadGateway, res.StatusCode)
	assert.Equal(t, "orbd_bad_response", errCode(t, body))

	// 未声明长度（chunked）的超长 JSON：读到上限即中止，客户端拿不到完整响应
	cb := &cappedBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("x", 10))), left: 4}
	got, err := io.ReadAll(cb)
	assert.ErrorIs(t, err, errOrbBadResponse)
	assert.LessOrEqual(t, len(got), 4)
}

// ---- M4 §3.5：服务接口经 orb link 的 fs 流转发到 orbd 本机 API /services/* ----

// servicesEnv 是带真实 orbs.Service 与 /orb/v1/link 的 API 服务，加上经 link 接入的假 orbd。
type servicesEnv struct {
	t   *testing.T
	st  *store.Store
	svc *orbs.Service
	srv *httptest.Server
	// seen 收到假 orbd 本机 API 的请求（"METHOD /path?query"）。
	seen chan string
}

func newServicesEnv(t *testing.T) *servicesEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	svc := orbs.New(orbs.Config{
		Idle:      config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(time.Hour)},
		PublicURL: "http://x", TokenKey: make([]byte, 32),
	}, st, jobs.New(st), nil, nil)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = "https://oryn.test", dir
	s := &Server{Deps: Deps{Config: cfg, Store: st, Mounts: map[string]http.Handler{link.Path: svc.LinkHandler()}}}
	s.Orbs = &OrbsDeps{Service: svc}
	e := &servicesEnv{t: t, st: st, svc: svc, srv: httptest.NewServer(NewRouter(s)), seen: make(chan string, 16)}
	t.Cleanup(func() {
		e.srv.Close()
		svc.Close()
		_ = st.Close()
	})
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	return e
}

// orbd 本机 API 的假实现：start / stop / restart 回 OrbService，未知服务 404，日志以错误的 Content-Type 回纯文本。
func (e *servicesEnv) orbdAPI(w http.ResponseWriter, r *http.Request) {
	e.seen <- r.Method + " " + r.URL.RequestURI()
	name, op, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/services/"), "/")
	switch {
	case name == "nope":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":"service_not_found","message":"no such service"}`)
	case op == "log":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "[orbd] starting web (port 3000)\n<script>x</script>\n")
	default:
		status := apigen.OrbServiceStatusRunning
		if op == "stop" {
			status = apigen.OrbServiceStatusStopped
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apigen.OrbService{Name: name, Source: apigen.OrbServiceSourceFile, Command: "c", Status: status, Links: []apigen.OrbPortalLink{}})
	}
}

// readyOrb 插入一个 ready 的 orb，并以 features 接入一个假 orbd（服务 fs 流）。
func (e *servicesEnv) readyOrb(features ...string) string {
	e.t.Helper()
	ctx := context.Background()
	id, tok := ids.Orb(), ids.Token("oot")
	require.NoError(e.t, e.st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: "small", State: orbs.StateReady, StateChangedAt: 1, TokenHash: ids.Hash(tok), CreatedAt: 1}))
	sess, err := link.Dial(ctx, link.URL(e.srv.URL), tok)
	require.NoError(e.t, err)
	e.t.Cleanup(func() { _ = sess.Close() })
	st, err := sess.Open(ctx, link.StreamHeader{T: link.StreamCtl})
	require.NoError(e.t, err)
	ctl := link.NewCtl(st, link.CtlHandlers{OnRequest: func(context.Context, string, json.RawMessage) (any, error) {
		return apigen.LinkCtlOkResult{Ok: true}, nil
	}})
	l := link.NewStreamListener("fs")
	hs := &http.Server{Handler: http.HandlerFunc(e.orbdAPI), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = hs.Serve(l) }()
	e.t.Cleanup(func() { _ = hs.Close() })
	go func() {
		for {
			st, err := sess.AcceptStream(context.Background())
			if err != nil {
				if sess.IsClosed() {
					return
				}
				continue
			}
			l.Push(st)
		}
	}()
	require.NoError(e.t, ctl.Notify("hello", apigen.LinkCtlHelloParams{OrbdVersion: "fake", AgentVersion: "omp/test", Os: "linux", Arch: "amd64", Threads: []apigen.LinkCtlHelloThread{}, Features: features}))
	require.Eventually(e.t, func() bool { return e.svc.Connected(id) }, 5*time.Second, 10*time.Millisecond)
	return id
}

func (e *servicesEnv) do(method, path string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, nil)
	require.NoError(e.t, err)
	res, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

func TestOrbServicesAPI(t *testing.T) {
	e := newServicesEnv(t)
	orb := e.readyOrb("fs", "services")
	base := "/api/v1/orbs/" + orb + "/services/"

	for _, op := range []string{"start", "stop", "restart"} {
		res, body := e.do(http.MethodPost, base+"web/"+op)
		require.Equal(t, http.StatusOK, res.StatusCode, string(body))
		assert.Equal(t, "application/json", res.Header.Get("Content-Type"))
		var sv apigen.OrbService
		require.NoError(t, json.Unmarshal(body, &sv))
		assert.Equal(t, "web", sv.Name)
		assert.Equal(t, "POST /services/web/"+op, <-e.seen)
	}
	cur, err := e.svc.Get(context.Background(), orb)
	require.NoError(t, err)
	assert.NotNil(t, cur.LastUserInteractionAt, "启停是前台请求：计用户交互")

	// orbd 的错误码原样回给客户端
	res, body := e.do(http.MethodPost, base+"nope/stop")
	assert.Equal(t, http.StatusNotFound, res.StatusCode)
	assert.Equal(t, "service_not_found", errCode(t, body))
	<-e.seen

	// 日志：text/plain，bg 不转发，tail 原样转发
	res, body = e.do(http.MethodGet, base+"web/log?tail=100&bg=1")
	require.Equal(t, http.StatusOK, res.StatusCode, string(body))
	assert.Equal(t, "text/plain; charset=utf-8", res.Header.Get("Content-Type"))
	assert.Equal(t, "nosniff", res.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "no-store", res.Header.Get("Cache-Control"))
	assert.Equal(t, "[orbd] starting web (port 3000)\n<script>x</script>\n", string(body))
	assert.Equal(t, "GET /services/web/log?tail=100", <-e.seen)

	// 参数校验在控制面完成，不到达 orbd
	for _, p := range []string{base + "web/log?tail=0", base + "web/log?tail=1048577", base + "Bad_Name/log"} {
		res, body = e.do(http.MethodGet, p)
		assert.Equal(t, http.StatusBadRequest, res.StatusCode, p+": "+string(body))
	}
	res, _ = e.do(http.MethodPost, base+"-x/start")
	assert.Equal(t, http.StatusBadRequest, res.StatusCode)
	assert.Empty(t, e.seen)

	// 旧 orbd（hello 无 services）：409 orbd_outdated
	old := e.readyOrb("fs")
	res, body = e.do(http.MethodPost, "/api/v1/orbs/"+old+"/services/web/start")
	assert.Equal(t, http.StatusConflict, res.StatusCode)
	assert.Equal(t, "orbd_outdated", errCode(t, body))
	res, body = e.do(http.MethodGet, "/api/v1/orbs/"+old+"/services/web/log?bg=1")
	assert.Equal(t, http.StatusConflict, res.StatusCode)
	assert.Equal(t, "orbd_outdated", errCode(t, body))
	assert.Empty(t, e.seen)
}

// GET services 读控制面镜像（不经 orbd）；分享接口在未配置 portal_domain 时 409 portal_disabled。
func TestOrbServicesListAndShareDisabled(t *testing.T) {
	e := newServicesEnv(t)
	ctx := context.Background()
	orb := ids.Orb()
	require.NoError(t, e.st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: orb, ProviderID: "prov_1", Type: "small", State: orbs.StateFrozen, StateChangedAt: 1, TokenHash: "h-" + orb, CreatedAt: 1}))
	doc, _ := json.Marshal(apigen.OrbService{Name: "web", Source: apigen.OrbServiceSourceFile, Command: "c", Status: apigen.OrbServiceStatusRunning, Port: new(3000), Links: []apigen.OrbPortalLink{{Title: "web", Path: "/"}}})
	require.NoError(t, e.st.Q.InsertOrbService(ctx, db.InsertOrbServiceParams{OrbID: orb, Name: "web", Port: new(int64(3000)), HasPortal: 1, Doc: string(doc), UpdatedAt: 1}))
	exp := time.Now().Add(time.Hour).UnixMilli()
	require.NoError(t, e.st.Q.UpsertPortalShare(ctx, db.UpsertPortalShareParams{OrbID: orb, Port: 3000, CreatedAt: 1, ExpiresAt: exp}))

	res, body := e.do(http.MethodGet, "/api/v1/orbs/"+orb+"/services")
	require.Equal(t, http.StatusOK, res.StatusCode, string(body))
	var list apigen.OrbServiceList
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Services, 1)
	require.Len(t, list.Portals, 1)
	assert.Equal(t, 3000, list.Portals[0].Port)
	require.NotNil(t, list.Portals[0].Public)
	assert.Equal(t, exp, list.Portals[0].Public.ExpiresAt)
	cur, err := e.svc.Get(ctx, orb)
	require.NoError(t, err)
	assert.Equal(t, orbs.StateFrozen, cur.State, "读镜像不唤醒")
	assert.Nil(t, cur.LastUserInteractionAt)

	res, _ = e.do(http.MethodGet, "/api/v1/orbs/orb-missing/services")
	assert.Equal(t, http.StatusNotFound, res.StatusCode)

	req, _ := http.NewRequest(http.MethodPut, e.srv.URL+"/api/v1/orbs/"+orb+"/portals/3000/share", strings.NewReader(`{"duration":"1h"}`))
	r2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	b2, _ := io.ReadAll(r2.Body)
	_ = r2.Body.Close()
	assert.Equal(t, http.StatusConflict, r2.StatusCode)
	assert.Equal(t, "portal_disabled", errCode(t, b2))
	res, body = e.do(http.MethodDelete, "/api/v1/orbs/"+orb+"/portals/3000/share")
	assert.Equal(t, http.StatusConflict, res.StatusCode)
	assert.Equal(t, "portal_disabled", errCode(t, body))
}
