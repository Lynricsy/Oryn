package desktop

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// exports 返回 orbd 收到的导出请求数。
func (o *fakeOrbd) exports() int {
	n := 0
	for _, r := range o.requests() {
		if strings.HasPrefix(r, "GET /desktop/profile/export") {
			n++
		}
	}
	return n
}

// 审查 1：A 的保存已读到「独占、围栏 1」，在复核租约之前被 B 接管（降为只读、围栏 2），B 随后冻结释放租约；
// A 的保存继续时不能把空着的租约重新领成新围栏：不导出、修订不变、A 不成为持有者。
func TestLateSaveCannotReacquireAfterTakeover(t *testing.T) {
	e := newEnv(t)
	oldTO := takeoverSaveTimeout
	takeoverSaveTimeout = 50 * time.Millisecond
	t.Cleanup(func() { takeoverSaveTimeout = oldTO })
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "late-on-A")

	reached, resume := make(chan struct{}), make(chan struct{})
	e.svc.beforeLease = func(orbID string) {
		if orbID == a {
			close(reached)
			<-resume
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.svc.BeforeFreeze(e.ctx, a)
	}()
	<-reached

	b := e.orb("B")
	e.bind(b, p, "")
	require.NoError(t, e.svc.SetOrbProfile(e.ctx, b, p, apigen.BrowserProfileModeTakeover))
	require.Equal(t, b, *e.get(p).HolderOrbID)
	require.Equal(t, apigen.OrbDesktopProfileStatusReadonly, e.view(a).Status)
	// B 没有产生新修订就冻结：租约释放
	e.setState(b, orbs.StateFrozen)
	e.fo.emit(orbs.Event{OrbID: b, Kind: orbs.EventState, From: orbs.StateFreezing, To: orbs.StateFrozen})
	require.Equal(t, b, <-e.released)
	require.Nil(t, e.get(p).HolderOrbID)

	close(resume)
	<-done
	got := e.get(p)
	assert.EqualValues(t, 0, got.Revision, "迟到的保存没有产生修订")
	assert.Nil(t, got.HolderOrbID, "A 没有重新成为持有者")
	assert.EqualValues(t, 2, got.Fence)
	assert.Zero(t, e.fo.orbd(a).exports(), "A 没有被导出")
	assert.EqualValues(t, 0, e.desk(a).Fence)
	assert.Empty(t, e.files(p))
}

// 合法的「冻结释放后停机前保存」：期间没有别的 orb 持有过，A 重新取得租约（代数 +1）并保存。
func TestShutdownAfterFreezeReacquiresOwnLease(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "data-before-stop")
	e.setState(a, orbs.StateFrozen)
	e.fo.emit(orbs.Event{OrbID: a, Kind: orbs.EventState, From: orbs.StateFreezing, To: orbs.StateFrozen})
	require.Equal(t, a, <-e.released)
	require.Nil(t, e.get(p).HolderOrbID)

	e.setState(a, orbs.StateStopping)
	e.svc.BeforeShutdown(e.ctx, a)
	got := e.get(p)
	assert.EqualValues(t, 1, got.Revision)
	require.NotNil(t, got.HolderOrbID)
	assert.Equal(t, a, *got.HolderOrbID)
	assert.EqualValues(t, 2, got.Fence)
	assert.EqualValues(t, 2, e.desk(a).Fence)
	assert.EqualValues(t, 1, e.desk(a).LocalRev)

	// 期间别的 orb 正常持有过（代数变了）：A 的保存不再被允许
	e.setState(a, orbs.StateStopped)
	e.fo.emit(orbs.Event{OrbID: a, Kind: orbs.EventState, From: orbs.StateStopping, To: orbs.StateStopped})
	require.Equal(t, a, <-e.released)
	c := e.orb("C")
	e.bind(c, p, "")
	e.svc.BeforeReady(e.ctx, c)
	e.setState(c, orbs.StateStopped)
	e.fo.emit(orbs.Event{OrbID: c, Kind: orbs.EventState, From: orbs.StateStopping, To: orbs.StateStopped})
	require.Equal(t, c, <-e.released)
	e.dirty(a, "stale-a")
	n := e.fo.orbd(a).exports()
	e.svc.BeforeShutdown(e.ctx, a)
	assert.Equal(t, n, e.fo.orbd(a).exports())
	assert.EqualValues(t, 1, e.get(p).Revision)
	assert.Nil(t, e.get(p).HolderOrbID)
}

