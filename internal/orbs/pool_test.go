package orbs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func defaultPoolCfg() config.Pool {
	return config.Pool{MaxTotal: 2, MaxAge: config.Duration(12 * time.Hour), ActiveWindow: config.Duration(72 * time.Hour)}
}

// newPoolEnv 是不运行 Service.Run 的 M11a 夹具：预热池对账由测试显式调用，结果确定。
func newPoolEnv(t *testing.T, mod func(*Config)) *m11Env {
	return newM11Env(t, []string{FeatureRepoUpdate}, false, func(c *Config) {
		c.Pool = defaultPoolCfg()
		if mod != nil {
			mod(c)
		}
	})
}

// warmProject 建一个 warm_orbs = n、72 小时内有新线程的项目。
func (e *m11Env) warmProject(name string, n int64, mod func(*db.CreateProjectParams)) string {
	e.t.Helper()
	prj := e.project(name, func(p *db.CreateProjectParams) {
		p.WarmOrbs = new(n)
		if mod != nil {
			mod(p)
		}
	})
	e.touchProject(prj)
	return prj
}

// touchProject 给项目插一条刚建的线程（预热池的活跃窗口）。
func (e *m11Env) touchProject(prj string) {
	e.t.Helper()
	id := ids.Thread()
	now := time.Now().UnixMilli()
	require.NoError(e.t, e.st.Q.CreateThread(context.Background(), db.CreateThreadParams{
		Kind: "work", ID: id, ProjectID: &prj, TitleSource: "auto", State: "idle", SessionDir: id, CreatedAt: now, UpdatedAt: now,
	}))
}

func (e *m11Env) pool(prj string) []db.Orb {
	e.t.Helper()
	rows, err := e.st.Q.ListPoolOrbs(context.Background(), &prj)
	require.NoError(e.t, err)
	var out []db.Orb
	for _, o := range rows {
		if poolUsable(o) {
			out = append(out, o)
		}
	}
	return out
}

// warmOne 对账一次并等这台新池 orb 进入 ready / idle。
func (e *m11Env) warmOne(prj string) db.Orb {
	e.t.Helper()
	before := map[string]bool{}
	for _, o := range e.pool(prj) {
		before[o.ID] = true
	}
	e.svc.poolReconcile(context.Background())
	var id string
	for _, o := range e.pool(prj) {
		if !before[o.ID] {
			id = o.ID
		}
	}
	require.NotEmpty(e.t, id, "对账应新建一台池 orb")
	var orb db.Orb
	require.Eventually(e.t, func() bool {
		var err error
		orb, err = e.st.Q.GetOrb(context.Background(), id)
		return err == nil && orb.State == StateReady && derefStr(orb.PoolState) == PoolIdle
	}, 10*time.Second, 20*time.Millisecond)
	return orb
}

// stateEvents 返回 orb 的状态变化（去掉同状态旁注）。
func (e *m11Env) stateEvents(orbID string) []string {
	var out []string
	for _, s := range e.eventSeq(orbID) {
		var from, to string
		for i := range s {
			if s[i] == '>' {
				from, to = s[:i], s[i+1:]
			}
		}
		if from != to {
			out = append(out, s)
		}
	}
	return out
}

