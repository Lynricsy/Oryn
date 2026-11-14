package orbs

import (
	"context"
	"encoding/json"
	"io"
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
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M4 §3：服务镜像、portal.configure、portal 拨号与合并的用户交互。

func lk(title, path string) apigen.OrbPortalLink {
	return apigen.OrbPortalLink{Title: title, Path: path}
}

func (h *harness) mirror(orbID string) []db.OrbService {
	h.t.Helper()
	rows, err := h.st.Q.ListOrbServices(context.Background(), orbID)
	require.NoError(h.t, err)
	return rows
}

func (h *harness) sharePorts(orbID string) []int64 {
	h.t.Helper()
	rows, err := h.st.Q.ListPortalSharesByOrb(context.Background(), orbID)
	require.NoError(h.t, err)
	out := []int64{}
	for _, r := range rows {
		out = append(out, r.Port)
	}
	return out
}

func (h *harness) countEvents(orbID, kind string) int {
	h.evMu.Lock()
	defer h.evMu.Unlock()
	n := 0
	for _, ev := range h.events {
		if ev.OrbID == orbID && ev.Kind == kind {
			n++
		}
	}
	return n
}

func (o *fakeOrbd) callsOf(method string) []json.RawMessage {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []json.RawMessage
	for _, c := range o.calls {
		if c.method == method {
			out = append(out, c.params)
		}
	}
	return out
}

// services.state 整体替换镜像：清洗非法项、has_portal 判定、撤掉不再声明的端口的分享、发出 EventServices；
// View 带 services / portals / public（过期分享不出现）；orb 删除后镜像与分享清空。
func TestServicesStateMirror(t *testing.T) {
	drv := newFakeDriver()
	drv.features = []string{FeatureServices, FeaturePortal}
	h := newHarness(t, drv, nil, nil)
	ctx := context.Background()
	orb, c := h.readyOrb(drv, "")

	future := time.Now().Add(time.Hour).UnixMilli()
	for _, p := range []int64{3000, 4000, 5000} {
		require.NoError(t, h.st.Q.UpsertPortalShare(ctx, db.UpsertPortalShareParams{OrbID: orb.ID, Port: p, CreatedAt: 1, ExpiresAt: future}))
	}

	long := strings.Repeat("x", 5000)
	file, dyn := apigen.OrbServiceSourceFile, apigen.OrbServiceSourceDynamic
	require.NoError(t, c.Notify("services.state", apigen.LinkCtlServicesStateParams{Services: []apigen.OrbService{
		{Name: "web", Source: file, Command: long, Status: apigen.OrbServiceStatusRunning, Port: new(3000),
			Links: []apigen.OrbPortalLink{lk("web", "/"), lk("abs", "https://evil.example/"), lk("rel", "docs")}, ThreadID: new("not-a-thread")},
		{Name: "api", Source: dyn, Command: "api", Status: apigen.OrbServiceStatusStarting, Port: new(4000), Links: []apigen.OrbPortalLink{lk("api", "/v1")}},
		{Name: "worker", Source: file, Command: "w", Status: apigen.OrbServiceStatusRunning, Links: []apigen.OrbPortalLink{}},
		{Name: "dup-port", Source: dyn, Command: "d", Status: apigen.OrbServiceStatusFailed, Port: new(3000), Links: []apigen.OrbPortalLink{lk("d", "/")}, Error: new("port_conflict")},
		{Name: "Bad Name", Source: file, Command: "b", Status: apigen.OrbServiceStatusRunning, Links: []apigen.OrbPortalLink{}},
		{Name: "web", Source: file, Command: "dup", Status: apigen.OrbServiceStatusRunning, Links: []apigen.OrbPortalLink{}},
		{Name: "hiport", Source: file, Command: "h", Status: apigen.OrbServiceStatusRunning, Port: new(70000), Links: []apigen.OrbPortalLink{}},
		{Name: "weird", Source: file, Command: "x", Status: "zombie", Links: []apigen.OrbPortalLink{}},
		{Name: "src", Source: "elsewhere", Command: "x", Status: apigen.OrbServiceStatusRunning, Links: []apigen.OrbPortalLink{}},
	}}))
	require.Eventually(t, func() bool { return len(h.mirror(orb.ID)) == 4 }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return h.countEvents(orb.ID, EventServices) >= 1 }, 5*time.Second, 10*time.Millisecond)

	rows := h.mirror(orb.ID)
	byName := map[string]db.OrbService{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	assert.ElementsMatch(t, []string{"api", "dup-port", "web", "worker"}, []string{rows[0].Name, rows[1].Name, rows[2].Name, rows[3].Name})
	assert.EqualValues(t, 1, byName["web"].HasPortal)
	assert.EqualValues(t, 1, byName["api"].HasPortal)
	assert.EqualValues(t, 0, byName["worker"].HasPortal, "无端口")
	assert.EqualValues(t, 0, byName["dup-port"].HasPortal, "同端口只算第一个声明 portal 的服务")
	var web apigen.OrbService
	require.NoError(t, json.Unmarshal([]byte(byName["web"].Doc), &web))
	assert.Len(t, web.Command, maxServiceCommand, "command 截到 4096 字节")
	assert.Equal(t, []apigen.OrbPortalLink{lk("web", "/")}, web.Links, "非 / 开头的链接被丢弃")
	assert.Nil(t, web.ThreadID, "不合法的线程 id 被丢弃")
	assert.Equal(t, []int64{3000, 4000}, h.sharePorts(orb.ID), "未声明 portal 的端口 5000 的分享被撤销")

	sv, ok, err := h.svc.PortalService(ctx, orb.ID, 4000)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "api", sv.Name)
	_, ok, err = h.svc.PortalService(ctx, orb.ID, 5000)
	require.NoError(t, err)
	assert.False(t, ok)

	// 下一份快照去掉 api：镜像整体替换，4000 的分享随之撤销
	before := h.countEvents(orb.ID, EventServices)
	require.NoError(t, c.Notify("services.state", apigen.LinkCtlServicesStateParams{Services: []apigen.OrbService{
		{Name: "web", Source: file, Command: "python3 -m http.server", Status: apigen.OrbServiceStatusRunning, Port: new(3000), Links: []apigen.OrbPortalLink{lk("web", "/"), lk("Docs", "/docs")}},
	}}))
	require.Eventually(t, func() bool { return h.countEvents(orb.ID, EventServices) > before }, 5*time.Second, 10*time.Millisecond)
	require.Len(t, h.mirror(orb.ID), 1)
	assert.Equal(t, []int64{3000}, h.sharePorts(orb.ID))

	cur, err := h.svc.Get(ctx, orb.ID)
	require.NoError(t, err)
	v, err := View(ctx, h.st.Q, cur)
	require.NoError(t, err)
	require.Len(t, v.Services, 1)
	assert.Equal(t, "web", v.Services[0].Name)
	require.Len(t, v.Portals, 1)
	assert.Equal(t, 3000, v.Portals[0].Port)
	assert.Equal(t, "web", v.Portals[0].Service)
	assert.Len(t, v.Portals[0].Links, 2)
	require.NotNil(t, v.Portals[0].Public)
	assert.Equal(t, future, v.Portals[0].Public.ExpiresAt)

	// 过期的分享不出现在 public
	require.NoError(t, h.st.Q.UpsertPortalShare(ctx, db.UpsertPortalShareParams{OrbID: orb.ID, Port: 3000, CreatedAt: 1, ExpiresAt: time.Now().Add(-time.Second).UnixMilli()}))
	v, err = View(ctx, h.st.Q, cur)
	require.NoError(t, err)
	require.Len(t, v.Portals, 1)
	assert.Nil(t, v.Portals[0].Public)

	// Refresh 推送一次 EventServices
	before = h.countEvents(orb.ID, EventServices)
	h.svc.Refresh(ctx, orb.ID)
	assert.Equal(t, before+1, h.countEvents(orb.ID, EventServices))

	// 删除 orb：镜像与分享清空，View 不带服务
	require.NoError(t, h.st.Q.UpsertPortalShare(ctx, db.UpsertPortalShareParams{OrbID: orb.ID, Port: 3000, CreatedAt: 1, ExpiresAt: future}))
	require.NoError(t, h.svc.Delete(ctx, orb.ID))
	cur = h.waitState(orb.ID, StateDeleted, 10*time.Second)
	assert.Empty(t, h.mirror(orb.ID))
	assert.Empty(t, h.sharePorts(orb.ID))
	v, err = View(ctx, h.st.Q, cur)
	require.NoError(t, err)
	assert.Nil(t, v.Services)
	assert.Nil(t, v.Portals)
}

