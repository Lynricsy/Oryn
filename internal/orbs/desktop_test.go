package orbs

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- 假钩子：记录调用顺序与调用当时的外部状态 ----

type fakeHooks struct {
	h   *harness
	drv *fakeDriver

	needs atomic.Bool
	// onReady 非 nil 时在 BeforeReady 里调用（模拟同步期间 link 断开）。
	onReady func(orbID string)

	mu  sync.Mutex
	log []string
}

func (f *fakeHooks) add(s string) {
	f.mu.Lock()
	f.log = append(f.log, s)
	f.mu.Unlock()
}

func (f *fakeHooks) entries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *fakeHooks) BeforeReady(_ context.Context, orbID string) {
	orb, _ := f.h.st.Q.GetOrb(context.Background(), orbID)
	f.add("ready@" + orb.State)
	if f.onReady != nil {
		f.onReady(orbID)
	}
}

func (f *fakeHooks) NeedsFreezeSave(context.Context, string) bool {
	f.add("needs")
	return f.needs.Load()
}

func (f *fakeHooks) BeforeFreeze(_ context.Context, orbID string) {
	js, _ := f.h.st.Q.ListUnfinishedJobsByOrb(context.Background(), &orbID)
	queued := false
	for _, j := range js {
		queued = queued || (j.Op == "pause" && jobPauseMode(j) == PauseFreeze)
	}
	orb, _ := f.h.st.Q.GetOrb(context.Background(), orbID)
	f.add("freeze@" + orb.State + map[bool]string{true: "+queued", false: ""}[queued])
}

func (f *fakeHooks) BeforeShutdown(_ context.Context, orbID string) {
	sent := slices.Contains(f.drv.orbd(orbID).methods(), "shutdown")
	f.add("shutdown" + map[bool]string{true: "+sent", false: ""}[sent])
}

func newDesktopHarness(t *testing.T, features []string, mod ...func(*Config)) (*harness, *fakeDriver, *fakeHooks) {
	drv := newFakeDriver()
	drv.features = features
	var m func(*Config)
	if len(mod) > 0 {
		m = mod[0]
	}
	h := newHarness(t, drv, nil, m)
	hooks := &fakeHooks{h: h, drv: drv}
	h.svc.SetDesktopHooks(hooks)
	return h, drv, hooks
}

// 三个钩子的调用时机：BeforeReady 先于 ready；preFreeze 只在静止且 NeedsFreezeSave 为真时调用 BeforeFreeze，
// 且先于 pause 任务入队；BeforeShutdown 先于 shutdown 请求。
func TestDesktopHooksTiming(t *testing.T) {
	h, drv, hooks := newDesktopHarness(t, []string{FeatureDesktop})
	ctx := context.Background()

	orb, _ := h.readyOrb(drv, "")
	assert.Equal(t, []string{"ready@setting_up"}, hooks.entries(), "BeforeReady 在 setting_up 中调用，此时尚未 ready")

	// 无东西可存：不调 BeforeFreeze
	require.NoError(t, h.svc.Pause(ctx, orb.ID, PauseFreeze))
	h.waitState(orb.ID, StateFrozen, 5*time.Second)
	assert.Equal(t, []string{"ready@setting_up", "needs"}, hooks.entries())

	// 有东西可存但线程不静止：闸门拒绝，不调 BeforeFreeze（手动冻结本身不经闸门）
	require.NoError(t, h.svc.Wake(ctx, orb.ID))
	h.waitState(orb.ID, StateReady, 5*time.Second)
	hooks.needs.Store(true)
	h.quiet.Store(false)
	require.NoError(t, h.svc.Pause(ctx, orb.ID, PauseFreeze))
	h.waitState(orb.ID, StateFrozen, 5*time.Second)
	assert.Equal(t, []string{"ready@setting_up", "needs", "ready@resuming", "needs"}, hooks.entries())

	// 静止且有东西可存：BeforeFreeze 在 ready 中、pause 任务入队之前调用
	h.quiet.Store(true)
	require.NoError(t, h.svc.Wake(ctx, orb.ID))
	h.waitState(orb.ID, StateReady, 5*time.Second)
	require.NoError(t, h.svc.Pause(ctx, orb.ID, PauseFreeze))
	h.waitState(orb.ID, StateFrozen, 5*time.Second)
	assert.Equal(t, []string{"ready@setting_up", "needs", "ready@resuming", "needs", "ready@resuming", "needs", "freeze@ready"}, hooks.entries())

	// 删除：BeforeShutdown 先于 shutdown 请求
	require.NoError(t, h.svc.Wake(ctx, orb.ID))
	h.waitState(orb.ID, StateReady, 5*time.Second)
	require.NoError(t, h.svc.Delete(ctx, orb.ID))
	h.waitState(orb.ID, StateDeleted, 5*time.Second)
	got := hooks.entries()
	assert.Equal(t, "shutdown", got[len(got)-1])
	assert.Contains(t, drv.orbd(orb.ID).methods(), "shutdown")
}

