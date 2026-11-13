package portal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func (e *env) share(port int) (db.PortalShare, bool) {
	e.t.Helper()
	sh, err := e.st.Q.GetPortalShare(e.ctx, db.GetPortalShareParams{OrbID: e.orbID, Port: int64(port)})
	if errors.Is(err, sql.ErrNoRows) {
		return sh, false
	}
	require.NoError(e.t, err)
	return sh, true
}

// 4.7：公开 → 匿名 200（owner=no、noindex）、其它端口仍 302；续期改 expires_at；终止立即断开匿名 WebSocket。
func TestShareLifecycle(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()

	_, err := e.svc.SetShare(e.ctx, e.orbID, portC, time.Hour)
	assert.ErrorIs(t, err, ErrNotFound, "端口不是已声明的 portal")

	op, err := e.svc.SetShare(e.ctx, e.orbID, portA, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, apigen.OrbPortal{Port: portA, Service: "web", Links: []apigen.OrbPortalLink{{Title: "web", Path: "/"}},
		Public: &apigen.OrbPortalPublic{ExpiresAt: now.Add(time.Hour).UnixMilli()}}, op)
	sh, ok := e.share(portA)
	require.True(t, ok)
	assert.Equal(t, now.Add(time.Hour).UnixMilli(), sh.ExpiresAt)
	first := e.timers.last()
	require.NotNil(t, first)
	assert.Equal(t, time.Hour, first.d)
	_, _, refreshes := e.orbs.snapshot()
	assert.Equal(t, []string{e.orbID}, refreshes)

	resp := e.get(portA, "/echo", "Accept", "text/html")
	assert.Equal(t, "noindex, nofollow", resp.Header.Get("X-Robots-Tag"))
	assert.Equal(t, "owner=no", readEcho(t, resp).Header.Get(AuthenticatedHeader))
	resp = e.get(portB, "/echo", "Accept", "text/html")
	assert.Equal(t, http.StatusFound, resp.StatusCode, "只公开该端口")
	_, touches, _ := e.orbs.snapshot()
	assert.Equal(t, 1, touches, "公开访客计交互")

	// 续期：同一接口，到期时间后移，旧定时器停掉，已建立的匿名连接不断
	ws, _, err := e.dialWS(portA, nil)
	require.NoError(t, err)
	defer ws.CloseNow()
	echoOnce(t, ws, "a")
	e.clock.Advance(10 * time.Minute)
	op, err = e.svc.SetShare(e.ctx, e.orbID, portA, 3*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, e.clock.Now().Add(3*time.Hour).UnixMilli(), op.Public.ExpiresAt)
	sh, _ = e.share(portA)
	assert.Equal(t, op.Public.ExpiresAt, sh.ExpiresAt)
	assert.True(t, first.isStopped())
	assert.Equal(t, 3*time.Hour, e.timers.last().d)
	echoOnce(t, ws, "b")

	// 终止：匿名 WebSocket 立即断开；之后匿名访问回到握手
	require.NoError(t, e.svc.EndShare(e.ctx, e.orbID, portA))
	requireClosed(t, ws)
	_, ok = e.share(portA)
	assert.False(t, ok)
	assert.True(t, e.timers.last().isStopped())
	assert.Equal(t, http.StatusFound, e.get(portA, "/", "Accept", "text/html").StatusCode)
	require.NoError(t, e.svc.EndShare(e.ctx, e.orbID, portA), "幂等")

	// 所有者的连接不受分享终止影响
	_, err = e.svc.SetShare(e.ctx, e.orbID, portA, time.Hour)
	require.NoError(t, err)
	owner, _, err := e.dialWS(portA, headers(e.bearer()...))
	require.NoError(t, err)
	defer owner.CloseNow()
	require.NoError(t, e.svc.EndShare(e.ctx, e.orbID, portA))
	echoOnce(t, owner, "still")
}

