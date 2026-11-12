package orbs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// ---- 不带 provider 运行时的夹具：任务停在队列里，由测试手工认领 / 完成 ----

type bare struct {
	t      *testing.T
	st     *store.Store
	q      *jobs.Queue
	svc    *Service
	provID string

	evMu   sync.Mutex
	events []Event
}

func newBare(t *testing.T, mod func(*Config)) *bare {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	b := &bare{t: t, st: st, q: jobs.New(st), provID: ids.New("prov")}
	cfg := Config{
		Idle:     config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(time.Hour)},
		TokenKey: []byte("0123456789abcdef0123456789abcdef"), ConnectTimeout: 10 * time.Second, ShutdownGrace: 1,
	}
	if mod != nil {
		mod(&cfg)
	}
	b.svc = New(cfg, st, b.q, nil, fakeSecrets{})
	b.svc.Subscribe(func(ev Event) {
		b.evMu.Lock()
		b.events = append(b.events, ev)
		b.evMu.Unlock()
	})
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: b.provID, Name: "local", TokenHash: ids.Hash(ids.Token("opv")), CreatedAt: 1}))
	t.Cleanup(func() {
		b.svc.Close()
		_ = st.Close()
	})
	return b
}

// orb 直接以给定状态写库（模拟重启前遗留的中间状态）。
func (b *bare) orb(state string, changedAt int64) db.Orb {
	b.t.Helper()
	ctx := context.Background()
	id := ids.Orb()
	require.NoError(b.t, b.st.Q.CreateOrb(ctx, db.CreateOrbParams{
		ID: id, ProviderID: b.provID, Type: "small", State: state, StateChangedAt: changedAt,
		TokenHash: ids.Hash(orbToken(b.svc.cfg.TokenKey, id, 0)), CreatedAt: 1,
	}))
	require.NoError(b.t, b.st.Q.InsertOrbEvent(ctx, db.InsertOrbEventParams{OrbID: id, Ts: changedAt, FromState: "ready", ToState: state, Detail: "{}"}))
	orb, err := b.st.Q.GetOrb(ctx, id)
	require.NoError(b.t, err)
	return orb
}

// finishedJob 入队、认领并完成一个任务，且不触发完成回调（模拟「任务已终态、回调前崩溃」）。
func (b *bare) finishedJob(orbID, op string, params any, ok bool) db.Job {
	b.t.Helper()
	ctx := context.Background()
	b.q.SetHooks(jobs.Hooks{})
	defer b.q.SetHooks(jobs.Hooks{OnClaimed: b.svc.onJobClaimed, OnFinished: b.svc.onJobFinished})
	job, err := b.q.Enqueue(ctx, b.provID, orbID, op, params)
	require.NoError(b.t, err)
	c, claimed, err := b.q.Claim(ctx, b.provID, 0)
	require.NoError(b.t, err)
	require.True(b.t, claimed)
	require.Equal(b.t, job.ID, c.ID)
	require.NoError(b.t, b.q.Complete(ctx, b.provID, job.ID, c.ClaimGen, jobs.Result{OK: ok, Error: "x"}))
	return job
}

func (b *bare) state(orbID string) string {
	b.t.Helper()
	o, err := b.st.Q.GetOrb(context.Background(), orbID)
	require.NoError(b.t, err)
	return o.State
}

// pending 返回 orb 未完成的任务（op:mode）。
func (b *bare) pending(orbID string) []string {
	b.t.Helper()
	js, err := b.st.Q.ListUnfinishedJobsByOrb(context.Background(), &orbID)
	require.NoError(b.t, err)
	out := []string{}
	for _, j := range js {
		out = append(out, j.Op+":"+jobPauseMode(j))
	}
	return out
}

func (b *bare) waitBG() {
	done := make(chan struct{})
	go func() { b.svc.bg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		b.t.Fatal("后台流程没有结束")
	}
}

