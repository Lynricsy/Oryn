package schedule

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// ---- C8：读最早触发时间与替换定时器在同一串行区（F1） ----

// raceTick 在 Tick 的 armLocked 读到最早触发时间之后、替换定时器之前插入一次修改 mutate。
// 修复后该位置持有 loopMu：修改只能排在 Tick 之后（另起 goroutine 等它）；修复前 loopMu 是空闲的，
// 修改在此同步完成，复现「旧快照停掉新定时器」的交错。
func (f *fixture) raceTick(mutate func() error) {
	f.t.Helper()
	var hooked atomic.Bool
	var wg sync.WaitGroup
	var merr error
	f.s.afterMinNext = func() {
		if !hooked.CompareAndSwap(false, true) {
			return
		}
		if f.s.loopMu.TryLock() {
			f.s.loopMu.Unlock()
			f.t.Errorf("armLocked 在 loopMu 之外执行")
			merr = mutate()
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			merr = mutate()
		}()
	}
	f.s.Tick(f.ctx)
	wg.Wait()
	require.True(f.t, hooked.Load())
	require.NoError(f.t, merr)
	f.s.afterMinNext = nil
}

func TestTickArmsUnderLoopMu(t *testing.T) {
	t.Run("empty_then_once", func(t *testing.T) {
		f := newFixture(t)
		th := f.thread()
		at := f.clock.Now().Add(time.Minute).UnixMilli()
		f.raceTick(func() error {
			_, err := f.s.PutForThread(f.ctx, th, Input{Kind: KindOnce, At: &at, Prompt: "p"}, "user")
			return err
		})
		assert.Equal(t, []time.Duration{time.Minute}, f.timers.Active(), "恰有一个定时器")
		f.clock.Advance(time.Minute)
		require.Equal(t, 1, f.timers.Fire(time.Minute))
		f.s.Tick(f.ctx)
		assert.Len(t, f.th.Delivers(), 1)
		f.advanceTick(time.Hour)
		assert.Len(t, f.th.Delivers(), 1, "只触发一次")
	})
	t.Run("later_timer_then_earlier_edit", func(t *testing.T) {
		f := newFixture(t)
		th := f.thread()
		f.putThread(th, Input{Kind: KindEvery, Every: "1h", Prompt: "p"})
		require.Equal(t, []time.Duration{time.Hour}, f.timers.Active())
		f.raceTick(func() error {
			_, err := f.s.PutForThread(f.ctx, th, Input{Kind: KindEvery, Every: "10m", Prompt: "p"}, "user")
			return err
		})
		assert.Equal(t, []time.Duration{10 * time.Minute}, f.timers.Active(), "旧的较晚定时器不覆盖提前的修改")
		f.clock.Advance(10 * time.Minute)
		require.Equal(t, 1, f.timers.Fire(10*time.Minute))
		f.s.Tick(f.ctx)
		assert.Len(t, f.th.Delivers(), 1)
		assert.Equal(t, 0, f.timers.Fire(time.Hour), "1 小时的旧定时器已停")
	})
}

// ---- C4：Space 在写引用的事务里复核（F7 定时侧） ----