// 审查 4：C 的接管第一次 CAS 之前，B 抢先接管并产生了新的登录态；C 重试时先保存 B，装载到的是 B 的数据。
func TestTakeoverRetrySavesNewHolder(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "A-data")

	c := e.orb("C")
	e.bind(c, p, apigen.BrowserProfileModeTakeover)
	reached, resume := make(chan struct{}), make(chan struct{})
	e.svc.beforeSteal = func(orbID string, attempt int) {
		if orbID == c && attempt == 0 {
			close(reached)
			<-resume
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.svc.BeforeReady(e.ctx, c)
	}()
	<-reached
	require.EqualValues(t, 1, e.get(p).Revision, "A 先被保存")

	b := e.orb("B")
	e.bind(b, p, apigen.BrowserProfileModeTakeover)
	e.svc.BeforeReady(e.ctx, b)
	require.Equal(t, b, *e.get(p).HolderOrbID)
	e.dirty(b, "B-cookie")

	close(resume)
	<-done
	got := e.get(p)
	require.NotNil(t, got.HolderOrbID)
	assert.Equal(t, c, *got.HolderOrbID)
	assert.EqualValues(t, 2, got.Revision, "B 在被接管前保存了一次")
	assert.Equal(t, "B-cookie", string(e.fo.orbd(c).lastPut()))
	assert.Equal(t, apigen.OrbDesktopProfileStatusReadonly, e.view(b).Status)
}

// 审查 2：B 接管 A 时发给 A 的 profile.mode 超时未确认；A 随后反向接管并以独占装载，之后旧消息才落到 A 的 orbd。
// 控制面据 desktop.state 以库为准改回独占，之后保存照常。反方向（库里已降为只读、orbd 仍报独占）重发 profile.mode。
func TestLateProfileModeCorrected(t *testing.T) {
	e := newEnv(t)
	oldTO := takeoverSaveTimeout
	takeoverSaveTimeout = time.Second
	t.Cleanup(func() { takeoverSaveTimeout = oldTO })
	p := e.profile("work")
	a, b := e.orb("A"), e.orb("B")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.fo.mu.Lock()
	e.fo.deferModes = true
	e.fo.mu.Unlock()

	e.bind(b, p, "")
	require.NoError(t, e.svc.SetOrbProfile(e.ctx, b, p, apigen.BrowserProfileModeTakeover))
	require.Equal(t, b, *e.get(p).HolderOrbID)
	// A 反向接管：完整装载、本地独占
	require.NoError(t, e.svc.SetOrbProfile(e.ctx, a, p, apigen.BrowserProfileModeTakeover))
	require.Equal(t, a, *e.get(p).HolderOrbID)
	oa := e.fo.orbd(a)
	oa.mu.Lock()
	require.Equal(t, "exclusive", oa.mode)
	oa.mu.Unlock()

	// 迟到的降级（发给 A 的那条）落地：orbd 变只读并上报
	// deliverModes 依次投递发给 A（B 接管时）与发给 B（A 反向接管时）的两条，纠正队列按序处理
	e.fo.deliverModes()
	require.Equal(t, a, <-e.fixed)
	require.Equal(t, b, <-e.fixed)
	oa.mu.Lock()
	assert.Equal(t, "exclusive", oa.mode, "A 的本地使用方式被改回独占")
	oa.mu.Unlock()
	assert.Contains(t, last(oa.requests()), "keep=1")
	assert.Contains(t, last(oa.requests()), "mode=exclusive")
	assert.Equal(t, apigen.OrbDesktopProfileStatusAttached, e.view(a).Status)
	// 之后的保存照常
	e.dirty(a, "A-after-fix")
	e.svc.BeforeFreeze(e.ctx, a)
	assert.EqualValues(t, 1, e.get(p).Revision)

	// 反方向：B 已降为只读，但其 orbd 仍以独占报告 → 重发 profile.mode（不带接管方信息）
	e.fo.mu.Lock()
	e.fo.deferModes = false
	n := len(e.fo.modes)
	hooks := e.fo.hooks
	e.fo.mu.Unlock()
	ob := e.fo.orbd(b)
	ob.mu.Lock()
	ob.mode = "exclusive"
	ob.mu.Unlock()
	e.fo.report(hooks, b)
	require.Equal(t, b, <-e.fixed)
	e.fo.mu.Lock()
	modes := append([]modeCall(nil), e.fo.modes[n:]...)
	e.fo.mu.Unlock()
	require.Len(t, modes, 1)
	assert.Equal(t, b, modes[0].orbID)
	assert.Equal(t, apigen.BrowserProfileAccessReadonly, modes[0].p.Mode)
	assert.Equal(t, apigen.BrowserProfileReadonlyReasonTakenOver, modes[0].p.Reason)
	assert.Nil(t, modes[0].p.By)
	assert.Nil(t, modes[0].p.ByThreadID)
}