// 到期定时器（手动触发）：删除分享、断开匿名连接、推送；即使定时器迟到，到期那一刻新请求就失效。
func TestShareExpiry(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.SetShare(e.ctx, e.orbID, portA, time.Hour)
	require.NoError(t, err)
	tm := e.timers.last()
	ws, _, err := e.dialWS(portA, nil)
	require.NoError(t, err)
	defer ws.CloseNow()
	echoOnce(t, ws, "x")

	// 到期但定时器尚未触发：新请求已失效（每个匿名请求都比较 expires_at）
	e.clock.Advance(time.Hour)
	resp := e.get(portA, "/echo")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	tm.f()
	requireClosed(t, ws)
	_, ok := e.share(portA)
	assert.False(t, ok, "到期行被删除")
	_, _, refreshes := e.orbs.snapshot()
	assert.Equal(t, []string{e.orbID, e.orbID}, refreshes)

	// 被续期后旧定时器触发：不删除，按新到期时间重装
	_, err = e.svc.SetShare(e.ctx, e.orbID, portB, time.Hour)
	require.NoError(t, err)
	stale := e.timers.last()
	require.NoError(t, e.st.Q.UpsertPortalShare(e.ctx, db.UpsertPortalShareParams{
		OrbID: e.orbID, Port: portB, CreatedAt: 1, ExpiresAt: e.clock.Now().Add(2 * time.Hour).UnixMilli(),
	}))
	e.clock.Advance(time.Hour)
	stale.f()
	_, ok = e.share(portB)
	assert.True(t, ok)
	assert.Equal(t, time.Hour, e.timers.last().d, "按剩余时间重装")
	assert.NotSame(t, stale, e.timers.last())
	stale.f() // 已被替换的定时器再次触发：无操作
	_, ok = e.share(portB)
	assert.True(t, ok)
}

// Run 启动时删除已过期的分享、为未过期的装定时器。
func TestRunRebuildsTimers(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	require.NoError(t, e.st.Q.UpsertPortalShare(e.ctx, db.UpsertPortalShareParams{OrbID: e.orbID, Port: portA, CreatedAt: 1, ExpiresAt: now.Add(-time.Second).UnixMilli()}))
	require.NoError(t, e.st.Q.UpsertPortalShare(e.ctx, db.UpsertPortalShareParams{OrbID: e.orbID, Port: portB, CreatedAt: 1, ExpiresAt: now.Add(2 * time.Hour).UnixMilli()}))

	timers := &fakeTimers{armed: make(chan *fakeTimer, 4)}
	svc := New(Config{Base: e.base, Now: e.clock.Now, AfterFunc: timers.AfterFunc}, e.st, e.orbs)
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	var tm *fakeTimer
	select {
	case tm = <-timers.armed:
	case <-time.After(5 * time.Second):
		t.Fatal("Run 没有装定时器")
	}
	assert.Equal(t, 2*time.Hour, tm.d)
	_, ok := e.share(portA)
	assert.False(t, ok, "过期的分享被删除（先于未过期的处理）")
	_, ok = e.share(portB)
	assert.True(t, ok)
	cancel()
	<-done
	assert.True(t, tm.isStopped(), "Run 结束时停掉定时器")
}

// 4.8：Revoke 后所有者的 WebSocket 断开、下一次请求回到握手。
func TestRevoke(t *testing.T) {
	e := newEnv(t)
	cookie := e.login(portA)
	ws, _, err := e.dialWS(portA, headers(portalCookie(cookie)...))
	require.NoError(t, err)
	defer ws.CloseNow()
	echoOnce(t, ws, "x")
	dev, _, err := e.dialWS(portA, headers(e.bearer()...))
	require.NoError(t, err)
	defer dev.CloseNow()

	// 登出：库里删除网页会话（portal 会话级联删除）并发布吊销
	hash := hashOf(e.webCookie)
	require.NoError(t, e.st.Q.DeleteWebSession(e.ctx, hash))
	e.svc.Revoke("web", hash)
	requireClosed(t, ws)
	echoOnce(t, dev, "device unaffected")
	resp := e.get(portA, "/", append(portalCookie(cookie), "Accept", "text/html")...)
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, e.authURL(portA, "/"), resp.Header.Get("Location"))

	// 设备吊销
	e.svc.Revoke("device", deviceID(t, e))
	requireClosed(t, dev)
}

func deviceID(t *testing.T, e *env) string {
	t.Helper()
	d, err := e.st.Q.GetDeviceByTokenHash(e.ctx, hashOf(e.device))
	require.NoError(t, err)
	return d.ID
}