func TestSpaceDeletedBetweenPrecheckAndWrite(t *testing.T) {
	f := newFixture(t)
	mkSpace := func() string {
		id := ids.New("spc")
		f.exec(`INSERT INTO spaces (id, name, thread_id, created_at, updated_at) VALUES (?, ?, ?, 1, 1)`, id, id, f.thread())
		return id
	}
	// 与 coord.DeleteSpace 的删除事务同样的写入：清引用、删 Space 行
	deleteSpace := func(id string) func() {
		return func() {
			f.exec(`UPDATE schedules SET space_id = NULL WHERE space_id = ?`, id)
			f.exec(`DELETE FROM spaces WHERE id = ?`, id)
		}
	}
	count := func(q string, args ...any) int {
		var n int
		require.NoError(t, f.st.DB.QueryRowContext(f.ctx, q, args...).Scan(&n))
		return n
	}

	// 新建
	spc := mkSpace()
	f.s.afterCheckTarget = deleteSpace(spc)
	_, err := f.s.Put(f.ctx, "", Input{Mode: ModeNewThread, SpaceID: spc, Name: "a", Kind: KindEvery, Every: "1h", Prompt: "p"}, "user")
	requireCode(t, err, CodeInvalidSchedule)
	assert.Contains(t, err.Error(), "space not found")
	assert.Zero(t, count(`SELECT COUNT(*) FROM schedules`))

	// 改写已有定时
	f.s.afterCheckTarget = nil
	s := f.putNew(Input{Name: "b", Kind: KindEvery, Every: "1h", Prompt: "p"})
	spc2 := mkSpace()
	f.s.afterCheckTarget = deleteSpace(spc2)
	_, err = f.s.Put(f.ctx, s.ID, Input{SpaceID: spc2, Name: "b", Kind: KindEvery, Every: "2h", Prompt: "q"}, "user")
	requireCode(t, err, CodeInvalidSchedule)
	assert.Zero(t, count(`SELECT COUNT(*) FROM schedules WHERE space_id IS NOT NULL`), "没有悬空引用")
	sc := f.sched(s.ID)
	assert.Equal(t, "p", sc.Prompt, "整个事务回滚")
	assert.EqualValues(t, 1, sc.Rev)

	// 保留下来的定时仍能触发
	f.s.afterCheckTarget = nil
	f.advanceTick(time.Hour)
	assert.Len(t, f.th.Creates(), 1)
}

// ---- C6.2：agent 定义的定时规则（M2） ----

func TestAgentNewThreadRules(t *testing.T) {
	f := newFixture(t)
	agent := func(id string, in Input) (apigen.Schedule, error) {
		if id == "" {
			in.Mode = ModeNewThread
		}
		return f.s.Put(f.ctx, id, in, "agent")
	}
	for _, in := range []Input{
		{Name: "e5", Kind: KindEvery, Every: "5m", Prompt: "p"},
		{Name: "e14", Kind: KindEvery, Every: "14m59s", Prompt: "p"},
		{Name: "c5", Kind: KindCron, Cron: "*/5 * * * *", Prompt: "p"},
		{Name: "burst", Kind: KindCron, Cron: "0,10 9 * * *", Prompt: "p"},
		{Name: "allow", Kind: KindEvery, Every: "1h", Prompt: "p", Overlap: OverlapAllow},
	} {
		_, err := agent("", in)
		requireCode(t, err, CodeInvalidSchedule)
	}
	for _, in := range []Input{
		{Name: "e15", Kind: KindEvery, Every: "15m", Prompt: "p"},
		{Name: "c15", Kind: KindCron, Cron: "*/15 * * * *", Prompt: "p", Overlap: OverlapSkip},
		{Name: "daily", Kind: KindCron, Cron: "0 9 * * *", Prompt: "p"},
	} {
		_, err := agent("", in)
		require.NoError(t, err, in.Name)
	}
	// 主人不受这些规则约束；线程内定时也不受
	f.putNew(Input{Name: "owner", Kind: KindEvery, Every: "5m", Prompt: "p", Overlap: OverlapAllow})
	_, err := f.s.PutForThread(f.ctx, f.thread(), Input{Kind: KindEvery, Every: "1m", Prompt: "p"}, "agent")
	require.NoError(t, err)

	// keepLast 不可由 agent 调小；空 = 沿用
	k, err := agent("", Input{Name: "keep", Kind: KindEvery, Every: "1h", Prompt: "p", KeepLast: new(3)})
	require.NoError(t, err)
	_, err = agent(k.ID, Input{Name: "keep", Kind: KindEvery, Every: "1h", Prompt: "p", KeepLast: new(2)})
	requireCode(t, err, CodeInvalidSchedule)
	got, err := agent(k.ID, Input{Name: "keep", Kind: KindEvery, Every: "2h", Prompt: "p"})
	require.NoError(t, err)
	assert.Equal(t, 3, got.KeepLast, "空 = 沿用现值（不回落到默认 5）")
	got, err = agent(k.ID, Input{Name: "keep", Kind: KindEvery, Every: "2h", Prompt: "p", KeepLast: new(4)})
	require.NoError(t, err)
	assert.Equal(t, 4, got.KeepLast)
	_, err = agent(k.ID, Input{Name: "keep", Kind: KindEvery, Every: "5m", Prompt: "p"})
	requireCode(t, err, CodeInvalidSchedule)

	// 主人设了 allow 与较小的 keepLast：agent 改写时空值沿用，给出别的 overlap 拒绝
	_, err = f.s.Put(f.ctx, k.ID, Input{Name: "keep", Kind: KindEvery, Every: "1h", Prompt: "p", Overlap: OverlapAllow, KeepLast: new(1)}, "user")
	require.NoError(t, err)
	got, err = agent(k.ID, Input{Name: "keep", Kind: KindEvery, Every: "1h", Prompt: "q"})
	require.NoError(t, err)
	assert.Equal(t, apigen.ScheduleOverlapAllow, got.Overlap)
	assert.Equal(t, 1, got.KeepLast)
	_, err = agent(k.ID, Input{Name: "keep", Kind: KindEvery, Every: "1h", Prompt: "q", Overlap: OverlapSkip})
	requireCode(t, err, CodeInvalidSchedule)
}

