package sandboxes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
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
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- 假 Orbs：orb 行写进真实的库，调用只记录 ----

type fakeOrbs struct {
	st *store.Store

	mu        sync.Mutex
	calls     []string
	connected map[string]bool
	features  map[string]map[string]bool
	handlers  map[string]http.Handler
	subs      []func(orbs.Event)
	// onCreate 在建出沙箱 orb 之后调用（默认：立即 ready、在线、带 sandbox 与 fs-tar 特性）。
	onCreate func(orbID string)
	netErr   error
}

func (f *fakeOrbs) rec(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeOrbs) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func (f *fakeOrbs) Create(ctx context.Context, p orbs.CreateParams) (db.Orb, error) {
	prov, err := f.st.Q.GetProviderByName(ctx, p.Provider)
	if err != nil {
		return db.Orb{}, err
	}
	id := ids.Orb()
	net := p.SandboxNetwork
	if err := f.st.Q.CreateOrb(ctx, db.CreateOrbParams{
		ID: id, ProviderID: prov.ID, Type: p.Type, Cpus: 2, MemoryMb: 4096, State: orbs.StateRequested,
		StateChangedAt: 1, TokenHash: ids.Hash(id), CreatedAt: time.Now().UnixMilli(), SandboxNetwork: &net,
	}); err != nil {
		return db.Orb{}, err
	}
	f.rec("create:" + p.Provider + "/" + p.Type + "/" + p.SandboxNetwork)
	f.mu.Lock()
	hook := f.onCreate
	f.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	return f.st.Q.GetOrb(ctx, id)
}

func (f *fakeOrbs) Get(ctx context.Context, id string) (db.Orb, error) {
	o, err := f.st.Q.GetOrb(ctx, id)
	if err != nil {
		return o, orbs.ErrNotFound
	}
	return o, nil
}

func (f *fakeOrbs) Delete(ctx context.Context, id string) error {
	f.rec("delete:" + id)
	return f.st.Q.SetOrbState(ctx, db.SetOrbStateParams{State: orbs.StateDeleting, StateChangedAt: 2, ID: id})
}

func (f *fakeOrbs) Connected(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected[id]
}

func (f *fakeOrbs) HasFeature(id, feature string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected[id] && f.features[id][feature]
}

// Do 交给该 orb 登记的 handler，经一台真实的 HTTP 测试服务（流式响应与请求取消都按真实语义）；响应体关闭时关掉服务。
func (f *fakeOrbs) Do(ctx context.Context, id string, req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	h := f.handlers[id]
	f.mu.Unlock()
	if h == nil {
		return nil, errors.New("no orbd")
	}
	srv := httptest.NewServer(h)
	out := req.Clone(ctx)
	u := *req.URL
	u.Scheme, u.Host = "http", strings.TrimPrefix(srv.URL, "http://")
	out.URL, out.RequestURI = &u, ""
	resp, err := srv.Client().Do(out)
	if err != nil {
		go srv.Close()
		return nil, err
	}
	resp.Body = &closeServer{ReadCloser: resp.Body, srv: srv}
	return resp, nil
}

type closeServer struct {
	io.ReadCloser
	srv *httptest.Server
}

func (c *closeServer) Close() error {
	err := c.ReadCloser.Close()
	go c.srv.Close()
	return err
}

func (f *fakeOrbs) Subscribe(fn func(orbs.Event)) {
	f.mu.Lock()
	f.subs = append(f.subs, fn)
	f.mu.Unlock()
}

func (f *fakeOrbs) SetSandboxNetwork(_ context.Context, orbID, network string) error {
	f.rec("net:" + orbID + ":" + network)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.netErr
}

func (f *fakeOrbs) online(id string, feats ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected[id] = true
	m := map[string]bool{}
	for _, x := range feats {
		m[x] = true
	}
	f.features[id] = m
}

func (f *fakeOrbs) handle(id string, h http.Handler) {
	f.mu.Lock()
	f.handlers[id] = h
	f.mu.Unlock()
}

// ---- 夹具 ----

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t       *testing.T
	ctx     context.Context
	st      *store.Store
	orbs    *fakeOrbs
	clk     *clock
	s       *Service
	owner   string // owner orb
	thread  string // owner orb 上的工作线程
	changed []string
	chMu    sync.Mutex
}

const (
	provDocker = "dock"
	provFC     = "fc"
)

