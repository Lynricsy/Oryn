package boxes

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- 假 Orbs：orb 行写进真实的库，调用只记录 ----

type fakeOrbs struct {
	st     *store.Store
	provID string

	mu        sync.Mutex
	calls     []string // "op:orbID"
	connected map[string]bool
	deleteErr error
	gate      func(string) bool
	subs      []func(orbs.Event)
}

func (f *fakeOrbs) rec(op, id string) {
	f.mu.Lock()
	f.calls = append(f.calls, op+":"+id)
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
	if !p.Box {
		return db.Orb{}, errors.New("not a box")
	}
	if p.Provider == "nope" {
		return db.Orb{}, errors.New("orbs: no provider: nope")
	}
	id := ids.Orb()
	if err := f.st.Q.CreateOrb(ctx, db.CreateOrbParams{
		ID: id, ProviderID: f.provID, Type: "medium", Cpus: 4, MemoryMb: 8192, State: orbs.StateRequested,
		StateChangedAt: 1, TokenHash: ids.Hash(id), CreatedAt: 1, Box: new(int64(1)),
	}); err != nil {
		return db.Orb{}, err
	}
	f.rec("create", id)
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
	f.mu.Lock()
	err := f.deleteErr
	f.mu.Unlock()
	f.rec("delete", id)
	if err != nil {
		return err
	}
	return f.st.Q.SetOrbState(ctx, db.SetOrbStateParams{State: orbs.StateDeleting, StateChangedAt: 2, ID: id})
}

func (f *fakeOrbs) Wake(_ context.Context, id string) error { f.rec("wake", id); return nil }
func (f *fakeOrbs) Pause(_ context.Context, id, mode string) error {
	f.rec("pause-"+mode, id)
	return nil
}
func (f *fakeOrbs) Revive(_ context.Context, id string) error { f.rec("revive", id); return nil }
func (f *fakeOrbs) Connected(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected[id]
}
func (f *fakeOrbs) HasFeature(string, string) bool { return false }
func (f *fakeOrbs) Do(context.Context, string, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrbs) DialPortal(context.Context, string, int) (net.Conn, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeOrbs) Subscribe(fn func(orbs.Event)) {
	f.mu.Lock()
	f.subs = append(f.subs, fn)
	f.mu.Unlock()
}
func (f *fakeOrbs) SetBoxGate(fn func(string) bool) {
	f.mu.Lock()
	f.gate = fn
	f.mu.Unlock()
}

type fakeThreads struct {
	mu   sync.Mutex
	busy bool
}

func (t *fakeThreads) Busy(context.Context, string) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.busy, nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t    *testing.T
	ctx  context.Context
	st   *store.Store
	orbs *fakeOrbs
	th   *fakeThreads
	clk  *clock
	s    *Service
}

func newEnv(t *testing.T, start time.Time) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	provID := ids.New("prov")
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: provID, Name: "docker", TokenHash: ids.Hash(provID), CreatedAt: 1}))
	o := &fakeOrbs{st: st, provID: provID, connected: map[string]bool{}}
	th := &fakeThreads{}
	clk := &clock{t: start}
	s := New(Config{Now: clk.Now}, st, o, th)
	return &env{t: t, ctx: ctx, st: st, orbs: o, th: th, clk: clk, s: s}
}

// box 建一台 box，并把它的 orb 置为 state。
func (e *env) box(name, state string) apigen.Box {
	e.t.Helper()
	b, err := e.s.Create(e.ctx, name, "", "", Actor{Kind: ActorOwner})
	require.NoError(e.t, err)
	e.setState(b.OrbID, state)
	e.orbs.took()
	return b
}

func (e *env) setState(orbID, state string) {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.SetOrbState(e.ctx, db.SetOrbStateParams{State: state, StateChangedAt: e.clk.Now().UnixMilli(), ID: orbID}))
}

func (e *env) row(name string) db.Box {
	e.t.Helper()
	b, err := e.st.Q.GetBoxByName(e.ctx, name)
	require.NoError(e.t, err)
	return b
}