func TestAgentEnabledLimit(t *testing.T) {
	f := newFixture(t)
	agentNew := func(name string, enabled bool) (apigen.Schedule, error) {
		return f.s.Put(f.ctx, "", Input{Mode: ModeNewThread, Name: name, Kind: KindEvery, Every: "1h", Prompt: "p", Enabled: &enabled}, "agent")
	}
	var first apigen.Schedule
	for i := range maxAgentEnabledSchedules {
		s, err := agentNew(fmt.Sprintf("a%d", i), true)
		require.NoError(t, err)
		if i == 0 {
			first = s
		}
	}
	_, err := agentNew("over", true)
	requireCode(t, err, CodeScheduleLimit)
	assert.Contains(t, err.Error(), "agents can have at most 20 enabled schedules")
	_, err = f.s.PutForThread(f.ctx, f.thread(), Input{Kind: KindEvery, Every: "1h", Prompt: "p"}, "agent")
	requireCode(t, err, CodeScheduleLimit)

	// 主人仍能新建
	owner := f.putNew(Input{Name: "owner", Kind: KindEvery, Every: "1h", Prompt: "p"})
	// agent 改写主人的启用中定时 → 计入 agent 上限
	_, err = f.s.Put(f.ctx, owner.ID, Input{Name: "owner", Kind: KindEvery, Every: "2h", Prompt: "p"}, "agent")
	requireCode(t, err, CodeScheduleLimit)
	// 停用的 agent 定时可以建，但恢复时按 created_by 计上限
	off, err := agentNew("off", false)
	require.NoError(t, err)
	_, err = f.s.SetEnabled(f.ctx, off.ID, true)
	requireCode(t, err, CodeScheduleLimit)
	_, err = f.s.SetEnabled(f.ctx, first.ID, false)
	require.NoError(t, err)
	_, err = f.s.SetEnabled(f.ctx, off.ID, true)
	require.NoError(t, err)
}

// ---- C7.1：rev / ifRev / schedule_conflict（W1） ----