// 唤醒（4.2 第 5 步）。
func TestWake(t *testing.T) {
	notReady := fmt.Errorf("%w: frozen", orbs.ErrOrbNotReady)

	t.Run("导航回过渡页并唤醒一次", func(t *testing.T) {
		e := newEnv(t)
		e.orbs.set(func(f *fakeOrbs) { f.usable = notReady })
		resp := e.get(portA, "/", append(e.bearer(), "Accept", "text/html")...)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		assert.Equal(t, "2", resp.Header.Get("Retry-After"))
		page := body(t, resp)
		assert.Contains(t, page, "正在唤醒")
		assert.Contains(t, page, `<meta http-equiv="refresh" content="2">`)
		assert.Contains(t, page, "prefers-color-scheme")
		calls, touches, _ := e.orbs.snapshot()
		assert.Equal(t, []ensureCall{{false, 0}, {true, 0}}, calls)
		assert.Equal(t, 1, touches)
	})

	t.Run("非导航阻塞到放行后 200", func(t *testing.T) {
		e := newEnv(t)
		gate, waiting := make(chan struct{}), make(chan struct{})
		e.orbs.set(func(f *fakeOrbs) { f.usable, f.gate, f.waiting = notReady, gate, waiting })
		got := make(chan *http.Response, 1)
		go func() { got <- e.get(portA, "/echo", e.bearer()...) }()
		select {
		case <-waiting:
		case <-time.After(5 * time.Second):
			t.Fatal("没有等待唤醒")
		}
		close(gate)
		resp := <-got
		assert.Equal(t, "owner=yes", readEcho(t, resp).Header.Get(AuthenticatedHeader))
		calls, _, _ := e.orbs.snapshot()
		assert.Equal(t, []ensureCall{{false, 0}, {true, WakeWait}}, calls)
	})

	t.Run("等待超时 503", func(t *testing.T) {
		e := newEnv(t)
		e.orbs.set(func(f *fakeOrbs) { f.usable = notReady })
		resp := e.get(portA, "/echo", e.bearer()...)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		assert.Equal(t, "3", resp.Header.Get("Retry-After"))
		assert.Equal(t, "orb_unavailable", errCode(t, resp))
	})

	t.Run("镜像里没有该端口：404 且不唤醒", func(t *testing.T) {
		e := newEnv(t)
		e.orbs.set(func(f *fakeOrbs) { f.usable = notReady })
		resp := e.get(portC, "/", append(e.bearer(), "Accept", "text/html")...)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		calls, _, _ := e.orbs.snapshot()
		assert.Equal(t, []ensureCall{{false, 0}}, calls)
	})

	t.Run("orb failed：503 不唤醒", func(t *testing.T) {
		e := newEnv(t)
		require.NoError(t, e.st.Q.SetOrbState(e.ctx, db.SetOrbStateParams{State: orbs.StateFailed, StateChangedAt: 1, ID: e.orbID}))
		e.orbs.set(func(f *fakeOrbs) { f.usable = notReady })
		resp := e.get(portA, "/", e.bearer()...)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		assert.Equal(t, "orb_unavailable", errCode(t, resp))
		calls, _, _ := e.orbs.snapshot()
		assert.Equal(t, []ensureCall{{false, 0}}, calls)
	})

	t.Run("orbd 过旧 409、orb 已删除 410", func(t *testing.T) {
		e := newEnv(t)
		e.orbs.set(func(f *fakeOrbs) { f.usable = orbs.ErrOrbdOutdated })
		resp := e.get(portA, "/", append(e.bearer(), "Accept", "text/html")...)
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
		assert.Contains(t, body(t, resp), "orbd_outdated")
		e.orbs.set(func(f *fakeOrbs) { f.usable = orbs.ErrOrbGone })
		resp = e.get(portA, "/", e.bearer()...)
		assert.Equal(t, http.StatusGone, resp.StatusCode)
		assert.Equal(t, "orb_deleted", errCode(t, resp))
	})

	t.Run("未授权请求不计交互也不唤醒", func(t *testing.T) {
		e := newEnv(t)
		e.orbs.set(func(f *fakeOrbs) { f.usable = notReady })
		_ = e.get(portA, "/", "Accept", "text/html")
		_ = e.get(portA, "/")
		_ = e.get(portA, "/__oryn/login")
		calls, touches, _ := e.orbs.snapshot()
		assert.Empty(t, calls)
		assert.Zero(t, touches)
	})
}