func codeOf(err error) string {
	var be *Error
	if errors.As(err, &be) {
		return be.Code
	}
	return ""
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// ---- 测试 ----

func TestValidName(t *testing.T) {
	for name, ok := range map[string]bool{
		"b1": true, "workbench": true, "a": true, "a-b": true, "abcdefghijklmnopqrstuvwxyz0123": true,
		"": false, "-a": false, "a-": false, "a--b": false, "A": false, "box_x": false, "a.b": false,
		"abcdefghijklmnopqrstuvwxyz01234": false,
	} {
		assert.Equal(t, ok, ValidName(name), name)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t, at("2026-11-20T12:00:00Z"))
	owner := Actor{Kind: ActorOwner}
	_, err := e.s.Create(e.ctx, "Bad", "", "", owner)
	assert.Equal(t, CodeInvalid, codeOf(err))
	_, err = e.s.Create(e.ctx, "b1", "nope", "", owner)
	assert.Equal(t, CodeInvalid, codeOf(err), "orbs.Create 的错误包成 invalid_request")

	b := e.box("b1", orbs.StateReady)
	assert.Equal(t, "b1", b.Name)
	assert.Equal(t, apigen.BoxDesiredRunning, b.Desired)
	assert.Equal(t, "docker", b.Provider)
	assert.Equal(t, "medium", b.Type)
	assert.Empty(t, b.Apps)
	_, err = e.s.Create(e.ctx, "b1", "", "", owner)
	assert.Equal(t, CodeBoxExists, codeOf(err))

	for i := 2; i <= MaxBoxes; i++ {
		_, err := e.s.Create(e.ctx, "b"+string(rune('a'+i)), "", "", owner)
		require.NoError(t, err)
	}
	_, err = e.s.Create(e.ctx, "extra", "", "", owner)
	assert.Equal(t, CodeBoxLimit, codeOf(err))

	got, err := e.s.Get(e.ctx, "b1")
	require.NoError(t, err)
	assert.Equal(t, apigen.BoxStateRunning, got.State)
	byID, err := e.s.Get(e.ctx, got.ID)
	require.NoError(t, err)
	assert.Equal(t, "b1", byID.Name)
	_, err = e.s.Get(e.ctx, "missing")
	assert.Equal(t, CodeBoxNotFound, codeOf(err))
}

func TestStopStartGateAndActivity(t *testing.T) {
	e := newEnv(t, at("2026-11-20T12:00:00Z"))
	b := e.box("b1", orbs.StateReady)
	e.orbs.mu.Lock()
	gate := e.orbs.gate
	e.orbs.connected[b.OrbID] = true
	e.orbs.mu.Unlock()
	require.NotNil(t, gate, "New 时注册闸门")
	assert.True(t, gate(b.OrbID))
	assert.True(t, gate("orb-unknown"), "不是 box → 放行")

	e.clk.Add(time.Minute)
	v, err := e.s.Stop(e.ctx, "b1", Actor{Kind: ActorOwner})
	require.NoError(t, err)
	assert.Equal(t, apigen.BoxDesiredStopped, v.Desired)
	require.NotNil(t, v.StoppedBy)
	assert.Equal(t, apigen.BoxStoppedByOwner, *v.StoppedBy)
	assert.False(t, gate(b.OrbID))
	e.s.reconcile(e.ctx)
	assert.Equal(t, []string{"pause-stop:" + b.OrbID}, e.orbs.took())

	e.setState(b.OrbID, orbs.StateStopped)
	e.clk.Add(time.Minute)
	v, err = e.s.Start(e.ctx, b.ID, Actor{Kind: ActorAgent, ThreadID: "T-1"})
	require.NoError(t, err)
	assert.Nil(t, v.StoppedBy)
	assert.True(t, gate(b.OrbID))
	e.s.reconcile(e.ctx)
	assert.Equal(t, []string{"wake:" + b.OrbID}, e.orbs.took())

	_, err = e.s.Rename(e.ctx, "b1", "b2", Actor{Kind: ActorOwner})
	require.NoError(t, err)
	_, err = e.s.Rename(e.ctx, "b2", "Bad", Actor{Kind: ActorOwner})
	assert.Equal(t, CodeInvalid, codeOf(err))

	// box 上的一个工作线程（app 目录）也进活动流
	cwd := "~/apps/wiki/current"
	tid := ids.Thread()
	require.NoError(t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{
		Kind: "work", ID: tid, OrbID: &b.OrbID, Title: "fix wiki", TitleSource: "auto", State: "idle", SessionDir: "/s",
		CreatedAt: e.clk.Now().Add(30 * time.Second).UnixMilli(), UpdatedAt: 1, Cwd: &cwd,
	}))
	items, err := e.s.Activity(e.ctx, "b2", 50)
	require.NoError(t, err)
	kinds := []apigen.BoxActivityKind{}
	for _, it := range items {
		kinds = append(kinds, it.Kind)
		assert.Equal(t, "b2", it.BoxName)
	}
	assert.Equal(t, []apigen.BoxActivityKind{
		apigen.BoxActivityThread, apigen.BoxActivityRenamed, apigen.BoxActivityStarted, apigen.BoxActivityStopped, apigen.BoxActivityCreated,
	}, kinds)
	assert.Equal(t, "t:"+tid, items[0].ID)
	require.NotNil(t, items[0].App)
	assert.Equal(t, "wiki", *items[0].App)
	require.NotNil(t, items[1].From)
	assert.Equal(t, "b1", *items[1].From)
	assert.Equal(t, "b2", *items[1].To)
	assert.Equal(t, apigen.BoxActorAgent, items[2].Actor)
	require.NotNil(t, items[2].ThreadID)
	assert.Equal(t, "T-1", *items[2].ThreadID)

	all, err := e.s.Activity(e.ctx, "", 2)
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestLastBoundary(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	cases := []struct {
		name, now, on, off string
		loc                *time.Location
		want, desired      string
	}{
		{"白天", "2026-11-20T12:00:00Z", "05:00", "23:00", time.UTC, "2026-11-20T05:00:00Z", desiredRunning},
		{"开机之前", "2026-11-20T04:59:00Z", "05:00", "23:00", time.UTC, "2026-11-19T23:00:00Z", desiredStopped},
		{"正好关机", "2026-11-20T23:00:00Z", "05:00", "23:00", time.UTC, "2026-11-20T23:00:00Z", desiredStopped},
		{"跨夜：夜里", "2026-11-20T02:00:00Z", "22:00", "06:00", time.UTC, "2026-11-19T22:00:00Z", desiredRunning},
		{"跨夜：白天", "2026-11-20T12:00:00Z", "22:00", "06:00", time.UTC, "2026-11-20T06:00:00Z", desiredStopped},
		{"跨夜：开机之后", "2026-11-20T22:30:00Z", "22:00", "06:00", time.UTC, "2026-11-20T22:00:00Z", desiredRunning},
		// 2026-03-29 柏林 02:00 拨快到 03:00（CET → CEST）
		{"夏令时开始当天", "2026-03-29T04:30:00Z", "05:00", "23:00", berlin, "2026-03-29T03:00:00Z", desiredRunning},
		{"夏令时开始前夜", "2026-03-29T00:30:00Z", "05:00", "23:00", berlin, "2026-03-28T22:00:00Z", desiredStopped},
		// 2026-10-25 柏林 03:00 拨回 02:00（CEST → CET）
		{"夏令时结束当天", "2026-10-25T05:30:00Z", "05:00", "23:00", berlin, "2026-10-25T04:00:00Z", desiredRunning},
		{"夏令时结束前夜", "2026-10-25T03:30:00Z", "05:00", "23:00", berlin, "2026-10-24T21:00:00Z", desiredStopped},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, desired, ok := lastBoundary(at(c.now), c.on, c.off, c.loc)
			require.True(t, ok)
			assert.Equal(t, at(c.want), got.UTC(), "boundary")
			assert.Equal(t, c.desired, desired)
		})
	}
}

