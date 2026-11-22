package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/desktop"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- M25：桌面应用启动、窗口操作与图标经 orb link 的 fs 流转发到 orbd 本机 API ----

// deskAppsEnv 是带真实 orbs / desktop 服务与 /orb/v1/link 的 API 服务，加上经 link 接入的假 orbd。
type deskAppsEnv struct {
	t   *testing.T
	st  *store.Store
	svc *orbs.Service
	srv *httptest.Server
}

func newDeskAppsEnv(t *testing.T) *deskAppsEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	svc := orbs.New(orbs.Config{
		Idle:      config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(time.Hour)},
		PublicURL: "http://x", TokenKey: make([]byte, 32),
	}, st, jobs.New(st), nil, nil)
	box, err := secrets.New(bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	dsvc, err := desktop.New(desktop.Config{DataDir: dir, Box: box}, st, svc, auth.NewTickets())
	require.NoError(t, err)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = "https://oryn.test", dir
	s := &Server{Deps: Deps{Config: cfg, Store: st, Mounts: map[string]http.Handler{link.Path: svc.LinkHandler()}}}
	s.Orbs = &OrbsDeps{Service: svc}
	s.Desktop = &DesktopDeps{Service: dsvc}
	e := &deskAppsEnv{t: t, st: st, svc: svc, srv: httptest.NewServer(NewRouter(s))}
	t.Cleanup(func() {
		e.srv.Close()
		dsvc.Close()
		svc.Close()
		_ = st.Close()
	})
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	return e
}

// deskAppsOrbd 是假 orbd 的 /desktop/apps|windows 路由：vscode 启动成功，busy → control_agent，其余应用 → app_not_found；
// 窗口 1 可操作，其余 → window_not_found；图标：vscode 回 SVG，html 回 text/html，其余 → app_not_found。
func deskAppsOrbd(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"code":"`+code+`","message":"`+code+`"}`)
	}
	snapshot := func() {
		w.Header().Set("Content-Type", "application/json")
		// 回包里的窗口列表比通知旧：控制面不应据此覆盖镜像
		_ = json.NewEncoder(w).Encode(apigen.LinkCtlDesktopStateParams{
			DisplayReady: true, Browser: apigen.OrbBrowserStatusStopped,
			Apps: []apigen.OrbDesktopApp{{ID: "vscode", Name: "VS Code", Launching: true}}, Windows: []apigen.OrbDesktopWindow{},
		})
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/desktop/apps/vscode/launch",
		r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/desktop/windows/1/"):
		snapshot()
	case r.URL.Path == "/desktop/apps/busy/launch":
		fail(http.StatusConflict, "control_agent")
	case strings.HasPrefix(r.URL.Path, "/desktop/windows/"):
		fail(http.StatusNotFound, "window_not_found")
	case r.Method == http.MethodGet && r.URL.Path == "/desktop/apps/vscode/icon":
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	case r.Method == http.MethodGet && r.URL.Path == "/desktop/apps/html/icon":
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<script>alert(1)</script>`)
	case strings.HasPrefix(r.URL.Path, "/desktop/apps/"):
		fail(http.StatusNotFound, "app_not_found")
	default:
		fail(http.StatusNotFound, "not_found")
	}
}

// readyOrb 插入一个 ready 的 orb，以 features 接入假 orbd，返回 orb id 与 ctl（发 desktop.state 用）。
func (e *deskAppsEnv) readyOrb(features ...string) (string, *link.Ctl) {
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
	hs := &http.Server{Handler: http.HandlerFunc(deskAppsOrbd), ReadHeaderTimeout: 5 * time.Second}
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
	return id, ctl
}

func (e *deskAppsEnv) do(method, path string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, nil)
	require.NoError(e.t, err)
	res, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

// 旧 orbd（hello 没有 desktop-apps）：三个接口都回 409 orbd_outdated。
func TestDesktopAppsAPIOutdated(t *testing.T) {
	e := newDeskAppsEnv(t)
	orb, _ := e.readyOrb("fs", orbs.FeatureDesktop)
	base := "/api/v1/orbs/" + orb + "/desktop/"
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "apps/vscode/launch"}, {http.MethodPost, "windows/1/close"}, {http.MethodGet, "apps/vscode/icon"},
	} {
		res, body := e.do(c.method, base+c.path)
		assert.Equal(t, http.StatusConflict, res.StatusCode, c.path)
		assert.Equal(t, "orbd_outdated", errCode(t, body), c.path)
	}
}