// CP4：重启时每个中间状态都与任务表对账，且对账幂等。
func TestRecoverReconcilesIntermediateStates(t *testing.T) {
	b := newBare(t, func(c *Config) { c.ConnectTimeout = 100 * time.Millisecond })
	ctx := context.Background()
	past := int64(1000)

	spawned := b.orb(StateSpawning, past)
	b.finishedJob(spawned.ID, jobs.OpSpawn, apigen.JobSpawnParams{Type: "small"}, true)
	frozeOK := b.orb(StateFreezing, past)
	b.finishedJob(frozeOK.ID, jobs.OpPause, apigen.JobPauseParams{Mode: apigen.Freeze}, true)
	stoppedOK := b.orb(StateStopping, past)
	b.finishedJob(stoppedOK.ID, jobs.OpPause, apigen.JobPauseParams{Mode: apigen.Stop}, true)
	deletedOK := b.orb(StateDeleting, past)
	b.finishedJob(deletedOK.ID, jobs.OpDelete, nil, true)

	freezingNoJob := b.orb(StateFreezing, past)
	stoppingNoJob := b.orb(StateStopping, past)
	deletingNoJob := b.orb(StateDeleting, past)
	// waking 之前的旧 spawn（早于进入 waking）不算数：须补入队一个新的 spawn 并轮换令牌。
	wakingStale := b.orb(StateReady, past)
	stale := b.finishedJob(wakingStale.ID, jobs.OpSpawn, apigen.JobSpawnParams{Type: "small"}, true)
	time.Sleep(5 * time.Millisecond)
	_, err := b.st.Q.TransitionOrbState(ctx, db.TransitionOrbStateParams{ToState: StateWaking, StateChangedAt: time.Now().UnixMilli(), ID: wakingStale.ID, FromState: StateReady})
	require.NoError(t, err)
	require.Less(t, stale.CreatedAt, time.Now().UnixMilli())
	// 任务仍在队列：什么也不做。
	requestedQueued := b.orb(StateRequested, past)
	_, err = b.q.Enqueue(ctx, b.provID, requestedQueued.ID, jobs.OpSpawn, apigen.JobSpawnParams{Type: "small"})
	require.NoError(t, err)

	for range 2 { // 第二遍验证幂等
		b.svc.recover(ctx)
		b.waitBG()
	}

	evs, err := b.st.Q.ListOrbEvents(ctx, spawned.ID)
	require.NoError(t, err)
	assert.Equal(t, StateConnecting, evs[1].ToState, "spawn 已成功 → connecting（之后无 hello 会超时 failed）")
	assert.Len(t, evs, 2+boolInt(b.state(spawned.ID) == StateFailed), "只推进一次")
	assert.Equal(t, StateFrozen, b.state(frozeOK.ID))
	assert.Equal(t, StateStopped, b.state(stoppedOK.ID))
	assert.Equal(t, StateDeleted, b.state(deletedOK.ID))

	assert.Equal(t, StateFreezing, b.state(freezingNoJob.ID))
	assert.Equal(t, []string{"pause:freeze"}, b.pending(freezingNoJob.ID))
	assert.Equal(t, StateStopping, b.state(stoppingNoJob.ID))
	assert.Equal(t, []string{"pause:stop"}, b.pending(stoppingNoJob.ID))
	assert.Equal(t, StateDeleting, b.state(deletingNoJob.ID))
	assert.Equal(t, []string{"delete:"}, b.pending(deletingNoJob.ID))
	assert.Equal(t, StateWaking, b.state(wakingStale.ID))
	assert.Equal(t, []string{"spawn:"}, b.pending(wakingStale.ID))
	w, err := b.st.Q.GetOrb(ctx, wakingStale.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, w.TokenGen, "补入队的 spawn 轮换了令牌（且只轮换一次）")
	assert.Equal(t, ids.Hash(orbToken(b.svc.cfg.TokenKey, w.ID, 1)), w.TokenHash)
	assert.Equal(t, StateRequested, b.state(requestedQueued.ID))
	assert.Equal(t, []string{"spawn:"}, b.pending(requestedQueued.ID))
}