func newEnv(t *testing.T, mod func(*Config)) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	e := &env{t: t, ctx: ctx, st: st, clk: &clock{t: time.Now()}}
	e.orbs = &fakeOrbs{st: st, connected: map[string]bool{}, features: map[string]map[string]bool{}, handlers: map[string]http.Handler{}}
	e.orbs.onCreate = func(id string) {
		e.setState(id, orbs.StateReady)
		e.orbs.online(id, orbs.FeatureSandbox, orbs.FeatureFSTar)
	}
	e.provider(provDocker, "docker", true)
	e.provider(provFC, "firecracker", true)
	e.owner = e.orb(provDocker, "")
	e.thread = e.workThread(e.owner)
	e.orbs.online(e.owner, orbs.FeatureFSTar)
	cfg := Config{
		Sandbox: config.Sandbox{Enabled: true, MaxPerOrb: 2, MaxTotal: 8, Idle: config.Duration(30 * time.Minute),
			MaxLifetime: config.Duration(12 * time.Hour), DefaultBackend: "kvm"},
		Now: e.clk.Now, CreateTimeout: 2 * time.Second, LostAfter: 2 * time.Minute,
		Changed: func(id string) {
			e.chMu.Lock()
			e.changed = append(e.changed, id)
			e.chMu.Unlock()
		},
	}
	if mod != nil {
		mod(&cfg)
	}
	e.s = New(cfg, st, e.orbs)
	return e
}

// provider 建一个在线的 provider（sandbox 能力按 sandbox，类型 small / medium，默认 medium）。
func (e *env) provider(name, driver string, sandbox bool) {
	e.t.Helper()
	id := ids.New("prov")
	require.NoError(e.t, e.st.Q.CreateProvider(e.ctx, db.CreateProviderParams{ID: id, Name: name, TokenHash: ids.Hash(id), CreatedAt: 1}))
	caps, _ := json.Marshal(apigen.ProviderCapabilities{Sandbox: &sandbox})
	types, _ := json.Marshal([]apigen.OrbType{{Name: "small", Cpus: 2, MemoryMB: 4096}, {Name: "medium", Cpus: 4, MemoryMB: 8192}})
	_, err := e.st.DB.ExecContext(e.ctx, `UPDATE providers SET driver = ?, capabilities = ?, orb_types = ?, default_type = 'medium',
		instance_id = 'i-1', lease_until = ? WHERE id = ?`, driver, string(caps), string(types), time.Now().Add(24*time.Hour).UnixMilli(), id)
	require.NoError(e.t, err)
}

func (e *env) providerID(name string) string {
	p, err := e.st.Q.GetProviderByName(e.ctx, name)
	require.NoError(e.t, err)
	return p.ID
}

// orb 在 provider 上建一台 ready 的普通 orb；kind 为 "runner" 时建 runner 的 orb。
func (e *env) orb(prov, kind string) string {
	e.t.Helper()
	id := ids.Orb()
	p := db.CreateOrbParams{ID: id, ProviderID: e.providerID(prov), Type: "medium", Cpus: 4, MemoryMb: 8192, State: orbs.StateReady,
		StateChangedAt: 1, TokenHash: ids.Hash(id), CreatedAt: 1}
	if kind != "" {
		p.Kind = &kind
	}
	require.NoError(e.t, e.st.Q.CreateOrb(e.ctx, p))
	return id
}

func (e *env) workThread(orbID string) string {
	e.t.Helper()
	id := "T-" + ids.New("t")
	require.NoError(e.t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{
		ID: id, OrbID: &orbID, Title: "", TitleSource: "auto", State: "idle", SessionDir: "/x", CreatedAt: 1, UpdatedAt: 1, Kind: "work",
	}))
	return id
}

func (e *env) setState(orbID, state string) {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.SetOrbState(e.ctx, db.SetOrbStateParams{State: state, StateChangedAt: 3, ID: orbID}))
}

// call 以 owner orb 的身份调用 op（不经流），返回结果与错误码。
func (e *env) callAs(orbID, threadID, op string, params any) (any, string, []string) {
	e.t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(e.t, err)
	var buf bytes.Buffer
	res, err := e.s.dispatch(e.ctx, orbID, threadID, request{Op: op, Params: raw}, &lineWriter{w: &buf})
	var lines []string
	for l := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	if err != nil {
		return nil, streamErr(err).Code, lines
	}
	return res, "", lines
}