// 并发名额：orb 级耗尽 → 503 portal_busy；匿名端口级耗尽只影响匿名访客。
func TestConcurrencyLimits(t *testing.T) {
	e := newEnv(t)
	e.svc.reg.maxOrb, e.svc.reg.maxAnon = 2, 1
	_, err := e.svc.SetShare(e.ctx, e.orbID, portA, time.Hour)
	require.NoError(t, err)

	anon, _, err := e.dialWS(portA, nil)
	require.NoError(t, err)
	defer anon.CloseNow()
	echoOnce(t, anon, "a")
	resp := e.get(portA, "/echo")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "portal_busy", errCode(t, resp))

	owner, _, err := e.dialWS(portA, headers(e.bearer()...))
	require.NoError(t, err)
	defer owner.CloseNow()
	echoOnce(t, owner, "o")
	resp = e.get(portB, "/echo", e.bearer()...)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "orb 级名额跨端口共享")
	assert.Equal(t, "portal_busy", errCode(t, resp))

	// 释放后恢复
	require.NoError(t, owner.Close(1000, ""))
	require.Eventually(t, func() bool {
		r := e.get(portB, "/echo", e.bearer()...)
		ok := r.StatusCode == http.StatusOK
		_ = r.Body.Close()
		return ok
	}, 5*time.Second, 10*time.Millisecond)
}

// registry：判定与登记之间发生的吊销让登记失败（errStale），不会漏掉取消。
func TestRegistryEpoch(t *testing.T) {
	g := newRegistry(10, 10)
	ep := g.current()
	g.cancelKey("web/x")
	_, err := g.enter(ep, "orb", "web/x", false, func() {})
	assert.ErrorIs(t, err, errStale)
	canceled := false
	leave, err := g.enter(g.current(), "orb", "web/x", false, func() { canceled = true })
	require.NoError(t, err)
	g.cancelKey("web/y")
	assert.False(t, canceled)
	g.cancelKey("web/x")
	assert.True(t, canceled)
	leave()
	assert.Empty(t, g.byKey)
	assert.Empty(t, g.perOrb)
}

func TestShareDuration(t *testing.T) {
	for in, want := range map[apigen.PortalShareDuration]time.Duration{"1h": time.Hour, "3h": 3 * time.Hour, "1d": 24 * time.Hour, "7d": 7 * 24 * time.Hour} {
		d, ok := ShareDuration(in)
		assert.True(t, ok)
		assert.Equal(t, want, d)
	}
	_, ok := ShareDuration("2h")
	assert.False(t, ok)
}

// contention 让 lock 在键锁被占用、即将阻塞时发信号（屏障：证明并发操作已排在键锁后面）。
func contention(e *env) <-chan struct{} {
	ch := make(chan struct{}, 1)
	e.svc.locks.contended = func(shareKey) {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return ch
}

// renewAsync 在后台续期 portA，返回结果通道。
func renewAsync(e *env, d time.Duration) chan error {
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.SetShare(e.ctx, e.orbID, portA, d)
		done <- err
	}()
	return done
}

// waitContendedOrDone 等到续期排在键锁后面；若续期直接完成（没有串行化），把结果放回通道。
func waitContendedOrDone(t *testing.T, contended <-chan struct{}, done chan error) {
	t.Helper()
	select {
	case <-contended:
	case err := <-done:
		done <- err
	case <-time.After(5 * time.Second):
		t.Fatal("续期既没有完成也没有等键锁")
	}
}

// B1：终止删行之后、停定时器之前并发续期。续期必须排在终止之后，新一代定时器不被旧终止停掉，
// 新一代的匿名 WebSocket 到新期限立即断开。
func TestShareEndRenewSerialized(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.SetShare(e.ctx, e.orbID, portA, time.Hour)
	require.NoError(t, err)
	contended := contention(e)
	var done chan error
	e.svc.hookEndShareDeleted = func() {
		done = renewAsync(e, 3*time.Hour)
		waitContendedOrDone(t, contended, done)
	}
	require.NoError(t, e.svc.EndShare(e.ctx, e.orbID, portA))
	e.svc.hookEndShareDeleted = nil
	require.NoError(t, <-done)

	exp := e.clock.Now().Add(3 * time.Hour).UnixMilli()
	sh, ok := e.share(portA)
	require.True(t, ok, "续期排在终止之后：分享存在")
	assert.Equal(t, exp, sh.ExpiresAt)
	tm := e.timers.last()
	assert.Equal(t, 3*time.Hour, tm.d)
	require.False(t, tm.isStopped(), "旧终止不能停掉新一代定时器")

	ws, _, err := e.dialWS(portA, nil)
	require.NoError(t, err)
	defer ws.CloseNow()
	echoOnce(t, ws, "new")
	e.clock.Advance(3 * time.Hour)
	tm.f()
	requireClosed(t, ws)
	_, ok = e.share(portA)
	assert.False(t, ok)
}