// 空闲评估的冻结同样先经 preFreeze（静止且有东西可存时在 ready 中调用 BeforeFreeze）。
func TestDesktopIdleFreezeSaves(t *testing.T) {
	h, _, hooks := newDesktopHarness(t, []string{FeatureDesktop}, func(c *Config) {
		c.Idle.AgentIdle, c.Idle.UserIdle = config.Duration(time.Millisecond), config.Duration(time.Millisecond)
	})
	hooks.needs.Store(true)
	orb, err := h.svc.Create(context.Background(), CreateParams{})
	require.NoError(t, err)
	h.waitState(orb.ID, StateFrozen, 10*time.Second)
	assert.Equal(t, []string{"ready@setting_up", "needs", "freeze@ready"}, hooks.entries())
}

// BeforeReady 期间 link 断开：orb 回到 connecting 而不是 ready，新 link 重跑本阶段。
func TestDesktopBeforeReadyLinkLost(t *testing.T) {
	h, drv, hooks := newDesktopHarness(t, []string{FeatureDesktop})
	var dropped atomic.Bool
	hooks.onReady = func(orbID string) {
		if dropped.Swap(true) {
			return
		}
		ctl, ok := h.svc.Ctl(orbID)
		require.True(t, ok)
		drv.orbd(orbID).disconnect()
		<-ctl.Done()
	}
	orb, err := h.svc.Create(context.Background(), CreateParams{})
	require.NoError(t, err)
	h.waitState(orb.ID, StateConnecting, 5*time.Second)
	require.Eventually(t, func() bool { return slices.Contains(h.eventSeq(orb.ID), "setting_up>connecting") }, 5*time.Second, 10*time.Millisecond)
	assert.NotContains(t, h.eventSeq(orb.ID), "setting_up>ready")

	require.NoError(t, drv.orbd(orb.ID).connect())
	h.waitState(orb.ID, StateReady, 5*time.Second)
	assert.Equal(t, []string{"ready@setting_up", "ready@setting_up"}, hooks.entries())
}

// hello 不带 desktop：三个钩子都不调用。
func TestDesktopHooksSkippedWithoutFeature(t *testing.T) {
	h, drv, hooks := newDesktopHarness(t, []string{FeatureFS})
	hooks.needs.Store(true)
	ctx := context.Background()
	orb, _ := h.readyOrb(drv, "")
	require.NoError(t, h.svc.Pause(ctx, orb.ID, PauseFreeze))
	h.waitState(orb.ID, StateFrozen, 5*time.Second)
	require.NoError(t, h.svc.Wake(ctx, orb.ID))
	h.waitState(orb.ID, StateReady, 5*time.Second)
	require.NoError(t, h.svc.Delete(ctx, orb.ID))
	h.waitState(orb.ID, StateDeleted, 5*time.Second)
	assert.Empty(t, hooks.entries())
}