// 启动与窗口操作回 200 Orb；镜像里的 apps / windows 只认 desktop.state，回包里较旧的列表不覆盖它；orbd 的错误码同码映射。
func TestDesktopAppsAPI(t *testing.T) {
	e := newDeskAppsEnv(t)
	orb, ctl := e.readyOrb("fs", orbs.FeatureDesktop, orbs.FeatureDesktopApps)
	base := "/api/v1/orbs/" + orb + "/desktop/"
	app := "vscode"
	wins := []apigen.OrbDesktopWindow{{ID: "1", AppID: &app, Title: "main.go", Active: true}}
	require.NoError(t, ctl.Notify("desktop.state", apigen.LinkCtlDesktopStateParams{
		DisplayReady: true, Browser: apigen.OrbBrowserStatusStopped, Apps: []apigen.OrbDesktopApp{{ID: app, Name: "VS Code", Running: true}}, Windows: wins,
	}))
	require.Eventually(t, func() bool {
		d, err := e.st.Q.GetOrbDesktop(context.Background(), orb)
		return err == nil && d.Windows != nil
	}, 5*time.Second, 10*time.Millisecond)

	for _, path := range []string{"apps/vscode/launch", "windows/1/minimize"} {
		res, body := e.do(http.MethodPost, base+path)
		require.Equal(t, http.StatusOK, res.StatusCode, string(body))
		var o apigen.Orb
		require.NoError(t, json.Unmarshal(body, &o))
		require.NotNil(t, o.Desktop, path)
		assert.Equal(t, wins, o.Desktop.Windows, path)
		require.Len(t, o.Desktop.Apps, 1, path)
		assert.True(t, o.Desktop.Apps[0].Running, path)
	}

	for _, c := range []struct {
		path   string
		status int
		code   string
	}{
		{"apps/busy/launch", http.StatusConflict, "control_agent"},
		{"apps/ghost/launch", http.StatusNotFound, "app_not_found"},
		{"windows/9/close", http.StatusNotFound, "window_not_found"},
		{"windows/1/maximize", http.StatusBadRequest, "invalid_request"},
	} {
		res, body := e.do(http.MethodPost, base+c.path)
		assert.Equal(t, c.status, res.StatusCode, c.path)
		assert.Equal(t, c.code, errCode(t, body), c.path)
	}
}

// 图标：只转发 PNG / SVG，带缓存与安全响应头；别的类型 502；没有图标 404；orb 不在 ready 时不唤醒，回 409 orb_unavailable。
func TestDesktopAppIconAPI(t *testing.T) {
	e := newDeskAppsEnv(t)
	orb, _ := e.readyOrb("fs", orbs.FeatureDesktop, orbs.FeatureDesktopApps)
	base := "/api/v1/orbs/" + orb + "/desktop/apps/"

	res, body := e.do(http.MethodGet, base+"vscode/icon")
	require.Equal(t, http.StatusOK, res.StatusCode, string(body))
	assert.Equal(t, "image/svg+xml", res.Header.Get("Content-Type"))
	assert.Equal(t, "private, max-age=86400", res.Header.Get("Cache-Control"))
	assert.Equal(t, "nosniff", res.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "sandbox", res.Header.Get("Content-Security-Policy"))
	assert.Contains(t, string(body), "<svg")

	res, body = e.do(http.MethodGet, base+"html/icon")
	assert.Equal(t, http.StatusBadGateway, res.StatusCode)
	assert.Equal(t, "orbd_bad_response", errCode(t, body))
	assert.NotEqual(t, "text/html", res.Header.Get("Content-Type"))

	res, body = e.do(http.MethodGet, base+"ghost/icon")
	assert.Equal(t, http.StatusNotFound, res.StatusCode)
	assert.Equal(t, "app_not_found", errCode(t, body))

	require.NoError(t, e.st.Q.SetOrbState(context.Background(), db.SetOrbStateParams{State: orbs.StateFrozen, StateChangedAt: 2, ID: orb}))
	res, body = e.do(http.MethodGet, base+"vscode/icon")
	assert.Equal(t, http.StatusConflict, res.StatusCode)
	assert.Equal(t, "orb_unavailable", errCode(t, body))
	cur, err := e.svc.Get(context.Background(), orb)
	require.NoError(t, err)
	assert.Equal(t, orbs.StateFrozen, cur.State, "取图标不唤醒")
	assert.Nil(t, cur.LastUserInteractionAt, "取图标不计用户交互")
}