func TestRevAndIfRev(t *testing.T) {
	f := newFixture(t)
	rev := func(id string) int64 { return f.sched(id).Rev }
	th := f.thread()
	s := f.putThread(th, Input{Kind: KindEvery, Every: "1h", Prompt: "p"})
	assert.EqualValues(t, 1, s.Rev)

	_, err := f.s.PutForThread(f.ctx, th, Input{Kind: KindEvery, Every: "1h", Prompt: "q", IfRev: new(int64(0))}, "user")
	requireCode(t, err, CodeScheduleConflict)
	assert.Contains(t, err.Error(), "the thread already has a schedule")

	s2, err := f.s.Put(f.ctx, s.ID, Input{Kind: KindEvery, Every: "2h", Prompt: "q", IfRev: new(int64(1))}, "user")
	require.NoError(t, err)
	assert.EqualValues(t, 2, s2.Rev)
	_, err = f.s.Put(f.ctx, s.ID, Input{Kind: KindEvery, Every: "3h", Prompt: "stale", IfRev: new(int64(1))}, "user")
	requireCode(t, err, CodeScheduleConflict)
	assert.Contains(t, err.Error(), "the schedule changed since it was loaded (rev 2)")
	assert.Equal(t, "q", f.sched(s.ID).Prompt, "库未变")
	assert.EqualValues(t, 2, rev(s.ID))

	_, err = f.s.SetEnabled(f.ctx, s.ID, false)
	require.NoError(t, err)
	assert.EqualValues(t, 3, rev(s.ID), "暂停 +1")
	_, err = f.s.SetEnabled(f.ctx, s.ID, true)
	require.NoError(t, err)
	assert.EqualValues(t, 4, rev(s.ID), "恢复 +1")
	f.advanceTick(2 * time.Hour)
	require.Len(t, f.th.Delivers(), 1)
	assert.EqualValues(t, 4, rev(s.ID), "按时触发不变")
	// 自动暂停 +1
	f.exec(`UPDATE schedules SET failures = 2 WHERE id = ?`, s.ID)
	f.ack(0, errors.New("x"))
	assert.False(t, f.sched(s.ID).Enabled)
	assert.EqualValues(t, 5, rev(s.ID))
	_, err = f.s.PutForThread(f.ctx, th, Input{Kind: KindEvery, Every: "1h", Prompt: "r", IfRev: new(int64(5))}, "user")
	require.NoError(t, err)
	assert.EqualValues(t, 6, rev(s.ID))

	// 一次性自动结束 +1
	th2 := f.thread()
	at := f.clock.Now().Add(time.Minute).UnixMilli()
	o := f.putThread(th2, Input{Kind: KindOnce, At: &at, Prompt: "p"})
	f.advanceTick(time.Minute)
	assert.EqualValues(t, o.Rev+1, rev(o.ID))

	// 定时已不存在
	require.NoError(t, f.s.DeleteByThread(f.ctx, th))
	_, err = f.s.PutForThread(f.ctx, th, Input{Kind: KindEvery, Every: "1h", Prompt: "p", IfRev: new(int64(6))}, "user")
	requireCode(t, err, CodeScheduleConflict)
	assert.Contains(t, err.Error(), "the schedule no longer exists")
	_, err = f.s.Put(f.ctx, s.ID, Input{Kind: KindEvery, Every: "1h", Prompt: "p", IfRev: new(int64(6))}, "user")
	requireCode(t, err, CodeScheduleConflict)
	_, err = f.s.Put(f.ctx, s.ID, Input{Kind: KindEvery, Every: "1h", Prompt: "p"}, "user")
	requireCode(t, err, CodeScheduleNotFound)
	n, err := f.s.PutForThread(f.ctx, th, Input{Kind: KindEvery, Every: "1h", Prompt: "p", IfRev: new(int64(0))}, "user")
	require.NoError(t, err, "0 = 只在没有定时时新建")
	assert.EqualValues(t, 1, n.Rev)
	// 新建入口忽略 ifRev
	_, err = f.s.Put(f.ctx, "", Input{Mode: ModeThread, ThreadID: f.thread(), Kind: KindEvery, Every: "1h", Prompt: "p", IfRev: new(int64(9))}, "user")
	require.NoError(t, err)
}

// ---- C7.2：每个发出点各推送一次 schedules.changed（W6） ----