// CP12：Wake 读到 freezing 之后、登记延迟唤醒之前，暂停任务完成并消费了（尚不存在的）标记——唤醒不能丢。
func TestWakeDuringFreezeCompletionIsNotLost(t *testing.T) {
	b := newBare(t, nil)
	ctx := context.Background()
	orb := b.orb(StateFreezing, time.Now().UnixMilli())
	b.svc.hookWakeDeferred = func() {
		pause := db.Job{ID: "job_x", OrbID: &orb.ID, Op: jobs.OpPause, Params: `{"mode":"freeze"}`}
		b.svc.onJobFinished(ctx, pause, true)
		require.Equal(t, StateFrozen, b.state(orb.ID))
	}
	require.NoError(t, b.svc.Wake(ctx, orb.ID))
	assert.Equal(t, StateWaking, b.state(orb.ID))
	assert.Equal(t, []string{"spawn:"}, b.pending(orb.ID))
	b.svc.mu.Lock()
	assert.Empty(t, b.svc.wakeAfter, "标记已收回")
	b.svc.mu.Unlock()
}

// CP8：空闲冻结经闸门提交；提交事务内复核空闲（闸门内的交互让提交失败），事件在闸门返回之后才发出。
func TestIdleFreezeGoesThroughQuiesceGate(t *testing.T) {
	b := newBare(t, func(c *Config) {
		c.Idle.AgentIdle = config.Duration(time.Minute)
		c.Idle.UserIdle = config.Duration(time.Minute)
	})
	ctx := context.Background()
	old := time.Now().Add(-2 * time.Minute).UnixMilli()
	touched := b.orb(StateReady, old)
	idle := b.orb(StateReady, old)

	emitted := func(orbID string) bool {
		b.evMu.Lock()
		defer b.evMu.Unlock()
		for _, ev := range b.events {
			if ev.OrbID == orbID && ev.Kind == EventState && ev.To == StateFreezing {
				return true
			}
		}
		return false
	}
	var commits sync.Map
	b.svc.SetQuiescence(func(orbID string, commit func() bool) bool {
		if orbID == touched.ID {
			// 命令准入（持线程锁）记下了一次交互：提交必须看到它。
			b.svc.TouchUser(ctx, orbID)
		}
		ok := commit()
		commits.Store(orbID, ok)
		assert.False(t, emitted(orbID), "提交期间（线程锁持有中）不得发出事件")
		return ok
	})
	b.svc.EvaluateIdle(ctx)

	v, _ := commits.Load(touched.ID)
	assert.Equal(t, false, v)
	assert.Equal(t, StateReady, b.state(touched.ID))
	assert.Empty(t, b.pending(touched.ID))

	v, _ = commits.Load(idle.ID)
	assert.Equal(t, true, v)
	assert.Equal(t, StateFreezing, b.state(idle.ID))
	assert.Equal(t, []string{"pause:freeze"}, b.pending(idle.ID), "状态与 pause 任务同一事务提交")
	assert.True(t, emitted(idle.ID))

	// 闸门不放行时不提交。
	b.svc.SetQuiescence(func(string, func() bool) bool { return false })
	other := b.orb(StateReady, old)
	b.svc.EvaluateIdle(ctx)
	assert.Equal(t, StateReady, b.state(other.ID))
}

