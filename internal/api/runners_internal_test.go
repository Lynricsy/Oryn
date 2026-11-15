package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// runnerEnv 是 runner REST 与 orb 代理的测试环境：真实的 orbs.Service + API 路由。
type runnerEnv struct {
	t   *testing.T
	st  *store.Store
	svc *orbs.Service
	s   *Server
	srv *httptest.Server

	mu sync.Mutex
	// seen 是假 orbd 本机 API 收到的请求："URI|X-Oryn-Workdir"。
	seen []string
}

func newRunnerEnv(t *testing.T) *runnerEnv {
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
	e := &runnerEnv{t: t, st: st, svc: svc, s: s, srv: httptest.NewServer(NewRouter(s))}
	t.Cleanup(func() {
		e.srv.Close()
		svc.Close()
		_ = st.Close()
	})
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	return e
}

func (e *runnerEnv) do(method, path string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	require.NoError(e.t, err)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func (e *runnerEnv) orbdAPI(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	e.seen = append(e.seen, r.URL.RequestURI()+"|"+r.Header.Get(orbs.HeaderWorkdir))
	e.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"entries":[]}`)
}

func (e *runnerEnv) takeSeen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.seen
	e.seen = nil
	return out
}

// connect 以 token 接入一个假 orbd（服务 fs 流），hello 带 features（runner 时另带 runner 信息），等到 hello 生效。
func (e *runnerEnv) connect(orbID, token string, runner bool, features ...string) {
	e.t.Helper()
	ctx := context.Background()
	sess, err := link.Dial(ctx, link.URL(e.srv.URL), token)
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
	hello := apigen.LinkCtlHelloParams{OrbdVersion: "fake", AgentVersion: "omp/test", Os: "linux", Arch: "amd64", Threads: []apigen.LinkCtlHelloThread{}, Features: features}
	if runner {
		hello.Runner = &apigen.LinkRunnerInfo{Hostname: "box", Dirs: []apigen.RunnerDir{{Path: "/work/app", Name: "app", Git: true}}}
	}
	require.NoError(e.t, ctl.Notify("hello", hello))
	require.Eventually(e.t, func() bool {
		o, err := e.svc.Get(ctx, orbID)
		return e.svc.Connected(orbID) && err == nil && o.State == orbs.StateReady
	}, 5*time.Second, 10*time.Millisecond)
}

func (e *runnerEnv) thread(id, orbID string, cwd *string) {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.CreateThread(context.Background(), db.CreateThreadParams{
		Kind: "work", ID: id, OrbID: &orbID, Title: "t", TitleSource: "auto", State: "idle", SessionDir: "~/.oryn/threads/" + id,
		CreatedAt: 1, UpdatedAt: 1, Cwd: cwd,
	}))
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var e apigen.Error
	require.NoError(t, json.Unmarshal(body, &e), string(body))
	return e.Code
}

func TestRunnerREST(t *testing.T) {
	e := newRunnerEnv(t)
	ctx := context.Background()

	code, body := e.do("POST", "/api/v1/runners", map[string]any{"name": "Bad Name"})
	assert.Equal(t, http.StatusBadRequest, code, string(body))
	code, body = e.do("POST", "/api/v1/runners", map[string]any{"name": "local"})
	assert.Equal(t, http.StatusConflict, code, "与 provider 共用唯一约束")
	assert.Equal(t, "conflict", errorCode(t, body))

	code, body = e.do("POST", "/api/v1/runners", map[string]any{"name": "laptop"})
	require.Equal(t, http.StatusCreated, code, string(body))
	var created apigen.RunnerCreated
	require.NoError(t, json.Unmarshal(body, &created))
	assert.Regexp(t, `^ort_`, created.Token)
	assert.Equal(t, "laptop", created.Runner.Name)
	assert.False(t, created.Runner.Online)
	assert.Equal(t, []apigen.RunnerDir{}, created.Runner.Dirs)

	code, body = e.do("GET", "/api/v1/runners", nil)
	require.Equal(t, http.StatusOK, code)
	var list apigen.RunnerList
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Runners, 1)
	assert.Equal(t, created.Runner.ID, list.Runners[0].ID)

	// runner 的 provider 行：不出现在 GET /providers，DELETE /providers 当它不存在，不能作项目默认
	code, body = e.do("GET", "/api/v1/providers", nil)
	require.Equal(t, http.StatusOK, code)
	assert.NotContains(t, string(body), created.Runner.ID)
	code, _ = e.do("DELETE", "/api/v1/providers/"+created.Runner.ID, nil)
	assert.Equal(t, http.StatusNotFound, code)
	assert.Error(t, e.s.checkProviderName(ctx, new("laptop")))
	assert.NoError(t, e.s.checkProviderName(ctx, new("local")))

	// 上线后列表显示在线与 hello 信息
	e.connect(created.Runner.OrbID, created.Token, true, "fs", "git", "metrics")
	code, body = e.do("GET", "/api/v1/runners", nil)
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Runners, 1)
	assert.True(t, list.Runners[0].Online)
	assert.Equal(t, []string{"fs", "git", "metrics"}, list.Runners[0].Features)

	// runner 的 orb：kind / features；pause / resume / delete → 409 runner_unsupported
	code, body = e.do("GET", "/api/v1/orbs/"+created.Runner.OrbID, nil)
	require.Equal(t, http.StatusOK, code)
	var ov apigen.Orb
	require.NoError(t, json.Unmarshal(body, &ov))
	require.NotNil(t, ov.Kind)
	assert.Equal(t, apigen.OrbKindRunner, *ov.Kind)
	assert.Equal(t, []string{"fs", "git", "metrics"}, ov.Features)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/pause", map[string]any{"mode": "freeze"}},
		{"POST", "/pause", map[string]any{"mode": "stop"}},
		{"POST", "/resume", nil},
		{"DELETE", "", nil},
	} {
		code, body = e.do(c.method, "/api/v1/orbs/"+created.Runner.OrbID+c.path, c.body)
		assert.Equal(t, http.StatusConflict, code, c.method+c.path)
		assert.Equal(t, "runner_unsupported", errorCode(t, body), c.method+c.path)
	}

	code, _ = e.do("DELETE", "/api/v1/runners/"+created.Runner.ID, nil)
	assert.Equal(t, http.StatusNoContent, code)
	code, _ = e.do("DELETE", "/api/v1/runners/"+created.Runner.ID, nil)
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = e.do("DELETE", "/api/v1/runners/prov_1", nil)
	assert.Equal(t, http.StatusNotFound, code, "普通 provider 不是 runner")
	code, body = e.do("GET", "/api/v1/runners", nil)
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, json.Unmarshal(body, &list))
	assert.Empty(t, list.Runners)
}

func TestRunnerOrbProxyThreadScope(t *testing.T) {
	e := newRunnerEnv(t)
	ctx := context.Background()
	r, tok, err := e.svc.CreateRunner(ctx, "laptop")
	require.NoError(t, err)
	e.connect(r.OrbID, tok, true, "fs", "git")
	cwd := "/work/app"
	e.thread("T-runner", r.OrbID, &cwd)

	// 普通 orb：不带 thread 照旧、不加头
	orbID, otok := ids.Orb(), ids.Token("oot")
	require.NoError(t, e.st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: orbID, ProviderID: "prov_1", Type: "small", State: orbs.StateReady, StateChangedAt: 1, TokenHash: ids.Hash(otok), CreatedAt: 1}))
	e.connect(orbID, otok, false, "fs", "git")
	e.thread("T-orb", orbID, nil)

	// runner 的 orb 不带 thread → 400 thread_required（fs 与 git 都是）
	for _, p := range []string{"/fs/list?path=.", "/git/status"} {
		code, body := e.do("GET", "/api/v1/orbs/"+r.OrbID+p, nil)
		assert.Equal(t, http.StatusBadRequest, code, p)
		assert.Equal(t, "thread_required", errorCode(t, body), p)
	}
	// 别的 orb 的线程、不存在的线程 → 400 invalid_request
	for _, th := range []string{"T-orb", "T-missing"} {
		code, body := e.do("GET", "/api/v1/orbs/"+r.OrbID+"/fs/list?path=.&thread="+th, nil)
		assert.Equal(t, http.StatusBadRequest, code, th)
		assert.Equal(t, "invalid_request", errorCode(t, body), th)
	}
	assert.Empty(t, e.takeSeen(), "校验失败的请求不到 orbd")

	// runner 线程：带 X-Oryn-Workdir，thread 参数不转给 orbd
	code, body := e.do("GET", "/api/v1/orbs/"+r.OrbID+"/fs/list?path=.&thread=T-runner", nil)
	require.Equal(t, http.StatusOK, code, string(body))
	code, body = e.do("POST", "/api/v1/orbs/"+r.OrbID+"/git/stage?thread=T-runner", map[string]any{"paths": []string{"a"}})
	require.Equal(t, http.StatusOK, code, string(body))
	assert.Equal(t, []string{"/fs/list?path=.|" + cwd, "/git/stage|" + cwd}, e.takeSeen())

	// 普通 orb：不带 thread、带没有 cwd 的线程都不加头
	code, _ = e.do("GET", "/api/v1/orbs/"+orbID+"/fs/list?path=.", nil)
	require.Equal(t, http.StatusOK, code)
	code, _ = e.do("GET", "/api/v1/orbs/"+orbID+"/fs/list?path=.&thread=T-orb", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"/fs/list?path=.|", "/fs/list?path=.|"}, e.takeSeen())

	// 客户端自己带的 X-Oryn-Workdir 不会转给 orbd
	req, err := http.NewRequest("GET", e.srv.URL+"/api/v1/orbs/"+orbID+"/fs/list?path=.", nil)
	require.NoError(t, err)
	req.Header.Set(orbs.HeaderWorkdir, "/etc")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = res.Body.Close()
	assert.Equal(t, []string{"/fs/list?path=.|"}, e.takeSeen())

	// runner 离线（stopped）时前台请求 → 409 runner_offline
	require.NoError(t, e.st.Q.SetOrbState(ctx, db.SetOrbStateParams{State: orbs.StateStopped, StateChangedAt: 2, ID: r.OrbID}))
	e.svc.Kick(r.OrbID, "test")
	require.Eventually(t, func() bool { return !e.svc.Connected(r.OrbID) }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, e.st.Q.SetOrbState(ctx, db.SetOrbStateParams{State: orbs.StateStopped, StateChangedAt: 3, ID: r.OrbID}))
	code, body = e.do("GET", "/api/v1/orbs/"+r.OrbID+"/fs/list?path=.&thread=T-runner", nil)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "runner_offline", errorCode(t, body))
}