func (e *env) call(op string, params any) (any, string) {
	res, code, _ := e.callAs(e.owner, e.thread, op, params)
	return res, code
}

// create 建一台沙箱并返回其行。
func (e *env) create(params map[string]any) db.Sandbox {
	e.t.Helper()
	res, code := e.call("create", params)
	require.Empty(e.t, code)
	sb := res.(map[string]any)["sandbox"].(apigen.Sandbox)
	row, err := e.st.Q.GetSandbox(e.ctx, sb.ID)
	require.NoError(e.t, err)
	return row
}

func (e *env) row(id string) db.Sandbox {
	e.t.Helper()
	r, err := e.st.Q.GetSandbox(e.ctx, id)
	require.NoError(e.t, err)
	return r
}

// ---- 测试 ----

func TestCallerChecks(t *testing.T) {
	e := newEnv(t, nil)
	other := e.orb(provDocker, "")
	otherThread := e.workThread(other)
	runner := e.orb(provDocker, "runner")
	runnerThread := e.workThread(runner)
	archived := e.workThread(e.owner)
	_, err := e.st.DB.ExecContext(e.ctx, `UPDATE threads SET archived_at = 5 WHERE id = ?`, archived)
	require.NoError(t, err)
	assistant := "T-" + ids.New("t")
	require.NoError(t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{ID: assistant, OrbID: &e.owner, TitleSource: "auto", State: "idle",
		SessionDir: "/x", CreatedAt: 1, UpdatedAt: 1, Kind: "assistant"}))

	for name, c := range map[string]struct{ orb, thread string }{
		"线程不存在":        {e.owner, "T-missing"},
		"冒用别的 orb 的线程": {e.owner, otherThread},
		"已归档":          {e.owner, archived},
		"不是工作线程":       {e.owner, assistant},
		"runner":       {runner, runnerThread},
	} {
		_, code, _ := e.callAs(c.orb, c.thread, "list", map[string]any{})
		assert.Equal(t, CodeForbidden, code, name)
	}
	res, code, _ := e.callAs(e.owner, "T-missing", "status", map[string]any{})
	assert.Empty(t, code, "status 不校验调用方")
	assert.Equal(t, true, res.(map[string]any)["available"])
}

func TestCreateAndOwnership(t *testing.T) {
	e := newEnv(t, nil)
	sb := e.create(map[string]any{"network": "none"})
	assert.Equal(t, stateReady, sb.State)
	assert.Equal(t, "kvm", sb.Backend, "默认优先 kvm")
	assert.Equal(t, provFC, sb.ProviderName)
	assert.Equal(t, "medium", sb.Type)
	assert.Equal(t, int64(1800), sb.IdleSec)
	assert.Nil(t, sb.TtlDeadline)
	assert.Equal(t, []string{"create:fc/medium/none"}, e.orbs.took())
	o, err := e.st.Q.GetOrb(e.ctx, sb.OrbID)
	require.NoError(t, err)
	require.NotNil(t, o.SandboxNetwork)
	assert.Equal(t, "none", *o.SandboxNetwork)

	// 别的 owner orb 看不到它
	other := e.orb(provDocker, "")
	otherThread := e.workThread(other)
	_, code, _ := e.callAs(other, otherThread, "destroy", map[string]any{"sandbox": sb.ID})
	assert.Equal(t, CodeNotFound, code)
	res, code, _ := e.callAs(other, otherThread, "list", map[string]any{})
	require.Empty(t, code)
	assert.Empty(t, res.(map[string]any)["sandboxes"])
	res, _ = e.call("list", map[string]any{})
	assert.Len(t, res.(map[string]any)["sandboxes"], 1)

	// 参数越界
	for _, p := range []map[string]any{
		{"network": "lan"}, {"network": "none", "backend": "lxc"}, {"network": "none", "idleMinutes": 4},
		{"network": "none", "idleMinutes": 241}, {"network": "none", "ttlMinutes": 0}, {"network": "none", "ttlMinutes": 721},
		{"network": "none", "bogus": 1},
	} {
		_, code := e.call("create", p)
		assert.Equal(t, CodeInvalid, code, "%v", p)
	}
}