// CP9：状态事件的 Seq = orb_events.id；乱序到达的旧状态不改动 connect 计时器。
func TestEventSeqAndStaleTimerUpdate(t *testing.T) {
	b := newBare(t, func(c *Config) { c.ConnectTimeout = 100 * time.Millisecond })
	ctx := context.Background()
	orb := b.orb(StateSpawning, 1)
	_, moved, err := b.svc.transition(ctx, orb.ID, StateSpawning, StateConnecting, nil)
	require.NoError(t, err)
	require.True(t, moved)
	evs, err := b.st.Q.ListOrbEvents(ctx, orb.ID)
	require.NoError(t, err)
	last := evs[len(evs)-1]
	b.evMu.Lock()
	ev := b.events[len(b.events)-1]
	b.evMu.Unlock()
	assert.Equal(t, last.ID, ev.Seq)
	assert.Equal(t, StateConnecting, ev.To)

	// 一个更早提交（Seq 更小）的 ready 事件迟到：不得取消 connecting 的 hello 超时。
	b.svc.armConnectTimer(orb.ID, StateReady, last.ID-1)
	require.Eventually(t, func() bool { return b.state(orb.ID) == StateFailed }, 5*time.Second, 10*time.Millisecond)

	// 非状态事件携带当前已提交的最大 Seq。
	cur, err := b.svc.Get(ctx, orb.ID)
	require.NoError(t, err)
	b.svc.emitInfo(ctx, EventLinkDown, cur)
	maxID, err := b.st.Q.MaxOrbEventID(ctx, orb.ID)
	require.NoError(t, err)
	b.evMu.Lock()
	ev = b.events[len(b.events)-1]
	b.evMu.Unlock()
	assert.Equal(t, EventLinkDown, ev.Kind)
	assert.Equal(t, maxID, ev.Seq)
	assert.Greater(t, maxID, last.ID)
}

// CP1：link 发布到 links 时 ctl 已就绪，且 hello 在发布之后才被处理。
func TestLinkPublishedWithReadyCtl(t *testing.T) {
	drv := newFakeDriver()
	h := newHarness(t, drv, nil, nil)
	var checked atomic.Int32
	h.svc.hookLinkPublished = func(orbID string) {
		ctl, ok := h.svc.Ctl(orbID)
		assert.True(t, ok)
		assert.NotNil(t, ctl, "发布出去的 link 必须带 ctl")
		assert.False(t, h.svc.Connected(orbID), "发布之前不处理 hello")
		checked.Add(1)
	}
	orb, err := h.svc.Create(context.Background(), CreateParams{})
	require.NoError(t, err)
	h.waitState(orb.ID, StateReady, 10*time.Second)
	assert.GreaterOrEqual(t, checked.Load(), int32(1))
}

// H2：每次 spawn 轮换令牌；frozen / stopped 的 orb 拒绝 link（hello 不能唤醒它）；旧令牌在唤醒后失效。
func TestOrbTokenRotationAndLinkStates(t *testing.T) {
	drv := newFakeDriver()
	h := newHarness(t, drv, nil, nil)
	ctx := context.Background()
	orb, err := h.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	h.waitState(orb.ID, StateReady, 10*time.Second)
	o := drv.orbd(orb.ID)
	o.mu.Lock()
	t1 := o.token
	o.mu.Unlock()
	got, err := h.st.Q.GetOrb(ctx, orb.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.TokenGen)
	assert.Equal(t, orbToken(h.svc.cfg.TokenKey, orb.ID, 1), t1)

	dial := func(token string) error {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		sess, err := link.Dial(c, link.URL(h.srv.URL), token)
		if err == nil {
			_ = sess.Close()
		}
		return err
	}
	require.NoError(t, h.svc.Pause(ctx, orb.ID, PauseFreeze))
	h.waitState(orb.ID, StateFrozen, 10*time.Second)
	assert.Error(t, dial(t1), "frozen 的 orb 不接受 link，即使令牌仍是当前代")
	assert.Equal(t, StateFrozen, h.waitState(orb.ID, StateFrozen, time.Second).State)

	require.NoError(t, h.svc.Wake(ctx, orb.ID))
	h.waitState(orb.ID, StateReady, 10*time.Second)
	o.mu.Lock()
	t2 := o.token
	o.mu.Unlock()
	assert.NotEqual(t, t1, t2)
	assert.Error(t, dial(t1), "旧令牌在轮换后失效")
	got, err = h.st.Q.GetOrb(ctx, orb.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, got.TokenGen)

	require.NoError(t, h.svc.Pause(ctx, orb.ID, PauseStop))
	h.waitState(orb.ID, StateStopped, 10*time.Second)
	assert.Error(t, dial(t2), "stopped 的 orb 不接受 link")
	assert.NotContains(t, h.eventSeq(orb.ID)[len(h.eventSeq(orb.ID))-1], "waking")
}