func TestChangedNotifications(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Name: "n", Kind: KindEvery, Every: "1m", Prompt: "p"})
	id := s.ID
	one := []string{id}
	assert.Equal(t, one, f.takeChanges(), "新建")
	_, err := f.s.Put(f.ctx, id, Input{Name: "n", Kind: KindEvery, Every: "1m", Prompt: "q"}, "user")
	require.NoError(t, err)
	assert.Equal(t, one, f.takeChanges(), "替换")
	_, err = f.s.SetEnabled(f.ctx, id, false)
	require.NoError(t, err)
	assert.Equal(t, one, f.takeChanges(), "暂停")
	_, err = f.s.SetEnabled(f.ctx, id, true)
	require.NoError(t, err)
	assert.Equal(t, one, f.takeChanges(), "恢复")

	f.advanceTick(time.Minute)
	assert.Equal(t, one, f.takeChanges(), "触发")
	f.ack(0, nil)
	assert.Equal(t, one, f.takeChanges(), "已下发")
	f.state(th, apigen.ThreadStateAwaitingInput)
	assert.Equal(t, one, f.takeChanges(), "needs_input")
	f.state(th, apigen.ThreadStateWorking)
	assert.Equal(t, one, f.takeChanges(), "回到 running")
	f.state(th, apigen.ThreadStateIdle)
	assert.Empty(t, f.takeChanges(), "idle 只是候选")
	f.done(0)
	assert.Equal(t, one, f.takeChanges(), "运行结束")

	f.exec(`UPDATE schedules SET failures = 2 WHERE id = ?`, id)
	f.advanceTick(time.Minute)
	f.takeChanges()
	f.ack(1, errors.New("x"))
	assert.Equal(t, []string{id, id}, f.takeChanges(), "运行结束 + 自动暂停")

	_, err = f.s.RunNow(f.ctx, id)
	require.NoError(t, err)
	assert.Equal(t, one, f.takeChanges(), "手动触发")
	f.turn(th, f.th.Delivers()[2].tag, threads.TurnAborted, "")
	assert.Equal(t, one, f.takeChanges(), "TurnEnded 结束运行")

	require.NoError(t, f.s.Delete(f.ctx, id))
	assert.Equal(t, one, f.takeChanges(), "删除")
	s2 := f.putThread(th, Input{Kind: KindEvery, Every: "1h", Prompt: "p"})
	f.takeChanges()
	require.NoError(t, f.s.DeleteByThread(f.ctx, th))
	assert.Equal(t, []string{s2.ID}, f.takeChanges(), "按线程删除")

	n := f.putNew(Input{Name: "nt", Kind: KindEvery, Every: "1h", Prompt: "p"})
	f.takeChanges()
	f.advanceTick(time.Hour)
	assert.Equal(t, []string{n.ID, n.ID}, f.takeChanges(), "new_thread：触发 + 建出线程")
}

// ---- C9.3：agent 定义的定时运行头行（M3） ----

func TestAgentDefinedHeader(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	_, err := f.s.PutForThread(f.ctx, th, Input{Name: "a", Kind: KindCron, Cron: "* * * * *", Prompt: "p"}, "agent")
	require.NoError(t, err)
	f.advanceTick(2*time.Minute + 30*time.Second)
	require.Len(t, f.th.Delivers(), 1)
	assert.Equal(t, "[Oryn scheduled run \"a\" #1 · scheduled for 2026-11-14T09:01:00Z (UTC) · late, 1 missed runs coalesced · defined by an agent]\np",
		f.th.Delivers()[0].text)
	f.ack(0, nil)
	f.done(0)

	// 主人在界面里改回定义后不再带后缀
	_, err = f.s.PutForThread(f.ctx, th, Input{Name: "a", Kind: KindCron, Cron: "* * * * *", Prompt: "p"}, "user")
	require.NoError(t, err)
	f.advanceTick(time.Minute)
	require.Len(t, f.th.Delivers(), 2)
	assert.Equal(t, "[Oryn scheduled run \"a\" #2 · scheduled for 2026-11-14T09:03:00Z (UTC)]\np", f.th.Delivers()[1].text)
}