// B2：到期回调读到过期行后、删除之前，行被改写为新期限：只删读到的那一代，新行、定时器与匿名连接保留。
func TestShareExpiryKeepsRenewedRow(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.SetShare(e.ctx, e.orbID, portA, time.Hour)
	require.NoError(t, err)
	tm := e.timers.last()
	ws, _, err := e.dialWS(portA, nil)
	require.NoError(t, err)
	defer ws.CloseNow()
	echoOnce(t, ws, "x")

	e.clock.Advance(time.Hour)
	exp := e.clock.Now().Add(2 * time.Hour).UnixMilli()
	e.svc.hookFireRead = func() {
		e.svc.hookFireRead = nil
		require.NoError(t, e.st.Q.UpsertPortalShare(e.ctx, db.UpsertPortalShareParams{
			OrbID: e.orbID, Port: portA, CreatedAt: 1, ExpiresAt: exp,
		}))
	}
	tm.f()
	sh, ok := e.share(portA)
	require.True(t, ok, "改写后的新一代不被删除")
	assert.Equal(t, exp, sh.ExpiresAt)
	next := e.timers.last()
	assert.NotSame(t, tm, next)
	assert.Equal(t, 2*time.Hour, next.d, "按新期限重装")
	assert.False(t, next.isStopped())
	echoOnce(t, ws, "still")
}

// B1/B2：到期回调读到过期行后并发续期：续期排在回调之后，最终库与定时器都是新一代。
func TestShareExpiryRenewSerialized(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.SetShare(e.ctx, e.orbID, portA, time.Hour)
	require.NoError(t, err)
	tm := e.timers.last()
	e.clock.Advance(time.Hour)
	contended := contention(e)
	var done chan error
	e.svc.hookFireRead = func() {
		e.svc.hookFireRead = nil
		done = renewAsync(e, time.Hour)
		waitContendedOrDone(t, contended, done)
	}
	tm.f()
	require.NoError(t, <-done)
	sh, ok := e.share(portA)
	require.True(t, ok)
	assert.Equal(t, e.clock.Now().Add(time.Hour).UnixMilli(), sh.ExpiresAt)
	next := e.timers.last()
	assert.NotSame(t, tm, next)
	assert.False(t, next.isStopped())
}

// B2：清理列出分享后行被续期：带 expires_at 条件的删除不删新一代。
func TestSweepKeepsRenewedRow(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	require.NoError(t, e.st.Q.UpsertPortalShare(e.ctx, db.UpsertPortalShareParams{OrbID: e.orbID, Port: portA, CreatedAt: 1, ExpiresAt: now.Add(-time.Second).UnixMilli()}))
	require.NoError(t, e.st.Q.UpsertPortalShare(e.ctx, db.UpsertPortalShareParams{OrbID: e.orbID, Port: portB, CreatedAt: 1, ExpiresAt: now.Add(-time.Second).UnixMilli()}))
	exp := now.Add(time.Hour).UnixMilli()
	e.svc.hookSweepListed = func() {
		require.NoError(t, e.st.Q.UpsertPortalShare(e.ctx, db.UpsertPortalShareParams{OrbID: e.orbID, Port: portA, CreatedAt: 1, ExpiresAt: exp}))
	}
	e.svc.sweep(e.ctx, false)
	sh, ok := e.share(portA)
	require.True(t, ok, "续期后的行保留")
	assert.Equal(t, exp, sh.ExpiresAt)
	_, ok = e.share(portB)
	assert.False(t, ok, "真正过期的行被删除")
	assert.Empty(t, e.svc.locks.m, "键锁条目回收")
}