// M4：orb 写入的项目脚本有大小上限，且每次写入记一条 orb_events（脚本哈希）。
func TestProjectScriptsSetLimitAndAudit(t *testing.T) {
	drv := newFakeDriver()
	h := newHarness(t, drv, nil, nil)
	ctx := context.Background()
	prj := h.createProject("p", nil, nil)
	orb, c := h.readyOrb(drv, prj.ID)

	big := strings.Repeat("x", MaxProjectScriptBytes+1)
	err := c.Call(ctx, "project.scripts.set", apigen.LinkCtlProjectScriptsSetParams{Setup: &big}, nil)
	var ce *link.CtlError
	require.True(t, errors.As(err, &ce), "%v", err)
	assert.Equal(t, CodeTooLarge, ce.Code)
	p, err := h.st.Q.GetProject(ctx, prj.ID)
	require.NoError(t, err)
	assert.Nil(t, p.SetupScript)

	script := "echo ok"
	require.NoError(t, c.Call(ctx, "project.scripts.set", apigen.LinkCtlProjectScriptsSetParams{Setup: &script}, nil))
	evs, err := h.st.Q.ListOrbEvents(ctx, orb.ID)
	require.NoError(t, err)
	last := evs[len(evs)-1]
	assert.Equal(t, StateReady, last.FromState)
	assert.Equal(t, StateReady, last.ToState)
	var d map[string]string
	require.NoError(t, json.Unmarshal([]byte(last.Detail), &d))
	assert.Equal(t, "project_scripts_set", d["event"])
	assert.Equal(t, prj.ID, d["projectId"])
	sum := sha256.Sum256([]byte(script))
	assert.Equal(t, hex.EncodeToString(sum[:]), d["setupSha256"])
	assert.Equal(t, "", d["resumeSha256"])
}

// M5：每个 orb 只保留最新的 MaxSetupRunsPerOrb 条 setup 运行记录。
func TestSetupRunsPrunedPerOrb(t *testing.T) {
	drv := newFakeDriver()
	h := newHarness(t, drv, nil, nil)
	ctx := context.Background()
	orb, c := h.readyOrb(drv, "")
	for i := range MaxSetupRunsPerOrb + 5 {
		require.NoError(t, c.Notify("setup.result", apigen.LinkCtlSetupResultParams{
			Phase: apigen.SetupPhaseResume, Trigger: apigen.SetupTriggerManual, Ok: true, DurationMs: int64(i), StartedAt: int64(i),
		}))
	}
	require.Eventually(t, func() bool {
		var n int
		err := h.st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM orb_setup_runs WHERE orb_id = ?`, orb.ID).Scan(&n)
		var maxDur int64
		_ = h.st.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(duration_ms), -1) FROM orb_setup_runs WHERE orb_id = ?`, orb.ID).Scan(&maxDur)
		return err == nil && n == MaxSetupRunsPerOrb && maxDur == int64(MaxSetupRunsPerOrb+4)
	}, 5*time.Second, 20*time.Millisecond)
	var minDur int64
	require.NoError(t, h.st.DB.QueryRowContext(ctx, `SELECT MIN(duration_ms) FROM orb_setup_runs WHERE orb_id = ?`, orb.ID).Scan(&minDur))
	assert.EqualValues(t, 5, minDur, "删除的是最早的记录")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