// 对账拿到旧快照、写入之前插进一次手动启动或清除时段：时段的判定在事务里复核，保留手动的结果，也不多记 schedule 活动。
func TestScheduleDoesNotOverrideConcurrentChange(t *testing.T) {
	for name, interject := range map[string]func(e *env) error{
		"manual start": func(e *env) error {
			_, err := e.s.Start(e.ctx, "b1", Actor{Kind: ActorOwner})
			return err
		},
		"clear schedule": func(e *env) error {
			_, err := e.s.SetSchedule(e.ctx, "b1", nil, Actor{Kind: ActorOwner})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, at("2026-11-20T12:00:00Z"))
			b := e.box("b1", orbs.StateReady)
			e.orbs.connected[b.OrbID] = true
			_, err := e.s.SetSchedule(e.ctx, "b1", &apigen.BoxSchedule{On: "05:00", Off: "23:00", Tz: "UTC"}, Actor{Kind: ActorOwner})
			require.NoError(t, err)
			e.clk.Set(at("2026-11-20T23:00:10Z"))
			fired := false
			e.s.hookBeforeScheduleTx = func() {
				if !fired {
					fired = true
					require.NoError(t, interject(e))
				}
			}
			e.s.reconcile(e.ctx)
			require.True(t, fired)
			r := e.row("b1")
			assert.Equal(t, desiredRunning, r.Desired, "保留手动的结果")
			assert.Equal(t, byOwner, r.DesiredBy)
			assert.Empty(t, e.orbs.took(), "没有停机")
			items, err := e.s.Activity(e.ctx, "b1", 100)
			require.NoError(t, err)
			for _, it := range items {
				assert.NotEqual(t, apigen.BoxActorSchedule, it.Actor, "不多出 schedule 活动：%+v", it)
			}
		})
	}
}