// desktop.state 写镜像并发出 EventDesktop；browser 不合法丢弃；orb 已删除时不写；View 带 desktop。
func TestDesktopStateMirror(t *testing.T) {
	h, drv, _ := newDesktopHarness(t, []string{FeatureDesktop})
	ctx := context.Background()
	orb, ctl := h.readyOrb(drv, "")
	v, err := View(ctx, h.st.Q, orb)
	require.NoError(t, err)
	assert.Nil(t, v.Desktop, "从未上报过 desktop.state")

	before := h.countEvents(orb.ID, EventDesktop)
	require.NoError(t, ctl.Notify("desktop.state", map[string]any{"displayReady": true, "browser": "bogus", "dirty": true}))
	require.NoError(t, ctl.Notify("desktop.state", apigen.LinkCtlDesktopStateParams{DisplayReady: true, Browser: apigen.OrbBrowserStatusRunning, Dirty: true}))
	require.Eventually(t, func() bool {
		d, err := h.st.Q.GetOrbDesktop(ctx, orb.ID)
		return err == nil && d.Browser == "running"
	}, 5*time.Second, 10*time.Millisecond)
	d, err := h.st.Q.GetOrbDesktop(ctx, orb.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, d.Reported)
	assert.EqualValues(t, 1, d.DisplayReady)
	assert.EqualValues(t, 1, d.Dirty)
	require.Eventually(t, func() bool { return h.countEvents(orb.ID, EventDesktop) == before+1 }, 5*time.Second, 10*time.Millisecond)

	cur, err := h.svc.Get(ctx, orb.ID)
	require.NoError(t, err)
	v, err = View(ctx, h.st.Q, cur)
	require.NoError(t, err)
	require.NotNil(t, v.Desktop)
	assert.Equal(t, apigen.OrbDesktop{DisplayReady: true, Browser: apigen.OrbBrowserStatusRunning}, *v.Desktop)

	// 已删除：不写
	require.NoError(t, h.st.Q.SetOrbState(ctx, db.SetOrbStateParams{State: StateDeleting, StateChangedAt: 1, ID: orb.ID}))
	h.svc.ApplyDesktopState(ctx, orb.ID, apigen.LinkCtlDesktopStateParams{Browser: apigen.OrbBrowserStatusStopped})
	d, err = h.st.Q.GetOrbDesktop(ctx, orb.ID)
	require.NoError(t, err)
	assert.Equal(t, "running", d.Browser)
	assert.Equal(t, before+1, h.countEvents(orb.ID, EventDesktop))
}

// Create 带 Profile 时同一事务写 orb_desktops：readonly → 只读副本（requested）；takeover → 独占 + 接管标记。
func TestCreateBindsBrowserProfile(t *testing.T) {
	drv := newFakeDriver()
	h := newHarness(t, drv, nil, nil)
	ctx := context.Background()
	pid := ids.BrowserProfile()
	require.NoError(t, h.st.Q.CreateBrowserProfile(ctx, db.CreateBrowserProfileParams{ID: pid, Name: "work", Now: 1}))

	for _, tc := range []struct {
		mode, access string
		takeover     int64
		reason       bool
	}{{"", "exclusive", 0, false}, {"readonly", "readonly", 0, true}, {"takeover", "exclusive", 1, false}} {
		orb, err := h.svc.Create(ctx, CreateParams{BrowserProfileID: pid, ProfileMode: tc.mode})
		require.NoError(t, err)
		d, err := h.st.Q.GetOrbDesktop(ctx, orb.ID)
		require.NoError(t, err, tc.mode)
		require.NotNil(t, d.ProfileID)
		assert.Equal(t, pid, *d.ProfileID)
		assert.Equal(t, tc.access, d.ProfileMode, tc.mode)
		assert.Equal(t, tc.takeover, d.Takeover, tc.mode)
		assert.Equal(t, tc.reason, d.ReadonlyReason != nil, tc.mode)
		assert.EqualValues(t, -1, d.LocalRev)
		assert.EqualValues(t, 0, d.Reported)
	}
	orb, err := h.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	_, err = h.st.Q.GetOrbDesktop(ctx, orb.ID)
	assert.Error(t, err, "不带 Profile 不建行")
}