func TestPick(t *testing.T) {
	e := newEnv(t, nil)
	sb := e.create(map[string]any{"network": "egress", "backend": "docker", "type": "small", "idleMinutes": 10, "ttlMinutes": 60})
	assert.Equal(t, provDocker, sb.ProviderName, "候选里有 owner orb 所在的 provider 就用它")
	assert.Equal(t, "small", sb.Type)
	assert.Equal(t, int64(600), sb.IdleSec)
	require.NotNil(t, sb.TtlDeadline)
	assert.Equal(t, e.clk.Now().Add(time.Hour).UnixMilli(), *sb.TtlDeadline)

	_, code := e.call("create", map[string]any{"network": "none", "type": "huge"})
	assert.Equal(t, CodeInvalidType, code)

	// kvm 不可用时退到 docker；都不可用时 no_sandbox_provider
	_, err := e.st.DB.ExecContext(e.ctx, `UPDATE providers SET lease_until = 1 WHERE name = ?`, provFC)
	require.NoError(t, err)
	sb2 := e.create(map[string]any{"network": "none"})
	assert.Equal(t, "docker", sb2.Backend)
	_, code = e.call("create", map[string]any{"network": "none", "backend": "kvm"})
	assert.Equal(t, CodeQuotaPerOrb, code, "配额先于选 provider")
	require.NoError(t, e.s.Destroy(e.ctx, sb.ID))
	_, code = e.call("create", map[string]any{"network": "none", "backend": "kvm"})
	assert.Equal(t, CodeNoProvider, code)
	res, _ := e.call("status", map[string]any{})
	assert.Equal(t, []string{"docker"}, res.(map[string]any)["backends"])
}

func TestQuotas(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Sandbox.MaxTotal = 3 })
	e.create(map[string]any{"network": "none"})
	e.create(map[string]any{"network": "none"})
	_, code := e.call("create", map[string]any{"network": "none"})
	assert.Equal(t, CodeQuotaPerOrb, code)

	other := e.orb(provDocker, "")
	otherThread := e.workThread(other)
	_, code, _ = e.callAs(other, otherThread, "create", map[string]any{"network": "none"})
	assert.Empty(t, code)
	_, code, _ = e.callAs(other, otherThread, "create", map[string]any{"network": "none"})
	assert.Equal(t, CodeQuotaTotal, code)
}

func TestCreateDisabled(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Sandbox.Enabled = false })
	_, code := e.call("create", map[string]any{"network": "none"})
	assert.Equal(t, CodeDisabled, code)
	res, _ := e.call("status", map[string]any{})
	assert.Equal(t, false, res.(map[string]any)["available"])
}

func TestCreateFailures(t *testing.T) {
	e := newEnv(t, nil)
	e.orbs.onCreate = func(id string) {
		e.setState(id, orbs.StateFailed)
		_, err := e.st.DB.ExecContext(e.ctx, `INSERT INTO orb_events (orb_id, ts, from_state, to_state, detail) VALUES (?, 1, 'spawning', 'failed', ?)`,
			id, `{"reason":"capacity: no free slot"}`)
		require.NoError(t, err)
	}
	_, code := e.call("create", map[string]any{"network": "none"})
	assert.Equal(t, CodeCapacity, code)
	rows, err := e.st.Q.ListSandboxesByThread(e.ctx, db.ListSandboxesByThreadParams{OwnerThreadID: e.thread, Since: new(int64(0))})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, stateFailed, rows[0].State)
	assert.Equal(t, ReasonCreateFailed, *rows[0].DestroyReason)
	assert.Equal(t, "capacity: no free slot", *rows[0].Error)
	assert.Contains(t, e.orbs.took(), "delete:"+rows[0].OrbID)

	// 一直不就绪：超时销毁
	e2 := newEnv(t, func(c *Config) { c.CreateTimeout = 300 * time.Millisecond })
	e2.orbs.onCreate = nil
	_, code = e2.call("create", map[string]any{"network": "none"})
	assert.Equal(t, CodeCreateTimeout, code)
	rows, err = e2.st.Q.ListLiveSandboxes(e2.ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, stateDestroying, rows[0].State)
	assert.Equal(t, ReasonCreateTimeout, *rows[0].DestroyReason)
}