// 手动停机保持到下一个 on 边界；之后时段接管。
func TestManualStopHoldsUntilNextOn(t *testing.T) {
	e := newEnv(t, at("2026-11-20T04:00:00Z"))
	b := e.box("b1", orbs.StateReady)
	e.orbs.connected[b.OrbID] = true
	_, err := e.s.SetSchedule(e.ctx, "b1", &apigen.BoxSchedule{On: "05:00", Off: "23:00", Tz: "UTC"}, Actor{Kind: ActorOwner})
	require.NoError(t, err)

	// 04:00 设时段：最近的边界（昨天 23:00）早于 box 建好（desired_at = 04:00），不切换
	e.s.reconcile(e.ctx)
	assert.Equal(t, desiredRunning, e.row("b1").Desired)
	assert.Equal(t, byOwner, e.row("b1").DesiredBy)
	e.orbs.took()

	e.clk.Set(at("2026-11-20T05:00:00Z"))
	e.setState(b.OrbID, orbs.StateStopped)
	e.s.reconcile(e.ctx)
	r := e.row("b1")
	assert.Equal(t, desiredRunning, r.Desired)
	assert.Equal(t, bySchedule, r.DesiredBy)
	assert.Equal(t, []string{"wake:" + b.OrbID}, e.orbs.took())

	e.clk.Set(at("2026-11-20T12:00:00Z"))
	e.setState(b.OrbID, orbs.StateReady)
	_, err = e.s.Stop(e.ctx, "b1", Actor{Kind: ActorOwner})
	require.NoError(t, err)
	e.clk.Set(at("2026-11-20T13:00:00Z"))
	e.s.reconcile(e.ctx)
	assert.Equal(t, desiredStopped, e.row("b1").Desired, "手动停机在下一个边界之前保持")
	assert.Equal(t, byOwner, e.row("b1").DesiredBy)
	e.setState(b.OrbID, orbs.StateStopped)
	e.orbs.took()

	e.clk.Set(at("2026-11-20T23:00:00Z"))
	e.s.reconcile(e.ctx)
	assert.Equal(t, desiredStopped, e.row("b1").Desired)
	assert.Empty(t, e.orbs.took(), "已经停着")

	e.clk.Set(at("2026-11-21T05:00:01Z"))
	e.s.reconcile(e.ctx)
	assert.Equal(t, desiredRunning, e.row("b1").Desired, "下一个 on 边界启动")
	assert.Equal(t, []string{"wake:" + b.OrbID}, e.orbs.took())

	// 清除时段之后不再切换
	_, err = e.s.SetSchedule(e.ctx, "b1", nil, Actor{Kind: ActorOwner})
	require.NoError(t, err)
	e.clk.Set(at("2026-11-21T23:30:00Z"))
	e.s.reconcile(e.ctx)
	assert.Equal(t, desiredRunning, e.row("b1").Desired)
}

