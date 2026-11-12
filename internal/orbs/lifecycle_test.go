package orbs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/providerrt"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- 进程内假 orbd ----

type ctlCall struct {
	method string
	params json.RawMessage
}

type fakeOrbd struct {
	url, orbID, token string

	mu    sync.Mutex
	sess  *link.Session
	ctl   *link.Ctl
	calls []ctlCall
}

func (o *fakeOrbd) connect() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := link.Dial(ctx, link.URL(o.url), o.token)
	if err != nil {
		return err
	}
	st, err := sess.Open(ctx, link.StreamHeader{T: link.StreamCtl})
	if err != nil {
		_ = sess.Close()
		return err
	}
	ctl := link.NewCtl(st, link.CtlHandlers{OnRequest: func(_ context.Context, method string, params json.RawMessage) (any, error) {
		o.mu.Lock()
		o.calls = append(o.calls, ctlCall{method, params})
		o.mu.Unlock()
		switch method {
		case "setup.run", "resume.run":
			return apigen.LinkCtlSetupRunResult{Ok: true, SetupRan: method == "setup.run", DurationMs: 3}, nil
		case "shutdown":
			return apigen.LinkCtlOkResult{Ok: true}, nil
		}
		return nil, link.ErrMethodNotFound
	}})
	o.mu.Lock()
	o.sess, o.ctl = sess, ctl
	o.mu.Unlock()
	if err := ctl.Notify("hello", apigen.LinkCtlHelloParams{OrbdVersion: "fake-1", AgentVersion: "omp/test", Os: "linux", Arch: "amd64", Threads: []apigen.LinkCtlHelloThread{}}); err != nil {
		return err
	}
	return ctl.Notify("metrics.sample", apigen.OrbMetricSample{TS: time.Now().UnixMilli(), CPUPct: 12.5, MemUsedMB: 100, MemLimitMB: 4096, DiskUsedMB: 10, NetRxBytes: 1, NetTxBytes: 2})
}

func (o *fakeOrbd) disconnect() {
	o.mu.Lock()
	sess := o.sess
	o.sess, o.ctl = nil, nil
	o.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

func (o *fakeOrbd) methods() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, len(o.calls))
	for _, c := range o.calls {
		out = append(out, c.method)
	}
	return out
}

func (o *fakeOrbd) call(method string) (json.RawMessage, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, c := range o.calls {
		if c.method == method {
			return c.params, true
		}
	}
	return nil, false
}

// ---- 假驱动：spawn 拉起 / 恢复假 orbd，freeze / stop 断开 link ----

type fakeDriver struct {
	mu        sync.Mutex
	orbds     map[string]*fakeOrbd
	noConnect bool
	ops       []string
}

func newFakeDriver() *fakeDriver { return &fakeDriver{orbds: map[string]*fakeOrbd{}} }

func (d *fakeDriver) Info() providerrt.Info {
	return providerrt.Info{
		Driver: apigen.Process, Capabilities: apigen.ProviderCapabilities{Freeze: true, Snapshot: true},
		Types: []apigen.OrbType{{Name: "small", Cpus: 2, MemoryMB: 4096}}, DefaultType: "small",
	}
}

func (d *fakeDriver) orbd(id string) *fakeOrbd {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.orbds[id]
}

func (d *fakeDriver) Exec(_ context.Context, j providerrt.Job, _ io.Writer) error {
	d.mu.Lock()
	d.ops = append(d.ops, j.Op+":"+j.PauseMode)
	o := d.orbds[j.OrbID]
	noConnect := d.noConnect
	d.mu.Unlock()
	switch j.Op {
	case "spawn":
		if o == nil {
			o = &fakeOrbd{url: j.Spawn.Env.OrynURL, orbID: j.OrbID, token: j.Spawn.Env.OrynOrbToken}
			d.mu.Lock()
			d.orbds[j.OrbID] = o
			d.mu.Unlock()
		}
		if noConnect {
			return nil
		}
		o.mu.Lock()
		up := o.sess != nil && !o.sess.IsClosed()
		o.mu.Unlock()
		if !up {
			return o.connect()
		}
	case "pause", "delete":
		if o != nil {
			o.disconnect()
		}
	}
	return nil
}

// ---- 夹具 ----

type fakeSetup struct{ upstream string }

func (f fakeSetup) Plan(context.Context, db.Orb) (SetupPlan, error) {
	return SetupPlan{
		Params: apigen.LinkCtlSetupRunParams{Env: map[string]string{"A": "1"}, GitHosts: []string{}, OmpHome: []apigen.LinkCtlOmpHomeFile{}},
		Routes: map[string]string{"anth": f.upstream},
	}, nil
}