// 支持 portal 的 orbd 在 hello 后、setup / resume 之前收到 portal.configure：urlTemplate 取配置，
// primaryThreadId = 该 orb 最早创建的线程；重连后再发一次。
func TestPortalConfigureOnHello(t *testing.T) {
	const tmpl = "https://t-{code}-p{port}.orb.test/"
	drv := newFakeDriver()
	drv.features = []string{FeatureServices, FeaturePortal}
	h := newHarness(t, drv, nil, func(c *Config) { c.PortalURLTemplate = tmpl })
	ctx := context.Background()
	orb, _ := h.readyOrb(drv, "")
	o := drv.orbd(orb.ID)

	calls := o.callsOf("portal.configure")
	require.Len(t, calls, 1)
	var p apigen.LinkCtlPortalConfigureParams
	require.NoError(t, json.Unmarshal(calls[0], &p))
	assert.Equal(t, tmpl, p.URLTemplate)
	assert.Nil(t, p.PrimaryThreadID, "还没有线程")
	methods := o.methods()
	require.GreaterOrEqual(t, len(methods), 2)
	assert.Equal(t, []string{"portal.configure", "setup.run"}, methods[:2], "先 portal.configure 再 setup")

	// 两个线程：后插入的那个创建得更早，应被选为主线程
	later, earlier := ids.Thread(), ids.Thread()
	for _, th := range []struct {
		id string
		at int64
	}{{later, 20}, {earlier, 10}} {
		require.NoError(t, h.st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: th.id, OrbID: &orb.ID, TitleSource: "auto", State: "idle", SessionDir: "/s/" + th.id, CreatedAt: th.at, UpdatedAt: th.at}))
	}
	o.disconnect()
	require.NoError(t, o.connect())
	require.Eventually(t, func() bool { return len(o.callsOf("portal.configure")) == 2 }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, json.Unmarshal(o.callsOf("portal.configure")[1], &p))
	require.NotNil(t, p.PrimaryThreadID)
	assert.Equal(t, earlier, *p.PrimaryThreadID)
}