func TestExpiry(t *testing.T) {
	e := newEnv(t, nil)
	jobs := apigen.OrbSandboxJobList{Jobs: []apigen.OrbSandboxJob{}}
	var jmu sync.Mutex
	sbHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jmu.Lock()
		defer jmu.Unlock()
		_ = json.NewEncoder(w).Encode(jobs)
	})

	idle := e.create(map[string]any{"network": "none"})
	e.orbs.handle(idle.OrbID, sbHandler)
	ttl := e.create(map[string]any{"network": "none", "ttlMinutes": 5})

	e.clk.Add(6 * time.Minute)
	e.s.Reconcile(e.ctx)
	assert.Equal(t, ReasonTTL, *e.row(ttl.ID).DestroyReason)
	assert.Equal(t, stateReady, e.row(idle.ID).State)

	// 有在跑的后台作业：不按空闲回收，续上活动时间
	jmu.Lock()
	jobs.Jobs = []apigen.OrbSandboxJob{{ID: "j1", State: apigen.OrbSandboxJobRunning}}
	jmu.Unlock()
	e.clk.Add(30 * time.Minute)
	e.s.Reconcile(e.ctx)
	assert.Equal(t, stateReady, e.row(idle.ID).State)
	assert.Equal(t, e.clk.Now().UnixMilli(), e.row(idle.ID).LastActiveAt)

	// exec 进行中（busy）也不回收
	jmu.Lock()
	jobs.Jobs = nil
	jmu.Unlock()
	e.clk.Add(31 * time.Minute)
	release := e.s.hold(e.ctx, idle.ID)
	e.clk.Add(31 * time.Minute)
	e.s.Reconcile(e.ctx)
	assert.Equal(t, stateReady, e.row(idle.ID).State)
	release()
	e.clk.Add(31 * time.Minute)
	e.s.Reconcile(e.ctx)
	assert.Equal(t, ReasonIdle, *e.row(idle.ID).DestroyReason)

	// 最长寿命
	life := e.create(map[string]any{"network": "none", "idleMinutes": 240})
	e.orbs.handle(life.OrbID, sbHandler)
	for range 4 {
		e.clk.Add(3 * time.Hour)
		e.s.touch(e.ctx, life.ID)
	}
	e.s.Reconcile(e.ctx)
	assert.Equal(t, ReasonMaxLifetime, *e.row(life.ID).DestroyReason)
}