type fakeSecrets struct{}

func (fakeSecrets) SecretByID(id string) (string, []string, bool) {
	if id == "sid1" {
		return "real-key", nil, true
	}
	return "", nil, false
}
func (fakeSecrets) GitToken(string) (string, bool) { return "", false }

type harness struct {
	t      *testing.T
	st     *store.Store
	q      *jobs.Queue
	svc    *Service
	srv    *httptest.Server
	provID string
	quiet  atomic.Bool

	evMu   sync.Mutex
	events []Event
}

func newHarness(t *testing.T, drv providerrt.Driver, setup SetupSource, mod func(*Config)) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	h := &harness{t: t, st: st, q: jobs.New(st)}
	var handler atomic.Value
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.Load().(http.Handler).ServeHTTP(w, r)
	}))
	cfg := Config{
		Idle:      config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(time.Hour)},
		PublicURL: h.srv.URL, TokenKey: []byte("0123456789abcdef0123456789abcdef"),
		EvalInterval: 100 * time.Millisecond, ConnectTimeout: 10 * time.Second, ShutdownGrace: 1,
	}
	if mod != nil {
		mod(&cfg)
	}
	h.svc = New(cfg, st, h.q, setup, fakeSecrets{})
	h.quiet.Store(true)
	h.svc.SetQuiescence(func(string) bool { return h.quiet.Load() })
	h.svc.Subscribe(func(ev Event) {
		h.evMu.Lock()
		h.events = append(h.events, ev)
		h.evMu.Unlock()
	})
	r := chi.NewRouter()
	r.Handle("/provider/v1/*", h.q.Handler())
	r.Handle(link.Path, h.svc.LinkHandler())
	handler.Store(http.Handler(r))

	tok := ids.Token("opv")
	h.provID = ids.New("prov")
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: h.provID, Name: "local", TokenHash: ids.Hash(tok), CreatedAt: 1}))
	runCtx, cancel := context.WithCancel(ctx)
	svcCtx, svcCancel := context.WithCancel(ctx)
	runner := &providerrt.Runner{URL: h.srv.URL, Token: tok, Driver: drv, ClaimWait: time.Second, ShutdownGrace: time.Second}
	done := make(chan struct{})
	go func() { _ = runner.Run(runCtx); close(done) }()
	go h.svc.Run(svcCtx)
	go h.q.Run(svcCtx)
	// 等 provider hello 完成（Create 需要 provider 的类型表）。
	require.Eventually(t, func() bool {
		p, err := st.Q.GetProvider(ctx, h.provID)
		return err == nil && p.DefaultType != ""
	}, 5*time.Second, 20*time.Millisecond)
	t.Cleanup(func() {
		cancel()
		<-done
		svcCancel()
		h.svc.Close()
		h.srv.Close()
		_ = st.Close()
	})
	return h
}

func (h *harness) waitState(orbID, state string, within time.Duration) db.Orb {
	h.t.Helper()
	var orb db.Orb
	require.Eventually(h.t, func() bool {
		var err error
		orb, err = h.st.Q.GetOrb(context.Background(), orbID)
		return err == nil && orb.State == state
	}, within, 20*time.Millisecond, "orb %s did not reach %s (now %s)", orbID, state, orb.State)
	return orb
}

func (h *harness) eventSeq(orbID string) []string {
	evs, err := h.st.Q.ListOrbEvents(context.Background(), orbID)
	require.NoError(h.t, err)
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.FromState+">"+e.ToState)
	}
	return out
}

// ---- 测试 ----