// DialPortal：ok 后流上是原始字节（应答行之后紧跟的字节不丢）；orbd 的各种拒绝映射为 PortalDialError；
// 应答行超时 → Code=timeout；无 link → ErrOrbNotReady；旧 orbd → ErrOrbdOutdated。
func TestDialPortal(t *testing.T) {
	old := portalDialTimeout
	portalDialTimeout = 300 * time.Millisecond
	t.Cleanup(func() { portalDialTimeout = old })

	reply := func(st *link.Stream, s string) { _, _ = io.WriteString(st, s+"\n") }
	drv := newFakeDriver()
	drv.features = []string{FeatureServices, FeaturePortal}
	drv.portal = func(st *link.Stream) {
		defer st.Close()
		switch st.Header.Port {
		case 8080:
			// 应答行与首批字节一次写出，之后回显
			_, _ = io.WriteString(st, `{"ok":true}`+"\nBANNER")
			_, _ = io.Copy(st, st)
		case 7811:
			reply(st, `{"ok":false,"code":"forbidden_port","message":"egress port"}`)
		case 9999:
			reply(st, `{"ok":false,"code":"not_exposed"}`)
		case 2:
			reply(st, `{"ok":false}`)
		case 3:
			reply(st, strings.Repeat("x", 5000))
		default:
			// 永不应答，直到对端关闭
			_, _ = io.Copy(io.Discard, st)
		}
	}
	h := newHarness(t, drv, nil, nil)
	ctx := context.Background()
	orb, _ := h.readyOrb(drv, "")
	require.NoError(t, h.svc.EnsureUsable(ctx, orb.ID, FeaturePortal, false, 0))

	conn, err := h.svc.DialPortal(ctx, orb.ID, 8080)
	require.NoError(t, err)
	banner := make([]byte, 6)
	_, err = io.ReadFull(conn, banner)
	require.NoError(t, err)
	assert.Equal(t, "BANNER", string(banner))
	payload := strings.Repeat("portal-bytes;", 1<<14)
	go func() { _, _ = io.WriteString(conn, payload) }()
	echo := make([]byte, len(payload))
	_, err = io.ReadFull(conn, echo)
	require.NoError(t, err)
	assert.Equal(t, payload, string(echo))
	require.NoError(t, conn.Close())

	for port, code := range map[int]string{7811: "forbidden_port", 9999: "not_exposed", 2: "failed", 1: "timeout"} {
		_, err := h.svc.DialPortal(ctx, orb.ID, port)
		var de *PortalDialError
		require.ErrorAs(t, err, &de, "port %d", port)
		assert.Equal(t, code, de.Code, "port %d", port)
	}
	_, err = h.svc.DialPortal(ctx, orb.ID, 7811)
	var de *PortalDialError
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "egress port", de.Message)
	_, err = h.svc.DialPortal(ctx, orb.ID, 3)
	assert.ErrorIs(t, err, link.ErrLineTooLong, "超长应答行")

	_, err = h.svc.DialPortal(ctx, "orb-missing", 8080)
	assert.ErrorIs(t, err, ErrOrbNotReady)

	// 旧 orbd：hello 无 portal 特性，拨号拒绝，也不会收到 portal.configure
	drv.mu.Lock()
	drv.features = []string{FeatureFS}
	drv.mu.Unlock()
	oldOrb, _ := h.readyOrb(drv, "")
	_, err = h.svc.DialPortal(ctx, oldOrb.ID, 8080)
	assert.ErrorIs(t, err, ErrOrbdOutdated)
	assert.ErrorIs(t, h.svc.EnsureUsable(ctx, oldOrb.ID, FeaturePortal, false, 0), ErrOrbdOutdated)
	assert.Empty(t, drv.orbd(oldOrb.ID).callsOf("portal.configure"))
}