// DesktopView 的各种 status。
func TestDesktopViewStatuses(t *testing.T) {
	b := newBare(t, nil)
	ctx := context.Background()
	q := b.st.Q
	pid := ids.BrowserProfile()
	require.NoError(t, q.CreateBrowserProfile(ctx, db.CreateBrowserProfileParams{ID: pid, Name: "work", Now: 1}))
	mk := func(mode string) string {
		o := b.orb(StateReady, 1)
		require.NoError(t, q.UpsertOrbDesktopProfile(ctx, ProfileBinding(o.ID, pid, apigen.BrowserProfileMode(mode), 5)))
		require.NoError(t, q.UpsertOrbDesktopState(ctx, db.UpsertOrbDesktopStateParams{OrbID: o.ID, DisplayReady: 1, Browser: "stopped", Now: 5}))
		return o.ID
	}
	view := func(orbID string) *apigen.OrbDesktopProfile {
		d, err := DesktopView(ctx, q, orbID)
		require.NoError(t, err)
		require.NotNil(t, d)
		require.NotNil(t, d.Profile)
		return d.Profile
	}
	holder, other, released, broken, reader := mk(""), mk(""), mk(""), mk(""), mk("readonly")
	require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: "T-1", OrbID: &holder, Title: "持有者", TitleSource: "user", State: "idle", SessionDir: "/s", CreatedAt: 3, UpdatedAt: 3}))
	_, err := q.AcquireBrowserProfile(ctx, db.AcquireBrowserProfileParams{OrbID: holder, Now: 7, ID: pid})
	require.NoError(t, err)
	require.NoError(t, q.SetOrbDesktopLocal(ctx, db.SetOrbDesktopLocalParams{LocalRev: 2, OrbID: holder, Now: 8}))
	code := "invalid_archive"
	require.NoError(t, q.SetOrbDesktopLocal(ctx, db.SetOrbDesktopLocalParams{LocalRev: -1, ProfileError: &code, OrbID: broken, Now: 8}))

	p := view(holder)
	assert.Equal(t, apigen.OrbDesktopProfileStatusAttached, p.Status)
	assert.EqualValues(t, 2, p.Revision)
	assert.Equal(t, apigen.BrowserProfileAccessExclusive, p.Mode)

	p = view(other)
	assert.Equal(t, apigen.OrbDesktopProfileStatusInUse, p.Status)
	assert.EqualValues(t, 0, p.Revision, "未装载为 0")
	require.NotNil(t, p.HeldBy)
	assert.Equal(t, holder, p.HeldBy.OrbID)
	assert.EqualValues(t, 7, p.HeldBy.Since)
	assert.Equal(t, []string{"T-1"}, p.HeldBy.ThreadIds)
	require.NotNil(t, p.HeldBy.ThreadTitle)
	assert.Equal(t, "持有者", *p.HeldBy.ThreadTitle)

	assert.Equal(t, apigen.OrbDesktopProfileStatusError, view(broken).Status)
	assert.Equal(t, code, *view(broken).Error)

	p = view(reader)
	assert.Equal(t, apigen.OrbDesktopProfileStatusReadonly, p.Status)
	assert.Equal(t, apigen.BrowserProfileReadonlyReasonRequested, *p.ReadonlyReason)
	assert.Nil(t, p.TakenOverBy)

	// 被接管降级
	title, tid := "接管方", "T-2"
	n, err := q.DemoteOrbDesktopReadonly(ctx, db.DemoteOrbDesktopReadonlyParams{ReadonlyBy: &title, ReadonlyByThreadID: &tid, Now: 9, OrbID: holder, ProfileID: pid})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	p = view(holder)
	assert.Equal(t, apigen.OrbDesktopProfileStatusReadonly, p.Status)
	assert.Equal(t, apigen.BrowserProfileAccessReadonly, p.Mode)
	require.NotNil(t, p.TakenOverBy)
	assert.Equal(t, apigen.OrbDesktopTakenOver{At: 9, ThreadID: &tid, ThreadTitle: &title}, *p.TakenOverBy)

	_, err = q.ReleaseBrowserProfileByOrb(ctx, holder)
	require.NoError(t, err)
	assert.Equal(t, apigen.OrbDesktopProfileStatusReleased, view(released).Status)

	// 没有上报：nil；没有行：nil
	o := b.orb(StateReady, 1)
	require.NoError(t, q.UpsertOrbDesktopProfile(ctx, ProfileBinding(o.ID, pid, "", 1)))
	d, err := DesktopView(ctx, q, o.ID)
	require.NoError(t, err)
	assert.Nil(t, d)
	d, err = DesktopView(ctx, q, "orb-none")
	require.NoError(t, err)
	assert.Nil(t, d)
}