func TestSetScheduleValidation(t *testing.T) {
	e := newEnv(t, at("2026-11-20T12:00:00Z"))
	e.box("b1", orbs.StateReady)
	for _, sch := range []apigen.BoxSchedule{
		{On: "05:00", Off: "05:00", Tz: "UTC"},
		{On: "5:00", Off: "23:00", Tz: "UTC"},
		{On: "05:00", Off: "24:00", Tz: "UTC"},
		{On: "05:00", Off: "23:00", Tz: "Mars/Olympus"},
		{On: "05:00", Off: "23:00", Tz: ""},
	} {
		_, err := e.s.SetSchedule(e.ctx, "b1", &sch, Actor{Kind: ActorOwner})
		assert.Equal(t, CodeInvalid, codeOf(err), "%+v", sch)
	}
	v, err := e.s.SetSchedule(e.ctx, "b1", &apigen.BoxSchedule{On: "22:00", Off: "06:00", Tz: "Europe/Berlin"}, Actor{Kind: ActorOwner})
	require.NoError(t, err)
	require.NotNil(t, v.Schedule)
	assert.Equal(t, "Europe/Berlin", v.Schedule.Tz)
}

// ready 却没有 link：第 89 秒不 Revive，第 90 秒 Revive；有 link 时计时清零。
func TestReviveAfterUnlinked(t *testing.T) {
	e := newEnv(t, at("2026-11-20T12:00:00Z"))
	b := e.box("b1", orbs.StateReady)

	e.s.reconcile(e.ctx)
	assert.Empty(t, e.orbs.took())
	e.clk.Add(89 * time.Second)
	e.s.reconcile(e.ctx)
	assert.Empty(t, e.orbs.took(), "第 89 秒")
	e.clk.Add(time.Second)
	e.s.reconcile(e.ctx)
	assert.Equal(t, []string{"revive:" + b.OrbID}, e.orbs.took(), "第 90 秒")

	// 重新计时；中途接上 link 则清零
	e.clk.Add(60 * time.Second)
	e.s.reconcile(e.ctx)
	e.orbs.mu.Lock()
	e.orbs.connected[b.OrbID] = true
	e.orbs.mu.Unlock()
	e.clk.Add(60 * time.Second)
	e.s.reconcile(e.ctx)
	e.orbs.mu.Lock()
	e.orbs.connected[b.OrbID] = false
	e.orbs.mu.Unlock()
	e.clk.Add(60 * time.Second)
	e.s.reconcile(e.ctx)
	assert.Empty(t, e.orbs.took(), "link 回来过：重新计时")
}

// failed 的 box 两次重试唤醒至少隔 60 秒；主人手动启动不等。
func TestFailedRetryInterval(t *testing.T) {
	e := newEnv(t, at("2026-11-20T12:00:00Z"))
	b := e.box("b1", orbs.StateFailed)
	e.s.reconcile(e.ctx)
	assert.Equal(t, []string{"wake:" + b.OrbID}, e.orbs.took())
	e.clk.Add(59 * time.Second)
	e.s.reconcile(e.ctx)
	assert.Empty(t, e.orbs.took())
	e.clk.Add(time.Second)
	e.s.reconcile(e.ctx)
	assert.Equal(t, []string{"wake:" + b.OrbID}, e.orbs.took())
	_, err := e.s.Start(e.ctx, "b1", Actor{Kind: ActorOwner})
	require.NoError(t, err)
	e.s.reconcile(e.ctx)
	assert.Equal(t, []string{"wake:" + b.OrbID}, e.orbs.took(), "手动启动不等重试间隔")
}