// 审查 5：orynd 在 orb 已离开运行态、释放完成之前重启：新服务启动时补做释放；运行中的持有者不动。
func TestStartupReleasesOfflineLeases(t *testing.T) {
	e := newEnv(t)
	held := map[string]string{}
	for _, state := range []string{orbs.StateFrozen, orbs.StateStopped, orbs.StateFailed, orbs.StateDeleted, orbs.StateReady} {
		p := e.profile("p-" + state)
		o := e.orb("")
		e.bind(o, p, "")
		e.svc.BeforeReady(e.ctx, o)
		require.Equal(t, o, *e.get(p).HolderOrbID)
		e.setState(o, state) // 不发事件：模拟释放任务随旧进程丢失
		held[o] = p
	}
	released := make(chan string, 16)
	svc2, err := newService(Config{DataDir: filepath.Dir(e.dir), Box: e.box}, e.st, newFakeOrbs(e.st), nil, func(s *Service) {
		s.onReleased = func(id string) { released <- id }
	})
	require.NoError(t, err)
	defer svc2.Close()
	for range held {
		select {
		case <-released:
		case <-time.After(10 * time.Second):
			t.Fatal("启动时没有补做租约释放")
		}
	}
	for o, p := range held {
		orb, err := e.st.Q.GetOrb(e.ctx, o)
		require.NoError(t, err)
		if orb.State == orbs.StateReady {
			assert.Equal(t, o, *e.get(p).HolderOrbID, "运行中的持有者不释放")
		} else {
			assert.Nil(t, e.get(p).HolderOrbID, orb.State)
		}
	}
}

// orbd 的导出预检超限（413 too_large）→ save_error=too_large、修订不变；手动保存 → 413 profile_too_large。
func TestExportTooLargeFromOrbd(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "v1")
	e.svc.BeforeShutdown(e.ctx, a)
	require.EqualValues(t, 1, e.get(p).Revision)

	e.fo.orbd(a).exportFail = "too_large"
	e.dirty(a, "v2-huge")
	e.svc.BeforeShutdown(e.ctx, a)
	got := e.get(p)
	assert.EqualValues(t, 1, got.Revision)
	require.NotNil(t, got.SaveError)
	assert.Equal(t, "too_large", *got.SaveError)
	assert.Equal(t, []string{"1.oss"}, e.files(p))
	_, err := e.svc.Save(e.ctx, a)
	var de *Error
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "profile_too_large", de.Code)
}
