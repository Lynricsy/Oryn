package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/orbsock"
	"github.com/Lynricsy/Oryn/internal/portal"
	"github.com/Lynricsy/Oryn/internal/portalhost"
)

// M4 §4：portal 数据面全栈——process 驱动 + 真实 orbd + fakeomp；被代理的「服务」是测试进程自己在
// 127.0.0.1 空闲端口上的 HTTP + WebSocket 回显服务，经 orbd.sock 的 service.start 登记为带 portal 的动态服务。

// portalApp 回显请求头（/echo）与 WebSocket 消息（/ws）。
func portalApp(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
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
				if c.Write(r.Context(), websocket.MessageText, append([]byte("echo:"), b...)) != nil {
					return
				}
			}
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"host": r.Host, "header": r.Header})
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// portalClient 向 portal 主机发请求（Host 头指定主机，不跟随重定向）。
type portalClient struct {
	t    *testing.T
	s    *stack
	host string
}

func (p portalClient) get(path string, kv ...string) *http.Response {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	p.t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.s.http.URL+path, nil)
	require.NoError(p.t, err)
	req.Host = p.host
	for i := 0; i+1 < len(kv); i += 2 {
		req.Header.Add(kv[i], kv[i+1])
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	require.NoError(p.t, err)
	p.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (p portalClient) owner(t *testing.T, resp *http.Response) string {
	t.Helper()
	b := readAll(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", b)
	var v struct {
		Host   string      `json:"host"`
		Header http.Header `json:"header"`
	}
	require.NoError(t, json.Unmarshal(b, &v))
	assert.Equal(t, p.host, v.Host)
	return v.Header.Get(portal.AuthenticatedHeader)
}

func (p portalClient) dialWS(hdr http.Header) (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, strings.Replace(p.s.http.URL, "http", "ws", 1)+"/ws", &websocket.DialOptions{Host: p.host, HTTPHeader: hdr})
	if err == nil {
		p.t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, err
}

func wsEcho(t *testing.T, c *websocket.Conn, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte(msg)))
	_, b, err := c.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, "echo:"+msg, string(b))
}