func TestLifecycle(t *testing.T) {
	var upstreamKey atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamKey.Store(r.Header.Get("X-Api-Key") + " " + r.URL.Path)
		_, _ = io.WriteString(w, "upstream-ok")
	}))
	defer upstream.Close()

	drv := newFakeDriver()
	h := newHarness(t, drv, fakeSetup{upstream: upstream.URL}, func(c *Config) {
		c.Idle.AgentIdle = config.Duration(2 * time.Second)
		c.Idle.UserIdle = config.Duration(2 * time.Second)
	})
	h.quiet.Store(false) // 先阻止冻结，做完 egress 检查再放开
	ctx := context.Background()

	orb, err := h.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	assert.Equal(t, "small", orb.Type)
	assert.EqualValues(t, 2, orb.Cpus)
	assert.EqualValues(t, 4096, orb.MemoryMb)
	h.waitState(orb.ID, StateReady, 10*time.Second)
	assert.True(t, h.svc.Connected(orb.ID))

	o := drv.orbd(orb.ID)
	require.NotNil(t, o)
	raw, ok := o.call("setup.run")
	require.True(t, ok)
	var sp apigen.LinkCtlSetupRunParams
	require.NoError(t, json.Unmarshal(raw, &sp))
	assert.Equal(t, map[string]string{"A": "1"}, sp.Env)
	assert.Equal(t, "", sp.Workdir)

	got, err := h.st.Q.GetOrb(ctx, orb.ID)
	require.NoError(t, err)
	assert.Equal(t, "fake-1", *got.OrbdVersion)
	samples, err := h.st.Q.ListOrbSamples(ctx, db.ListOrbSamplesParams{OrbID: orb.ID, FromTs: 0, ToTs: time.Now().UnixMilli() + 1000})
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.InDelta(t, 12.5, samples[0].CpuPct, 0.001)

	// egress：占位符在控制面被替换成真实密钥。
	o.mu.Lock()
	sess := o.sess
	o.mu.Unlock()
	est, err := sess.Open(ctx, link.StreamHeader{T: link.StreamEgress})
	require.NoError(t, err)
	_, err = io.WriteString(est, "GET /llm/anth/v1/messages HTTP/1.1\r\nHost: 127.0.0.1:7811\r\nX-Api-Key: oryn-ph-sid1\r\nConnection: close\r\n\r\n")
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(est), nil)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	_ = est.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "upstream-ok", string(body))
	assert.Equal(t, "real-key /v1/messages", upstreamKey.Load())

	// 空闲 2s → frozen。
	h.quiet.Store(true)
	h.waitState(orb.ID, StateFrozen, 10*time.Second)

	// 唤醒 → ready（走 resume）。
	require.NoError(t, h.svc.Wake(ctx, orb.ID))
	h.svc.TouchUser(ctx, orb.ID) // 交互后不会立刻再冻结
	h.waitState(orb.ID, StateReady, 10*time.Second)
	assert.Contains(t, o.methods(), "resume.run")
	h.quiet.Store(false)

	// 删除 → deleted；先 shutdown。
	require.NoError(t, h.svc.Delete(ctx, orb.ID))
	h.waitState(orb.ID, StateDeleted, 10*time.Second)
	assert.Equal(t, []string{"setup.run", "resume.run", "shutdown"}, o.methods())
	got, err = h.st.Q.GetOrb(ctx, orb.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.DeletedAt)
	require.Eventually(t, func() bool { return !h.svc.Connected(orb.ID) }, 5*time.Second, 20*time.Millisecond)

	assert.Equal(t, []string{
		">requested", "requested>spawning", "spawning>connecting", "connecting>setting_up", "setting_up>ready",
		"ready>freezing", "freezing>frozen",
		"frozen>waking", "waking>connecting", "connecting>resuming", "resuming>ready",
		"ready>deleting", "deleting>deleted",
	}, h.eventSeq(orb.ID))

	h.evMu.Lock()
	var kinds []string
	for _, ev := range h.events {
		if ev.OrbID == orb.ID && ev.Kind != EventState {
			kinds = append(kinds, ev.Kind)
		}
	}
	h.evMu.Unlock()
	assert.Contains(t, kinds, EventLinkUp)
	assert.Contains(t, kinds, EventLinkDown)

	// 删除后的 orb 令牌不能再建立 link。
	assert.Error(t, o.connect())
}