func TestPoolWarmAndClaimReady(t *testing.T) {
	e := newPoolEnv(t, nil)
	ctx := context.Background()
	prj := e.warmProject("app", 1, nil)

	p := e.warmOne(prj)
	v, err := View(ctx, e.st.Q, p)
	require.NoError(t, err)
	require.NotNil(t, v.Pool)
	assert.Equal(t, apigen.OrbPoolStateIdle, *v.Pool)
	evs, err := e.st.Q.ListOrbEvents(ctx, p.ID)
	require.NoError(t, err)
	assert.Contains(t, evs[0].Detail, `"pool":true`)
	// 已满：再对账不新建
	e.svc.poolReconcile(ctx)
	assert.Len(t, e.pool(prj), 1)

	// 认领 ready 的池 orb：ready → connecting → resuming → ready，setup.run 带 repo.update，结束后离开池
	got, ok := e.svc.ClaimPool(ctx, CreateParams{ProjectID: prj})
	require.True(t, ok)
	assert.Equal(t, p.ID, got.ID)
	done := e.waitState(p.ID, StateReady, 10*time.Second)
	require.Eventually(t, func() bool {
		o, _ := e.st.Q.GetOrb(ctx, p.ID)
		return o.State == StateReady && o.PoolState == nil
	}, 10*time.Second, 20*time.Millisecond)
	assert.NotNil(t, done.ClaimedAt)
	seq := e.stateEvents(p.ID)
	assert.Equal(t, []string{"ready>connecting", "connecting>resuming", "resuming>ready"}, seq[len(seq)-3:])
	calls := e.setupCalls(p.ID)
	// warming：setup.run + 发布后令牌轮换、重连重跑的 setup.run（跳过）；认领：setup.run
	require.Len(t, calls, 3, "认领后用 setup.run 刷新（而不是 resume.run）")
	require.NotNil(t, calls[2].Repo.Update)
	assert.True(t, *calls[2].Repo.Update)
	assert.NotContains(t, e.drv.orbd(p.ID).methods(), "resume.run")
	v, err = View(ctx, e.st.Q, mustOrb(t, e, p.ID))
	require.NoError(t, err)
	assert.Nil(t, v.Pool)

	// 两个 goroutine 同时认领同一台：恰有一个成功
	p2 := e.warmOne(prj)
	var wg sync.WaitGroup
	results := make([]bool, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = e.svc.ClaimPool(ctx, CreateParams{ProjectID: prj})
		}()
	}
	wg.Wait()
	assert.ElementsMatch(t, []bool{true, false}, results)
	e.waitState(p2.ID, StateReady, 10*time.Second)
}

func mustOrb(t *testing.T, e *m11Env, id string) db.Orb {
	t.Helper()
	o, err := e.st.Q.GetOrb(context.Background(), id)
	require.NoError(t, err)
	return o
}

// 认领 warming 的池 orb：直接离开池，照常走完那一次 setup，不产生第二次 setup.run。
func TestPoolClaimWarming(t *testing.T) {
	e := newPoolEnv(t, nil)
	ctx := context.Background()
	prj := e.warmProject("app", 1, nil)
	block := make(chan struct{})
	e.block.Store(block)
	e.svc.poolReconcile(ctx)
	pool := e.pool(prj)
	require.Len(t, pool, 1)
	id := pool[0].ID
	e.waitState(id, StateSettingUp, 10*time.Second)
	got, ok := e.svc.ClaimPool(ctx, CreateParams{ProjectID: prj})
	require.True(t, ok)
	assert.Equal(t, id, got.ID)
	assert.Nil(t, got.PoolState)
	close(block)
	e.waitState(id, StateReady, 10*time.Second)
	assert.Nil(t, mustOrb(t, e, id).PoolState)
	// 认领不加刷新：唯一的第二次 setup.run 是发布快照后令牌轮换、重连重跑的那次（跳过，不带 repo.update）
	calls := e.setupCalls(id)
	require.Len(t, calls, 2)
	for _, c := range calls {
		assert.Nil(t, c.Repo.Update)
	}
}

// 认领冻结的池 orb：走 waking，刷新后 ready 并离开池。
func TestPoolClaimFrozen(t *testing.T) {
	e := newPoolEnv(t, nil)
	ctx := context.Background()
	prj := e.warmProject("app", 1, nil)
	p := e.warmOne(prj)
	require.NoError(t, e.svc.Pause(ctx, p.ID, PauseFreeze))
	e.waitState(p.ID, StateFrozen, 10*time.Second)
	got, ok := e.svc.ClaimPool(ctx, CreateParams{ProjectID: prj})
	require.True(t, ok)
	assert.Equal(t, p.ID, got.ID)
	require.Eventually(t, func() bool {
		o := mustOrb(t, e, p.ID)
		return o.State == StateReady && o.PoolState == nil
	}, 10*time.Second, 20*time.Millisecond)
	assert.Contains(t, e.stateEvents(p.ID), "frozen>waking")
	calls := e.setupCalls(p.ID)
	require.Len(t, calls, 3, "warming 两次（发布后重连重跑一次）+ 认领刷新一次")
	assert.True(t, *calls[2].Repo.Update)
}