// 时段停机在 box 忙时推迟，边界之后 30 分钟照停。
func TestScheduleStopDeferredWhenBusy(t *testing.T) {
	e := newEnv(t, at("2026-11-20T12:00:00Z"))
	b := e.box("b1", orbs.StateReady)
	e.orbs.connected[b.OrbID] = true
	_, err := e.s.SetSchedule(e.ctx, "b1", &apigen.BoxSchedule{On: "05:00", Off: "23:00", Tz: "UTC"}, Actor{Kind: ActorOwner})
	require.NoError(t, err)
	e.s.reconcile(e.ctx)
	assert.Empty(t, e.orbs.took())

	e.th.busy = true
	e.clk.Set(at("2026-11-20T23:00:10Z"))
	e.s.reconcile(e.ctx)
	assert.Equal(t, desiredStopped, e.row("b1").Desired)
	assert.Equal(t, bySchedule, e.row("b1").DesiredBy)
	assert.Empty(t, e.orbs.took(), "忙：推迟")
	e.clk.Set(at("2026-11-20T23:29:59Z"))
	e.s.reconcile(e.ctx)
	assert.Empty(t, e.orbs.took())
	e.clk.Set(at("2026-11-20T23:30:00Z"))
	e.s.reconcile(e.ctx)
	assert.Equal(t, []string{"pause-stop:" + b.OrbID}, e.orbs.took(), "边界之后 30 分钟照停")

	// 主人手动停机不推迟
	e.th.busy = true
	b2 := e.box("b2", orbs.StateReady)
	_, err = e.s.Stop(e.ctx, "b2", Actor{Kind: ActorOwner})
	require.NoError(t, err)
	e.s.reconcile(e.ctx)
	assert.Contains(t, e.orbs.took(), "pause-stop:"+b2.OrbID)
}

// Usage：21 天，创建之前为 0；在线率按 ready 时长占比，今天只算到现在。
func TestUsage(t *testing.T) {
	now := at("2026-11-20T12:00:00Z")
	created := at("2026-11-18T12:00:00Z")
	e := newEnv(t, created)
	b := e.box("b1", orbs.StateReady)
	ev := func(ts time.Time, to string) {
		require.NoError(t, e.st.Q.InsertOrbEvent(e.ctx, db.InsertOrbEventParams{OrbID: b.OrbID, Ts: ts.UnixMilli(), FromState: "", ToState: to, Detail: "{}"}))
	}
	ev(created.Add(-time.Second), orbs.StateRequested)
	ev(created, orbs.StateReady)
	ev(at("2026-11-19T00:00:00Z"), orbs.StateStopped)
	ev(at("2026-11-19T18:00:00Z"), orbs.StateReady)
	ev(at("2026-11-20T06:00:00Z"), orbs.StateStopped)
	thread := func(ts time.Time, kind string) {
		id := ids.Thread()
		require.NoError(t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{
			Kind: kind, ID: id, OrbID: &b.OrbID, TitleSource: "auto", State: "idle", SessionDir: "/s", CreatedAt: ts.UnixMilli(), UpdatedAt: 1,
		}))
	}
	thread(at("2026-11-19T10:00:00Z"), "work")
	thread(at("2026-11-19T11:00:00Z"), "work")
	thread(at("2026-11-20T01:00:00Z"), "work")
	e.clk.Set(now)

	u, err := e.s.Usage(e.ctx, "b1")
	require.NoError(t, err)
	require.Len(t, u.Days, 21)
	assert.Equal(t, "2026-10-31", u.Days[0].Date)
	last := u.Days[20]
	assert.Equal(t, "2026-11-20", last.Date)
	assert.Equal(t, 1, last.Threads)
	assert.InDelta(t, 0.5, last.UpRatio, 1e-6, "00:00–06:00 在线，算到 12:00")
	y := u.Days[19]
	assert.Equal(t, 2, y.Threads)
	assert.InDelta(t, 0.25, y.UpRatio, 1e-6, "18:00–24:00")
	c := u.Days[18]
	assert.Equal(t, "2026-11-18", c.Date)
	assert.InDelta(t, 1.0, c.UpRatio, 1e-6, "创建当天从创建时刻算起")
	for _, d := range u.Days[:18] {
		assert.Zero(t, d.Threads, d.Date)
		assert.Zero(t, d.UpRatio, d.Date)
	}
}