func TestStopSnapshotAndConnectTimeout(t *testing.T) {
	drv := newFakeDriver()
	h := newHarness(t, drv, nil, func(c *Config) { c.ConnectTimeout = 500 * time.Millisecond })
	ctx := context.Background()

	orb, err := h.svc.Create(ctx, CreateParams{Provider: "local", Type: "small"})
	require.NoError(t, err)
	h.waitState(orb.ID, StateReady, 10*time.Second)

	snap, err := h.svc.Snapshot(ctx, orb.ID)
	require.NoError(t, err)
	assert.Regexp(t, `^oryn-snap-[0-9a-z]{26}$`, snap)

	// 手动停机：shutdown → pause{stop} → stopped；再唤醒走 resume。
	require.NoError(t, h.svc.Pause(ctx, orb.ID, PauseStop))
	h.waitState(orb.ID, StateStopped, 10*time.Second)
	require.ErrorIs(t, h.svc.Pause(ctx, orb.ID, PauseFreeze), ErrInvalidState)
	require.NoError(t, h.svc.Wake(ctx, orb.ID))
	h.waitState(orb.ID, StateReady, 10*time.Second)
	assert.Equal(t, []string{"setup.run", "shutdown", "resume.run"}, drv.orbd(orb.ID).methods())

	// 冻结后直接停机：先 spawn 解冻，link 恢复后 shutdown，再 stop。
	require.NoError(t, h.svc.Pause(ctx, orb.ID, PauseFreeze))
	h.waitState(orb.ID, StateFrozen, 10*time.Second)
	require.NoError(t, h.svc.Pause(ctx, orb.ID, PauseStop))
	h.waitState(orb.ID, StateStopped, 10*time.Second)
	assert.Equal(t, []string{"setup.run", "shutdown", "resume.run", "shutdown"}, drv.orbd(orb.ID).methods())
	assert.Equal(t, []string{
		">requested", "requested>spawning", "spawning>connecting", "connecting>setting_up", "setting_up>ready",
		"ready>stopping", "stopping>stopped",
		"stopped>waking", "waking>connecting", "connecting>resuming", "resuming>ready",
		"ready>freezing", "freezing>frozen", "frozen>stopping", "stopping>stopped",
	}, h.eventSeq(orb.ID))

	// 快照派生的 orb 沿用源 orb 的 provider 与类型，spawn 带 snapshotId。
	clone, err := h.svc.Create(ctx, CreateParams{SnapshotOf: orb.ID, SnapshotID: snap})
	require.NoError(t, err)
	assert.Equal(t, snap, *clone.SnapshotOf)
	h.waitState(clone.ID, StateReady, 10*time.Second)
	jl, err := h.st.Q.ListJobsByOrb(ctx, &clone.ID)
	require.NoError(t, err)
	require.NotEmpty(t, jl)
	assert.Contains(t, jl[0].Params, snap)
	assert.NotContains(t, jl[0].Params, "oot_", "orb token scrubbed from finished spawn job")

	// 无 hello → connecting 超时 → failed。
	drv.mu.Lock()
	drv.noConnect = true
	drv.mu.Unlock()
	lost, err := h.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	h.waitState(lost.ID, StateFailed, 10*time.Second)
	assert.Equal(t, []string{">requested", "requested>spawning", "spawning>connecting", "connecting>failed"}, h.eventSeq(lost.ID))

	_, err = h.svc.Create(ctx, CreateParams{Provider: "nope"})
	assert.ErrorIs(t, err, ErrNoProvider)
	_, err = h.svc.Create(ctx, CreateParams{Type: "huge"})
	assert.ErrorIs(t, err, ErrBadType)
}

// TestLifecycleRealOrbd 在设置 ORYN_TEST_ORBD=<orbd 可执行文件> 时，用 process 驱动跑真实 orbd。
func TestLifecycleRealOrbd(t *testing.T) {
	bin := os.Getenv("ORYN_TEST_ORBD")
	if bin == "" {
		t.Skip("未设置 ORYN_TEST_ORBD")
	}
	abs, err := filepath.Abs(bin)
	require.NoError(t, err)
	drv := providerrt.NewProcess(t.TempDir(), abs, config.Docker{})
	drv.StopTimeout = 10 * time.Second
	t.Cleanup(drv.Close)
	h := newHarness(t, drv, &DefaultSetup{}, func(c *Config) {
		c.Idle.AgentIdle = config.Duration(2 * time.Second)
		c.Idle.UserIdle = config.Duration(2 * time.Second)
		c.ConnectTimeout = 30 * time.Second
	})
	ctx := context.Background()
	orb, err := h.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	h.waitState(orb.ID, StateReady, 60*time.Second)
	h.waitState(orb.ID, StateFrozen, 30*time.Second)
	require.NoError(t, h.svc.Wake(ctx, orb.ID))
	h.svc.TouchUser(ctx, orb.ID)
	h.waitState(orb.ID, StateReady, 60*time.Second)
	h.quiet.Store(false)
	require.NoError(t, h.svc.Delete(ctx, orb.ID))
	h.waitState(orb.ID, StateDeleted, 60*time.Second)
	assert.Equal(t, []string{
		">requested", "requested>spawning", "spawning>connecting", "connecting>setting_up", "setting_up>ready",
		"ready>freezing", "freezing>frozen",
		"frozen>waking", "waking>connecting", "connecting>resuming", "resuming>ready",
		"ready>deleting", "deleting>deleted",
	}, h.eventSeq(orb.ID), fmt.Sprintf("orbd log: %s", filepath.Join(drv.Root, "orbs", orb.ID, "orbd.log")))
}