// 不走池：请求了浏览器 Profile、指定了快照、没有项目、配置哈希不匹配。
func TestPoolClaimPreconditions(t *testing.T) {
	e := newPoolEnv(t, nil)
	ctx := context.Background()
	prj := e.warmProject("app", 1, nil)
	e.warmOne(prj)
	for _, p := range []CreateParams{
		{ProjectID: prj, BrowserProfileID: "bpf_x"},
		{ProjectID: prj, SnapshotID: "oryn-snap-x"},
		{},
		{ProjectID: prj, Type: "nope"},
	} {
		_, ok := e.svc.ClaimPool(ctx, p)
		assert.False(t, ok, "%+v", p)
	}
	e.setSetupScript(prj, "changed")
	_, ok := e.svc.ClaimPool(ctx, CreateParams{ProjectID: prj})
	assert.False(t, ok, "配置哈希变了的池 orb 不可认领")
	_, err := e.svc.Create(ctx, CreateParams{ProjectID: prj, Pool: true, BrowserProfileID: "bpf_x"})
	assert.ErrorIs(t, err, ErrInvalidState)
	_, err = e.svc.Create(ctx, CreateParams{Pool: true})
	assert.ErrorIs(t, err, ErrInvalidState)
}

// 改项目 setup 脚本后对账回收旧池 orb 并新建；retiring 不对外暴露；重启后补删 retiring 的池 orb。
func TestPoolReconcileStale(t *testing.T) {
	e := newPoolEnv(t, nil)
	ctx := context.Background()
	prj := e.warmProject("app", 1, nil)
	old := e.warmOne(prj)
	e.setSetupScript(prj, "make deps v2")
	e.svc.poolReconcile(ctx)
	o := mustOrb(t, e, old.ID)
	assert.Equal(t, PoolRetiring, derefStr(o.PoolState))
	assert.Contains(t, []string{StateDeleting, StateDeleted}, o.State)
	v, err := View(ctx, e.st.Q, o)
	require.NoError(t, err)
	assert.Nil(t, v.Pool, "retiring 不对外暴露")
	pool := e.pool(prj)
	require.Len(t, pool, 1)
	assert.NotEqual(t, old.ID, pool[0].ID)
	e.waitState(old.ID, StateDeleted, 10*time.Second)
	fresh := e.waitState(pool[0].ID, StateReady, 10*time.Second)

	// 回收在 CAS 与 Delete 之间被重启打断：recover 补删
	n, err := e.st.Q.SetOrbPoolState(ctx, db.SetOrbPoolStateParams{PoolState: new(PoolRetiring), ID: fresh.ID, Seen: PoolIdle})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	e.svc.recover(ctx)
	e.waitState(fresh.ID, StateDeleted, 10*time.Second)
}

// pool.max_total 是全局上限；warm_orbs 调小时回收多余的。
func TestPoolMaxTotalAndSurplus(t *testing.T) {
	e := newPoolEnv(t, func(c *Config) { c.Pool.MaxTotal = 1 })
	ctx := context.Background()
	a := e.warmProject("a", 1, nil)
	b := e.warmProject("b", 1, nil)
	e.svc.poolReconcile(ctx)
	e.svc.poolReconcile(ctx)
	assert.Equal(t, 1, len(e.pool(a))+len(e.pool(b)))

	e2 := newPoolEnv(t, nil)
	c := e2.warmProject("c", 2, nil)
	e2.warmOne(c)
	e2.warmOne(c)
	_, err := e2.st.DB.Exec(`UPDATE projects SET warm_orbs = 1 WHERE id = ?`, c)
	require.NoError(t, err)
	e2.svc.poolReconcile(ctx)
	assert.Len(t, e2.pool(c), 1)
}