func TestLost(t *testing.T) {
	e := newEnv(t, nil)
	sb := e.create(map[string]any{"network": "none"})
	e.orbs.mu.Lock()
	e.orbs.connected[sb.OrbID] = false
	e.orbs.mu.Unlock()
	e.s.Reconcile(e.ctx)
	e.clk.Add(time.Minute)
	e.s.Reconcile(e.ctx)
	assert.Equal(t, stateReady, e.row(sb.ID).State, "不到 LostAfter")

	// provider 离线期间不计时
	_, err := e.st.DB.ExecContext(e.ctx, `UPDATE providers SET lease_until = 1 WHERE name = ?`, provFC)
	require.NoError(t, err)
	e.clk.Add(5 * time.Minute)
	e.s.Reconcile(e.ctx)
	assert.Equal(t, stateReady, e.row(sb.ID).State)

	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE providers SET lease_until = ? WHERE name = ?`, e.clk.Now().Add(time.Hour).UnixMilli(), provFC)
	require.NoError(t, err)
	e.s.Reconcile(e.ctx)
	e.clk.Add(2 * time.Minute)
	e.s.Reconcile(e.ctx)
	assert.Equal(t, ReasonLost, *e.row(sb.ID).DestroyReason)
}

func TestExtendAndNetwork(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Sandbox.MaxLifetime = config.Duration(time.Hour) })
	sb := e.create(map[string]any{"network": "egress"})
	res, code := e.call("extend", map[string]any{"sandbox": sb.ID, "ttlMinutes": 120})
	require.Empty(t, code)
	got := res.(map[string]any)["sandbox"].(apigen.Sandbox)
	assert.Equal(t, sb.HardDeadline, *got.TTLDeadline, "被最长寿命封顶")
	_, code = e.call("extend", map[string]any{"sandbox": sb.ID, "ttlMinutes": 0})
	assert.Equal(t, CodeInvalid, code)

	res, code = e.call("network", map[string]any{"sandbox": sb.ID, "network": "none"})
	require.Empty(t, code)
	assert.Equal(t, apigen.SandboxNetworkNone, res.(map[string]any)["sandbox"].(apigen.Sandbox).Network)
	assert.Equal(t, "none", e.row(sb.ID).Network)
	_, code = e.call("network", map[string]any{"sandbox": sb.ID, "network": "none"})
	assert.Empty(t, code, "已处于目标状态直接成功")
	assert.Equal(t, []string{"create:fc/medium/egress", "net:" + sb.OrbID + ":none"}, e.orbs.took())

	e.orbs.mu.Lock()
	e.orbs.netErr = errors.New("network_unavailable: docker refused to connect")
	e.orbs.mu.Unlock()
	_, code = e.call("network", map[string]any{"sandbox": sb.ID, "network": "egress"})
	assert.Equal(t, CodeNetworkUnavailable, code)
	assert.Equal(t, "none", e.row(sb.ID).Network)

	none := e.create(map[string]any{"network": "none"})
	_, code = e.call("network", map[string]any{"sandbox": none.ID, "network": "egress"})
	assert.Equal(t, CodeNetworkUnavailable, code, "创建时没有网卡的沙箱不能开网")
}

func TestCascade(t *testing.T) {
	e := newEnv(t, nil)
	a := e.create(map[string]any{"network": "none"})
	b := e.create(map[string]any{"network": "none"})

	// 线程归档
	e.s.ThreadRetired(e.thread, false)
	require.Eventually(t, func() bool { return e.row(a.ID).State == stateDestroying && e.row(b.ID).State == stateDestroying },
		5*time.Second, 10*time.Millisecond)
	assert.Equal(t, ReasonThreadArchived, *e.row(a.ID).DestroyReason)

	// 沙箱 orb 到达 deleted：行置为 destroyed
	o, err := e.st.Q.GetOrb(e.ctx, a.OrbID)
	require.NoError(t, err)
	o.State = orbs.StateDeleted
	e.s.onEvent(e.ctx, orbs.Event{OrbID: a.OrbID, Kind: orbs.EventState, To: orbs.StateDeleted, Orb: o})
	assert.Equal(t, stateDestroyed, e.row(a.ID).State)
	// 对账：orb 已 deleted 的 destroying 行
	e.setState(b.OrbID, orbs.StateDeleted)
	e.s.Reconcile(e.ctx)
	assert.Equal(t, stateDestroyed, e.row(b.ID).State)

	// owner orb 停机：销毁；冻结：不动
	owner2 := e.orb(provDocker, "")
	th2 := e.workThread(owner2)
	e.orbs.online(owner2)
	res, code, _ := e.callAs(owner2, th2, "create", map[string]any{"network": "none"})
	require.Empty(t, code)
	c := res.(map[string]any)["sandbox"].(apigen.Sandbox)
	ownerRow, err := e.st.Q.GetOrb(e.ctx, owner2)
	require.NoError(t, err)
	e.s.onEvent(e.ctx, orbs.Event{OrbID: owner2, Kind: orbs.EventState, To: orbs.StateFrozen, Orb: ownerRow})
	assert.Equal(t, stateReady, e.row(c.ID).State)
	e.s.onEvent(e.ctx, orbs.Event{OrbID: owner2, Kind: orbs.EventState, To: orbs.StateStopped, Orb: ownerRow})
	assert.Equal(t, ReasonOwnerOrbGone, *e.row(c.ID).DestroyReason)

	// 对账兜底：线程已删除
	th3 := e.workThread(owner2)
	e.setState(owner2, orbs.StateReady)
	res, code, _ = e.callAs(owner2, th3, "create", map[string]any{"network": "none"})
	require.Empty(t, code)
	d := res.(map[string]any)["sandbox"].(apigen.Sandbox)
	_, err = e.st.DB.ExecContext(e.ctx, `DELETE FROM threads WHERE id = ?`, th3)
	require.NoError(t, err)
	e.s.Reconcile(e.ctx)
	assert.Equal(t, ReasonThreadDeleted, *e.row(d.ID).DestroyReason)
}

func TestOrphanOrbs(t *testing.T) {
	e := newEnv(t, nil)
	id := ids.Orb()
	net := "none"
	require.NoError(t, e.st.Q.CreateOrb(e.ctx, db.CreateOrbParams{ID: id, ProviderID: e.providerID(provFC), Type: "medium", Cpus: 2,
		MemoryMb: 4096, State: orbs.StateReady, StateChangedAt: 1, TokenHash: ids.Hash(id),
		CreatedAt: e.clk.Now().Add(-2 * time.Minute).UnixMilli(), SandboxNetwork: &net}))
	fresh := ids.Orb()
	require.NoError(t, e.st.Q.CreateOrb(e.ctx, db.CreateOrbParams{ID: fresh, ProviderID: e.providerID(provFC), Type: "medium", Cpus: 2,
		MemoryMb: 4096, State: orbs.StateReady, StateChangedAt: 1, TokenHash: ids.Hash(fresh), CreatedAt: e.clk.Now().UnixMilli(), SandboxNetwork: &net}))
	e.s.Reconcile(e.ctx)
	assert.Equal(t, []string{"delete:" + id}, e.orbs.took(), "宽限期内的不动")
}

// execHandler 是沙箱 orbd 的 /sandbox/exec 替身：回两行输出加退出码；block 时一直挂到请求被取消。
func execHandler(block chan struct{}, gotCancel chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req apigen.OrbSandboxExecRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Detach != nil && *req.Detach {
			_ = json.NewEncoder(w).Encode(apigen.OrbSandboxJobStart{JobID: "j1"})
			return
		}
		fl := w.(http.Flusher)
		_, _ = w.Write([]byte(`{"s":"o","d":"out\n"}` + "\n" + `{"s":"e","d":"err\n"}` + "\n"))
		fl.Flush()
		if block != nil {
			select {
			case <-block:
			case <-r.Context().Done():
				close(gotCancel)
				return
			}
		}
		if req.Command == "drop" {
			return
		}
		_, _ = w.Write([]byte(`{"exit":3}` + "\n"))
	})
}

func TestExec(t *testing.T) {
	e := newEnv(t, nil)
	sb := e.create(map[string]any{"network": "none"})
	e.orbs.handle(sb.OrbID, execHandler(nil, nil))

	res, code, lines := e.callAs(e.owner, e.thread, "exec", map[string]any{"sandbox": sb.ID, "command": "x"})
	require.Empty(t, code)
	assert.Equal(t, []string{`{"s":"o","d":"out\n"}`, `{"s":"e","d":"err\n"}`}, lines)
	assert.Equal(t, 3, res.(map[string]any)["exit"])

	_, code, _ = e.callAs(e.owner, e.thread, "exec", map[string]any{"sandbox": sb.ID, "command": "drop"})
	assert.Equal(t, CodeLinkLost, code, "没等到 exit 行就断了")

	res, code, _ = e.callAs(e.owner, e.thread, "exec", map[string]any{"sandbox": sb.ID, "command": "x", "detach": true})
	require.Empty(t, code)
	assert.Equal(t, "j1", res.(map[string]any)["jobId"])

	for _, p := range []map[string]any{
		{"sandbox": sb.ID, "command": ""},
		{"sandbox": sb.ID, "command": "x", "timeoutSec": 1801},
		{"sandbox": sb.ID, "command": "x", "timeoutSec": 0},
		{"sandbox": sb.ID, "command": "x", "detach": true, "timeoutSec": 86401},
		{"sandbox": sb.ID, "command": "x", "env": map[string]string{"1BAD": "v"}},
	} {
		_, code, _ := e.callAs(e.owner, e.thread, "exec", p)
		assert.Equal(t, CodeInvalid, code, "%v", p)
	}

	e.orbs.mu.Lock()
	e.orbs.connected[sb.OrbID] = false
	e.orbs.mu.Unlock()
	_, code, _ = e.callAs(e.owner, e.thread, "exec", map[string]any{"sandbox": sb.ID, "command": "x"})
	assert.Equal(t, CodeNotReady, code)
}

// sessionPair 建一对经 net.Pipe 相连的 link 会话（客户端 = orbd 侧，服务端 = 控制面侧）。
func sessionPair(t *testing.T) (cli, srv *link.Session) {
	t.Helper()
	a, b := net.Pipe()
	ch := make(chan *link.Session, 1)
	go func() {
		s, err := link.NewConnSession(b, false)
		if err == nil {
			ch <- s
		}
	}()
	cli, err := link.NewConnSession(a, true)
	require.NoError(t, err)
	srv = <-ch
	t.Cleanup(func() { _ = cli.Close(); _ = srv.Close() })
	return cli, srv
}

// TestOnStream 走真实的 sandbox 流：exec 的输出行与结果行；调用方中途关流时沙箱请求被取消。
func TestOnStream(t *testing.T) {
	e := newEnv(t, nil)
	sb := e.create(map[string]any{"network": "none"})
	block, gotCancel := make(chan struct{}), make(chan struct{})
	e.orbs.handle(sb.OrbID, execHandler(block, gotCancel))
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	cli, srv := sessionPair(t)
	go func() {
		for {
			st, err := srv.AcceptStream(ctx)
			if err != nil {
				return
			}
			go func() {
				defer st.Close()
				e.s.OnStream(ctx, e.owner, st.Header, st)
			}()
		}
	}()

	open := func() *link.Stream {
		st, err := cli.Open(ctx, link.StreamHeader{T: link.StreamSandbox, ThreadID: e.thread})
		require.NoError(t, err)
		req, _ := json.Marshal(map[string]any{"op": "exec", "params": map[string]any{"sandbox": sb.ID, "command": "x"}})
		_, err = st.Write(append(req, '\n'))
		require.NoError(t, err)
		return st
	}

	st := open()
	var got []string
	for range 2 {
		l, err := st.ReadLine(1 << 20)
		require.NoError(t, err)
		got = append(got, strings.TrimSpace(string(l)))
	}
	close(block)
	l, err := st.ReadLine(1 << 20)
	require.NoError(t, err)
	var final struct {
		Result struct {
			Exit       int   `json:"exit"`
			DurationMs int64 `json:"durationMs"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(l, &final))
	assert.Equal(t, []string{`{"s":"o","d":"out\n"}`, `{"s":"e","d":"err\n"}`}, got)
	assert.Equal(t, 3, final.Result.Exit)
	_ = st.Close()

	// 中途关流 = 放弃：沙箱上的请求被取消
	block2, gotCancel2 := make(chan struct{}), make(chan struct{})
	e.orbs.handle(sb.OrbID, execHandler(block2, gotCancel2))
	st = open()
	_, err = st.ReadLine(1 << 20)
	require.NoError(t, err)
	_ = st.Stream.Close()
	select {
	case <-gotCancel2:
	case <-time.After(5 * time.Second):
		t.Fatal("关流后沙箱上的 exec 没有被取消")
	}
	_ = gotCancel

	// 错误行
	st, err = cli.Open(ctx, link.StreamHeader{T: link.StreamSandbox, ThreadID: "T-missing"})
	require.NoError(t, err)
	_, err = st.Write([]byte(`{"op":"list","params":{}}` + "\n"))
	require.NoError(t, err)
	l, err = st.ReadLine(1 << 20)
	require.NoError(t, err)
	assert.JSONEq(t, `{"error":{"code":"forbidden","message":"calling thread not found"}}`, string(l))
}