// TouchUserCoalesced：同一 orb 在 min(1s, user_idle/2) 内至多写一次库；不同 orb 互不影响。
func TestTouchUserCoalesced(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	mkOrb := func() string {
		id := ids.Orb()
		require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: "small", State: StateReady, StateChangedAt: 1, TokenHash: "h-" + id, CreatedAt: 1}))
		return id
	}
	lastTouch := func(orbID string) int64 {
		o, err := st.Q.GetOrb(ctx, orbID)
		require.NoError(t, err)
		if o.LastUserInteractionAt == nil {
			return 0
		}
		return *o.LastUserInteractionAt
	}
	t0 := time.UnixMilli(1_700_000_000_000)
	for _, tc := range []struct {
		name     string
		userIdle time.Duration
		window   time.Duration
	}{{"1s 封顶", time.Hour, time.Second}, {"user_idle/2", time.Second, 500 * time.Millisecond}} {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(Config{
				Idle:     config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(tc.userIdle), StopAfterFrozen: config.Duration(time.Hour)},
				TokenKey: make([]byte, 32),
			}, st, jobs.New(st), nil, nil)
			t.Cleanup(svc.Close)
			now := t0
			svc.now = func() time.Time { return now }
			a, b := mkOrb(), mkOrb()

			svc.TouchUserCoalesced(ctx, a)
			assert.Equal(t, t0.UnixMilli(), lastTouch(a))
			now = t0.Add(tc.window - time.Millisecond)
			svc.TouchUserCoalesced(ctx, a)
			assert.Equal(t, t0.UnixMilli(), lastTouch(a), "窗口内不写库")
			svc.TouchUserCoalesced(ctx, b)
			assert.Equal(t, now.UnixMilli(), lastTouch(b), "其它 orb 不受影响")
			now = t0.Add(tc.window)
			svc.TouchUserCoalesced(ctx, a)
			assert.Equal(t, now.UnixMilli(), lastTouch(a), "窗口过后再写一次")
		})
	}
}

// B3：spawn 完成后 advance 发起的 setup 也要等本 link 的 portal.configure。onHello 被钩子卡在 configure
// 之前时，advance 的 runPhase 必须停在准备屏障上，而不是先发 setup.run。
func TestPortalConfigureBarrierOnAdvance(t *testing.T) {
	drv := newFakeDriver()
	drv.features = []string{FeatureServices, FeaturePortal}
	setupSeen := make(chan struct{}, 1)
	drv.onCall = func(method string) {
		if method == "setup.run" {
			select {
			case setupSeen <- struct{}{}:
			default:
			}
		}
	}
	h := newHarness(t, drv, nil, func(c *Config) { c.PortalURLTemplate = "https://t-{code}-p{port}.orb.test/" })
	helloBlocked, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	h.svc.hookHelloStart = func(string) {
		if first.CompareAndSwap(false, true) {
			close(helloBlocked)
			<-release
		}
	}
	phaseWaiting := make(chan struct{}, 1)
	h.svc.hookAwaitPrepared = func(string) {
		select {
		case phaseWaiting <- struct{}{}:
		default:
		}
	}
	orb, err := h.svc.Create(context.Background(), CreateParams{})
	require.NoError(t, err)
	<-helloBlocked
	select {
	case <-phaseWaiting:
	case <-setupSeen:
		t.Fatal("setup.run 在 portal.configure 之前发出")
	case <-time.After(10 * time.Second):
		t.Fatal("advance 没有进入 runPhase")
	}
	cur, err := h.st.Q.GetOrb(context.Background(), orb.ID)
	require.NoError(t, err)
	assert.Equal(t, StateConnecting, cur.State, "屏障之前不进入 setting_up")
	close(release)
	h.waitState(orb.ID, StateReady, 10*time.Second)
	methods := drv.orbd(orb.ID).methods()
	require.GreaterOrEqual(t, len(methods), 2)
	assert.Equal(t, []string{"portal.configure", "setup.run"}, methods[:2])
}

