package orbs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// spawnJob 构造一个 spawn 任务终态（不入库：只交给 onJobFinished）。
func spawnJob(id, orbID string, createdAt int64) db.Job {
	return db.Job{ID: id, Op: jobs.OpSpawn, OrbID: &orbID, Params: `{}`, CreatedAt: createdAt}
}

// 停机期间才结束的 spawn 只有一个属于这次停机：frozen → stopping 在同一事务里入队、记进 detail.spawnJob 的那次。
// 之前入队的 spawn 与停机转换落在同一毫秒（created_at == state_changed_at）也不能被当作停机进度：成功不重发
// shutdown / pause{stop}，失败不把正常停机置为 failed。时钟固定，确定性地复现同一毫秒。
func TestStopIgnoresForeignSpawnSameMillisecond(t *testing.T) {
	drv := newFakeDriver()
	h := newHarnessRun(t, drv, nil, func(c *Config) { c.ConnectTimeout = 200 * time.Millisecond }, false)
	ctx := context.Background()
	t0 := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	pauses := func(orbID string) int {
		return h.count(`SELECT COUNT(*) FROM jobs WHERE orb_id = ? AND op = 'pause'`, orbID)
	}
	state := func(orbID string) string {
		h.svc.bg.Wait()
		o, err := h.svc.Get(ctx, orbID)
		require.NoError(t, err)
		return o.State
	}

	// ready → stopping 不入队 spawn：同一毫秒结束的旧 spawn 不属于它
	a, err := h.svc.Create(ctx, CreateParams{Provider: "local", Type: "small"})
	require.NoError(t, err)
	h.waitState(a.ID, StateReady, 10*time.Second)
	h.svc.now = func() time.Time { return t0 }
	ch, err := h.svc.transitionTx(ctx, a.ID, StateReady, StateStopping, nil, transitionOpts{})
	require.NoError(t, err)
	require.NotNil(t, ch)
	stale := spawnJob("job_stale_a", a.ID, t0.UnixMilli())
	require.Equal(t, stale.CreatedAt, ch.orb.StateChangedAt, "与旧任务同一毫秒")
	before := pauses(a.ID)
	h.svc.onJobFinished(ctx, stale, false)
	assert.Equal(t, StateStopping, state(a.ID), "旧 spawn 失败不把停机置为 failed")
	h.svc.onJobFinished(ctx, stale, true)
	assert.Equal(t, StateStopping, state(a.ID))
	assert.Equal(t, before, pauses(a.ID), "旧 spawn 成功不再入队 pause{stop}")

	// frozen → stopping：只有这次变化入队的 spawn 算数，同一毫秒的另一个 spawn 不算
	h.svc.now = time.Now
	b, err := h.svc.Create(ctx, CreateParams{Provider: "local", Type: "small"})
	require.NoError(t, err)
	h.waitState(b.ID, StateReady, 10*time.Second)
	require.NoError(t, h.svc.Pause(ctx, b.ID, PauseFreeze))
	h.waitState(b.ID, StateFrozen, 10*time.Second)
	t1 := t0.Add(time.Minute)
	h.svc.now = func() time.Time { return t1 }
	ch, err = h.svc.transitionTx(ctx, b.ID, StateFrozen, StateStopping, nil, transitionOpts{spawnJob: true, then: func(t *txn) error {
		// 代替 spawnIn 的入队（不让 provider 真的去跑）：任务终态由测试交给 onJobFinished
		t.job = "job_fresh_b"
		return nil
	}})
	require.NoError(t, err)
	require.NotNil(t, ch)
	staleB := spawnJob("job_stale_b", b.ID, t1.UnixMilli())
	before = pauses(b.ID)
	h.svc.onJobFinished(ctx, staleB, false)
	assert.Equal(t, StateStopping, state(b.ID), "不是这次停机入队的 spawn 不算")
	h.svc.onJobFinished(ctx, staleB, true)
	assert.Equal(t, StateStopping, state(b.ID))
	assert.Equal(t, before, pauses(b.ID))

	h.svc.onJobFinished(ctx, spawnJob("job_fresh_b", b.ID, t1.UnixMilli()), false)
	assert.Equal(t, StateFailed, state(b.ID), "这次停机自己的 spawn 失败才置 failed")
}