// 逐条覆盖「不预热」：warm_orbs = 0、没有仓库也没有 setup 脚本、72 小时内没有新线程、项目已归档、provider 离线、
// pool.max_total = 0。
func TestPoolNotWarmed(t *testing.T) {
	e := newPoolEnv(t, nil)
	ctx := context.Background()
	zero := e.warmProject("zero", 0, nil)
	bare := e.warmProject("bare", 1, func(p *db.CreateProjectParams) { p.RepoUrl, p.SetupScript = nil, nil })
	cold := e.project("cold", func(p *db.CreateProjectParams) { p.WarmOrbs = new(int64(1)) })
	archived := e.warmProject("archived", 1, nil)
	_, err := e.st.DB.Exec(`UPDATE projects SET archived_at = 1 WHERE id = ?`, archived)
	require.NoError(t, err)
	e.svc.poolReconcile(ctx)
	for _, prj := range []string{zero, bare, cold, archived} {
		assert.Empty(t, e.pool(prj), prj)
	}
	// provider 离线（租约在服务时钟里已过期）
	live := e.warmProject("live", 1, nil)
	e.skew.Store(int64(time.Hour))
	e.svc.poolReconcile(ctx)
	assert.Empty(t, e.pool(live), "provider 离线")
	e.skew.Store(0)
	e.svc.poolReconcile(ctx)
	assert.Len(t, e.pool(live), 1)

	off := newPoolEnv(t, func(c *Config) { c.Pool.MaxTotal = 0 })
	prj := off.warmProject("x", 1, nil)
	off.svc.poolReconcile(ctx)
	assert.Empty(t, off.pool(prj), "pool.max_total = 0")
	_, ok := off.svc.ClaimPool(ctx, CreateParams{ProjectID: prj})
	assert.False(t, ok)
}

// View：provider 声明了 docker 特性的类型 → nestedDocker（M11a 4.1）；其它类型不给出。
func TestViewNestedDocker(t *testing.T) {
	drv := newFakeDriver()
	drv.types = []apigen.OrbType{{Name: "small", Cpus: 2, MemoryMB: 4096}, {Name: "dind", Cpus: 4, MemoryMB: 8192, Features: []string{FeatureDocker}}}
	h := newHarnessRun(t, drv, nil, nil, false)
	ctx := context.Background()
	for typ, want := range map[string]bool{"small": false, "dind": true} {
		orb, err := h.svc.Create(ctx, CreateParams{Type: typ})
		require.NoError(t, err)
		v, err := View(ctx, h.st.Q, orb)
		require.NoError(t, err)
		assert.Equal(t, want, v.NestedDocker != nil && *v.NestedDocker, typ)
		assert.Nil(t, v.Pool)
		assert.Nil(t, v.SnapshotKind)
	}
}

// 池 orb setup 失败：被回收且 1 小时内不重建；退避过期（或项目被修改）后重新预热。
func TestPoolSetupFailureBackoff(t *testing.T) {
	e := newPoolEnv(t, nil)
	ctx := context.Background()
	prj := e.warmProject("app", 1, nil)
	e.mode.Store(modeFail)
	e.svc.poolReconcile(ctx)
	pool := e.pool(prj)
	require.Len(t, pool, 1)
	id := pool[0].ID
	e.waitState(id, StateDeleted, 10*time.Second)
	assert.Equal(t, PoolRetiring, derefStr(mustOrb(t, e, id).PoolState))
	assert.NotContains(t, e.stateEvents(id), "setting_up>ready", "setup 失败的池 orb 不进 ready")
	e.svc.mu.Lock()
	until := e.svc.poolBackoff[prj]
	e.svc.mu.Unlock()
	assert.WithinDuration(t, time.Now().Add(time.Hour), until, time.Minute)
	e.mode.Store(modeHit)
	e.svc.poolReconcile(ctx)
	assert.Empty(t, e.pool(prj), "退避期内（provider 在线、项目活跃）不重建")

	// 退避到期后重新预热（把截止时间挪到「现在」之前；拨快时钟会让 provider 租约也过期）
	e.svc.mu.Lock()
	e.svc.poolBackoff[prj] = time.Now().Add(-time.Second)
	e.svc.mu.Unlock()
	e.svc.poolReconcile(ctx)
	assert.Len(t, e.pool(prj), 1)

	// 项目被修改：PoolKick 清掉退避
	e.svc.mu.Lock()
	e.svc.poolBackoff[prj] = time.Now().Add(time.Hour)
	e.svc.mu.Unlock()
	e.svc.PoolKick(prj)
	e.svc.mu.Lock()
	_, still := e.svc.poolBackoff[prj]
	e.svc.mu.Unlock()
	assert.False(t, still)
}