// DialDesk：ok 后流上是原始字节；orbd 的拒绝映射为 DeskDialError；应答超时 → timeout；无 link / 旧 orbd。
func TestDialDesk(t *testing.T) {
	old := deskDialTimeout
	deskDialTimeout = 300 * time.Millisecond
	t.Cleanup(func() { deskDialTimeout = old })

	var mode atomic.Value
	mode.Store("ok")
	drv := newFakeDriver()
	drv.features = []string{FeatureDesktop}
	drv.desk = func(st *link.Stream) {
		defer st.Close()
		switch mode.Load().(string) {
		case "ok":
			_, _ = io.WriteString(st, `{"ok":true}`+"\nRFB 003.008\n")
			_, _ = io.Copy(st, st)
		case "unavailable":
			_, _ = io.WriteString(st, `{"ok":false,"code":"display_unavailable","message":"display not ready"}`+"\n")
		case "bare":
			_, _ = io.WriteString(st, `{"ok":false}`+"\n")
		default:
			_, _ = io.Copy(io.Discard, st)
		}
	}
	h := newHarness(t, drv, nil, nil)
	ctx := context.Background()
	orb, _ := h.readyOrb(drv, "")

	conn, err := h.svc.DialDesk(ctx, orb.ID)
	require.NoError(t, err)
	banner := make([]byte, 12)
	_, err = io.ReadFull(conn, banner)
	require.NoError(t, err)
	assert.Equal(t, "RFB 003.008\n", string(banner))
	payload := strings.Repeat("rfb;", 1<<16)
	go func() { _, _ = io.WriteString(conn, payload) }()
	echo := make([]byte, len(payload))
	_, err = io.ReadFull(conn, echo)
	require.NoError(t, err)
	assert.Equal(t, payload, string(echo))
	require.NoError(t, conn.Close())

	for m, code := range map[string]string{"unavailable": "display_unavailable", "bare": "failed", "silent": "timeout"} {
		mode.Store(m)
		_, err := h.svc.DialDesk(ctx, orb.ID)
		var de *DeskDialError
		require.ErrorAs(t, err, &de, m)
		assert.Equal(t, code, de.Code, m)
	}
	_, err = h.svc.DialDesk(ctx, "orb-missing")
	assert.ErrorIs(t, err, ErrOrbNotReady)

	drv.mu.Lock()
	drv.features = []string{FeatureFS}
	drv.mu.Unlock()
	oldOrb, _ := h.readyOrb(drv, "")
	_, err = h.svc.DialDesk(ctx, oldOrb.ID)
	assert.ErrorIs(t, err, ErrOrbdOutdated)
	assert.ErrorIs(t, h.svc.SendProfileMode(ctx, oldOrb.ID, apigen.LinkCtlProfileModeParams{}), ErrOrbdOutdated)

	// profile.mode 经 ctl 送达
	require.NoError(t, h.svc.SendProfileMode(ctx, orb.ID, apigen.LinkCtlProfileModeParams{ProfileID: "bpf_X", Mode: apigen.BrowserProfileAccessReadonly, Reason: apigen.BrowserProfileReadonlyReasonTakenOver}))
	raw, ok := drv.orbd(orb.ID).call("profile.mode")
	require.True(t, ok)
	var pm apigen.LinkCtlProfileModeParams
	require.NoError(t, json.Unmarshal(raw, &pm))
	assert.Equal(t, "bpf_X", pm.ProfileID)
}