func TestE2EM4Portal(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)},
		func(c *config.Orynd) { c.PortalDomain = "portal.test" })
	require.NotNil(t, s.srv.Portal)

	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt": "hi", "executor": map[string]any{"kind": "new_orb", "provider": "local"},
	}, &created, 201)
	th := s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" && v.OrbID != "" })
	code, ok := portalhost.Code(th.ID)
	require.True(t, ok)

	var tv struct {
		PortalURLTemplate string `json:"portalUrlTemplate"`
	}
	s.rest("GET", "/api/v1/threads/"+th.ID, nil, &tv, 200)
	assert.Equal(t, "http://t-"+code+"-p{port}.portal.test/", tv.PortalURLTemplate)

	// 登记服务：命令只是占位进程，端口由测试进程自己监听
	port := portalApp(t)
	cmd := "exec sleep 100000"
	_, err := orbsock.Call(context.Background(), orbsock.Path(s.drv.HomeDir(th.OrbID)), orbsock.MethodServiceStart,
		orbsock.ServiceStartParams{Name: "echo", Command: &cmd, Port: port, Portal: true, ThreadID: th.ID}, nil)
	require.NoError(t, err)

	base := "/api/v1/orbs/" + th.OrbID
	require.Eventually(t, func() bool {
		var list struct {
			Services []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"services"`
			Portals []struct {
				Port    int    `json:"port"`
				Service string `json:"service"`
			} `json:"portals"`
		}
		s.rest("GET", base+"/services", nil, &list, 200)
		return len(list.Services) == 1 && list.Services[0].Name == "echo" &&
			len(list.Portals) == 1 && list.Portals[0].Port == port && list.Portals[0].Service == "echo"
	}, 30*time.Second, 50*time.Millisecond, "服务与 portal 出现在镜像里")

	pc := portalClient{t: t, s: s, host: portalhost.Label(code, port) + ".portal.test"}
	bearer := []string{"Authorization", "Bearer " + s.token}

	// 设备令牌：200，上游看到 owner=yes；WebSocket 回显
	assert.Equal(t, "owner=yes", pc.owner(t, pc.get("/echo", bearer...)))
	ws, err := pc.dialWS(http.Header{"Authorization": {"Bearer " + s.token}})
	require.NoError(t, err)
	wsEcho(t, ws, "hi")

	// portal 主机上的 /api/v1/me 不是主站 API
	resp := pc.get("/api/v1/me")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "portal_unauthorized", errCode(t, resp))

	// 网页会话走完握手后凭 portal Cookie 访问
	session, err := auth.CreateWebSession(context.Background(), s.srv.Store, "e2e")
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodGet, s.http.URL+"/portal/auth?t="+code+"&p="+strconv.Itoa(port)+"&next=%2Fecho", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: session})
	resp, err = http.DefaultTransport.RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, pc.host, loc.Host)
	resp = pc.get(loc.RequestURI())
	require.Equal(t, http.StatusFound, resp.StatusCode)
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == portal.CookieName {
			cookie = c.Value
		}
	}
	require.NotEmpty(t, cookie)
	resp = pc.get(resp.Header.Get("Location"), "Cookie", portal.CookieName+"="+cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/echo", resp.Header.Get("Location"))
	assert.Equal(t, "owner=yes", pc.owner(t, pc.get("/echo", "Cookie", portal.CookieName+"="+cookie)))

	// 公开 → 匿名 200；终止 → 匿名 302 且已建立的匿名 WebSocket 断开
	s.rest("PUT", base+"/portals/"+strconv.Itoa(port)+"/share", map[string]string{"duration": "1h"}, nil, 200)
	assert.Equal(t, "owner=no", pc.owner(t, pc.get("/echo")))
	anon, err := pc.dialWS(nil)
	require.NoError(t, err)
	wsEcho(t, anon, "public")
	s.rest("DELETE", base+"/portals/"+strconv.Itoa(port)+"/share", nil, nil, 204)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _, err = anon.Read(ctx)
	require.Error(t, err)
	require.NoError(t, ctx.Err(), "匿名 WebSocket 应被立即断开")
	cancel()
	resp = pc.get("/echo", "Accept", "text/html")
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "http://localhost/portal/auth?t="+code+"&p="+strconv.Itoa(port)+"&next=%2Fecho", resp.Header.Get("Location"))

	// 冻结：无凭据 302 且仍 frozen；设备令牌的非导航请求唤醒并在 25s 内 200
	orbState := func() string {
		var o struct {
			State string `json:"state"`
		}
		s.rest("GET", base, nil, &o, 200)
		return o.State
	}
	s.rest("POST", base+"/pause", map[string]string{"mode": "freeze"}, nil, 202)
	require.Eventually(t, func() bool { return orbState() == "frozen" }, 30*time.Second, 50*time.Millisecond)
	before, err := s.srv.Store.Q.GetOrb(context.Background(), th.OrbID)
	require.NoError(t, err)
	resp = pc.get("/echo", "Accept", "text/html")
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "frozen", orbState())

	start := time.Now()
	assert.Equal(t, "owner=yes", pc.owner(t, pc.get("/echo", bearer...)))
	assert.Less(t, time.Since(start), 25*time.Second)
	assert.Equal(t, "ready", orbState())
	after, err := s.srv.Store.Q.GetOrb(context.Background(), th.OrbID)
	require.NoError(t, err)
	require.NotNil(t, after.LastUserInteractionAt)
	if before.LastUserInteractionAt != nil {
		assert.Greater(t, *after.LastUserInteractionAt, *before.LastUserInteractionAt)
	}
	evs, err := s.srv.Store.Q.ListOrbEvents(context.Background(), th.OrbID)
	require.NoError(t, err)
	var seq []string
	for _, e := range evs {
		if e.FromState != "" {
			seq = append(seq, e.FromState+">"+e.ToState)
		}
	}
	assert.Equal(t, []string{"freezing>frozen", "frozen>waking", "waking>connecting", "resuming>ready"},
		subseq(seq, "freezing>frozen", "frozen>waking", "waking>connecting", "resuming>ready"))

}