// Delete 在 orbs.Delete 出错时 box 行仍在；再调一次成功；之后按名字找不到。
func TestDeleteRetry(t *testing.T) {
	e := newEnv(t, at("2026-11-20T12:00:00Z"))
	b := e.box("b1", orbs.StateReady)
	e.orbs.deleteErr = errors.New("boom")
	require.Error(t, e.s.Delete(e.ctx, "b1", Actor{Kind: ActorOwner}))
	_, err := e.s.Get(e.ctx, "b1")
	require.NoError(t, err, "box 行还在")

	e.orbs.deleteErr = nil
	require.NoError(t, e.s.Delete(e.ctx, "b1", Actor{Kind: ActorOwner}))
	assert.Equal(t, []string{"delete:" + b.OrbID, "delete:" + b.OrbID}, e.orbs.took())
	_, err = e.s.Get(e.ctx, "b1")
	assert.Equal(t, CodeBoxNotFound, codeOf(err))
	assert.Equal(t, CodeBoxNotFound, codeOf(e.s.Delete(e.ctx, "b1", Actor{Kind: ActorOwner})))
}

// orb 已删除的 box 行被对账删掉；它在那之前就不出现在 List / Get 里。
func TestReconcileDropsBoxOfDeletedOrb(t *testing.T) {
	e := newEnv(t, at("2026-11-20T12:00:00Z"))
	b := e.box("b1", orbs.StateReady)
	e.box("b2", orbs.StateReady)
	e.setState(b.OrbID, orbs.StateDeleted)
	list, err := e.s.List(e.ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "b2", list[0].Name)
	_, err = e.s.Get(e.ctx, "b1")
	assert.Equal(t, CodeBoxNotFound, codeOf(err))

	e.s.reconcile(e.ctx)
	_, err = e.st.Q.GetBoxByName(e.ctx, "b1")
	require.Error(t, err, "box 行已删")
	_, err = e.st.Q.GetBoxByName(e.ctx, "b2")
	require.NoError(t, err)
}

// 创建超过 1 分钟仍没有 box 行的 box orb 被对账删掉；刚建的不动。
func TestReconcileDeletesOrphanBoxOrbs(t *testing.T) {
	e := newEnv(t, at("2026-11-20T12:00:00Z"))
	mk := func(createdAt time.Time) string {
		id := ids.Orb()
		require.NoError(t, e.st.Q.CreateOrb(e.ctx, db.CreateOrbParams{
			ID: id, ProviderID: e.orbs.provID, Type: "medium", State: orbs.StateReady, StateChangedAt: 1,
			TokenHash: ids.Hash(id), CreatedAt: createdAt.UnixMilli(), Box: new(int64(1)),
		}))
		return id
	}
	old := mk(at("2026-11-20T11:58:00Z"))
	fresh := mk(at("2026-11-20T11:59:30Z"))
	e.s.reconcile(e.ctx)
	assert.Equal(t, []string{"delete:" + old}, e.orbs.took())
	o, err := e.st.Q.GetOrb(e.ctx, fresh)
	require.NoError(t, err)
	assert.Equal(t, orbs.StateReady, o.State)
}

// Changed 在 250ms 内合并；Touch 走同一个合并。
func TestChangedCoalesced(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	calls := make(chan struct{}, 8)
	s := New(Config{Changed: func() { calls <- struct{}{} }}, st, &fakeOrbs{st: st, connected: map[string]bool{}}, &fakeThreads{})
	s.Touch()
	s.Touch()
	s.changed()
	select {
	case <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("Changed 没有被调用")
	}
	select {
	case <-calls:
		t.Fatal("合并期内只调一次")
	case <-time.After(400 * time.Millisecond):
	}
}