// TestCp 经两端的 /fs/tar 替身流式中转，结果与错误码透传。
func TestCp(t *testing.T) {
	e := newEnv(t, nil)
	sb := e.create(map[string]any{"network": "none"})
	var gotQuery, putQuery string
	var putBody []byte
	e.orbs.handle(e.owner, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if r.URL.Query().Get("path") == "/home/user/missing" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found","message":"no such file"}`))
			return
		}
		w.Header().Set("X-Oryn-Tar-Kind", "dir")
		_, _ = w.Write([]byte("TARDATA"))
	}))
	e.orbs.handle(sb.OrbID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		putQuery = r.URL.RawQuery
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		putBody = buf.Bytes()
		_ = json.NewEncoder(w).Encode(apigen.OrbFsTarResult{Files: 2, Bytes: 7, Skipped: 1})
	}))
	res, code := e.call("cp", map[string]any{"sandbox": sb.ID, "direction": "to_sandbox", "source": "/home/user/src", "dest": "proj",
		"exclude": []string{"*.log"}, "gitignore": true})
	require.Empty(t, code)
	assert.Equal(t, map[string]any{"files": 2, "bytes": int64(7), "skipped": 1}, res)
	assert.Equal(t, "exclude=%2A.log&gitignore=1&path=%2Fhome%2Fuser%2Fsrc", gotQuery)
	assert.Equal(t, "kind=dir&path=proj", putQuery)
	assert.Equal(t, "TARDATA", string(putBody))

	_, code = e.call("cp", map[string]any{"sandbox": sb.ID, "direction": "to_sandbox", "source": "/home/user/missing", "dest": "x"})
	assert.Equal(t, "not_found", code, "orbd 的错误码原样透传")
	_, code = e.call("cp", map[string]any{"sandbox": sb.ID, "direction": "sideways", "source": "a", "dest": "b"})
	assert.Equal(t, CodeInvalid, code)

	e.orbs.online(e.owner) // 去掉 fs-tar
	_, code = e.call("cp", map[string]any{"sandbox": sb.ID, "direction": "to_sandbox", "source": "/home/user/src", "dest": "proj"})
	assert.Equal(t, CodeOrbdOutdated, code)
}