// B5：合并时间只在写库成功后发布；窗口内的后来者等在途写入完成才返回；写失败不算已写，由后来者重写。
func TestTouchUserCoalescedInflight(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	mkOrb := func() string {
		id := ids.Orb()
		require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: "small", State: StateReady, StateChangedAt: 1, TokenHash: "h-" + id, CreatedAt: 1}))
		return id
	}
	lastTouch := func(orbID string) int64 {
		o, err := st.Q.GetOrb(ctx, orbID)
		if err != nil || o.LastUserInteractionAt == nil {
			return 0
		}
		return *o.LastUserInteractionAt
	}
	svc := New(Config{
		Idle:     config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(time.Hour)},
		TokenKey: make([]byte, 32),
	}, st, jobs.New(st), nil, nil)
	t.Cleanup(svc.Close)
	t0 := time.UnixMilli(1_700_000_000_000)
	svc.now = func() time.Time { return t0 }

	// blockFirstWrite 让 orbID 的第一次写库停在写之前，直到 release 关闭；onBlocked 在停住时调用。
	var blocked sync.Map
	blockFirstWrite := func(orbID string, release <-chan struct{}, onBlocked func()) {
		blocked.Store(orbID, func() { onBlocked(); <-release })
	}
	svc.hookTouchWrite = func(_ context.Context, orbID string) {
		if fn, ok := blocked.LoadAndDelete(orbID); ok {
			fn.(func())()
		}
	}
	waiting := make(chan string, 4)
	svc.hookTouchWait = func(orbID string) { waiting <- orbID }

	// 首个写入在途时，后来者在写库成功之前不返回。
	a := mkOrb()
	entered, release := make(chan struct{}), make(chan struct{})
	blockFirstWrite(a, release, func() { close(entered) })
	aDone := make(chan struct{})
	go func() { svc.TouchUserCoalesced(ctx, a); close(aDone) }()
	<-entered
	bSaw := make(chan int64, 1)
	go func() { svc.TouchUserCoalesced(ctx, a); bSaw <- lastTouch(a) }()
	select {
	case id := <-waiting:
		assert.Equal(t, a, id)
	case v := <-bSaw:
		t.Fatalf("后来者在交互写库之前就返回了（库中为 %d）", v)
	}
	close(release)
	assert.Equal(t, t0.UnixMilli(), <-bSaw, "后来者返回时交互已持久化")
	<-aDone

	// 在途写入失败（ctx 取消）：等待者重新写库。
	b := mkOrb()
	cctx, cancel := context.WithCancel(ctx)
	entered2, release2 := make(chan struct{}), make(chan struct{})
	blockFirstWrite(b, release2, func() { close(entered2) })
	b1Done := make(chan struct{})
	go func() { svc.TouchUserCoalesced(cctx, b); close(b1Done) }()
	<-entered2
	b2Saw := make(chan int64, 1)
	go func() { svc.TouchUserCoalesced(ctx, b); b2Saw <- lastTouch(b) }()
	assert.Equal(t, b, <-waiting)
	cancel()
	close(release2)
	<-b1Done
	assert.Equal(t, t0.UnixMilli(), <-b2Saw, "在途写入失败后由等待者写库")

	// 写失败不记为已写：同一窗口内的下一次调用照常写库。
	c := mkOrb()
	dead, kill := context.WithCancel(ctx)
	kill()
	svc.TouchUserCoalesced(dead, c)
	assert.Zero(t, lastTouch(c))
	svc.TouchUserCoalesced(ctx, c)
	assert.Equal(t, t0.UnixMilli(), lastTouch(c), "失败的写入没有占用合并窗口")
}
