package schedule

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 带标签投递的终态（契约 C1.5）与恢复运行的终态（补充契约 C1.6 / C1.7）。

// threads 对 agent_start → terminal agent_end → prompt_result(error) 发出的 Observer 序列是
// State(working→idle)、TurnEnded{error}、State(idle→error, PromptError)：连续 3 次都记 failed 并自动暂停。
func TestTaggedPromptErrorFailsAndPauses(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Name: "nightly", Kind: KindEvery, Every: "1m", Prompt: "p"})
	for i := range 3 {
		f.advanceTick(time.Minute)
		f.ack(i, nil)
		f.state(th, apigen.ThreadStateWorking)
		f.state(th, apigen.ThreadStateIdle)
		require.Equal(t, runRunning, f.runs(s.ID)[0].Status, "第 %d 次：idle 不结算", i+1)
		d := f.th.Delivers()[i]
		f.turn(th, d.tag, threads.TurnError, "provider exploded")
		f.stateEv(th, apigen.ThreadStateError, true)
		r := f.runs(s.ID)[0]
		assert.Equal(t, runFailed, r.Status)
		assert.Equal(t, "provider exploded", deref(r.Error))
		assert.Equal(t, d.tag, r.ID)
	}
	sc := f.sched(s.ID)
	assert.EqualValues(t, 3, sc.Failures)
	assert.False(t, sc.Enabled)
	assert.Equal(t, pausedFailures, deref(sc.PausedReason))
	assert.Equal(t, []post{{th, "", `Schedule "nightly" was paused after 3 consecutive failures`}}, f.n.Posts(),
		"已下发的运行失败不另发投递失败通知")
}

func TestTaggedOutcomes(t *testing.T) {
	long := strings.Repeat("x", 600)
	cases := []struct {
		name                 string
		ev                   threads.TurnEvent
		status, err          string
		failures0, failures1 int64
	}{
		{"completed", threads.TurnEvent{Status: threads.TurnCompleted}, runCompleted, "", 2, 0},
		{"local", threads.TurnEvent{Status: threads.TurnCompleted, Local: true}, runCompleted, "", 2, 0},
		{"aborted", threads.TurnEvent{Status: threads.TurnAborted}, runFailed, "aborted", 1, 1},
		{"agent_exited", threads.TurnEvent{Status: threads.TurnError, Error: "agent_exited"}, runFailed, "agent_exited", 0, 1},
		{"error_empty", threads.TurnEvent{Status: threads.TurnError}, runFailed, "thread_error", 0, 1},
		{"error_long", threads.TurnEvent{Status: threads.TurnError, Error: long}, runFailed, long[:maxRunError], 0, 1},
		{"unknown", threads.TurnEvent{Status: threads.TurnUnknown}, runFailed, "outcome_unknown", 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			th := f.thread()
			s := f.putThread(th, Input{Kind: KindEvery, Every: "1h", Prompt: "p"})
			f.exec(`UPDATE schedules SET failures = ? WHERE id = ?`, c.failures0, s.ID)
			f.advanceTick(time.Hour)
			f.ack(0, nil)
			f.state(th, apigen.ThreadStateWorking)
			ev := c.ev
			ev.ThreadID, ev.Tag = th, f.th.Delivers()[0].tag
			f.s.TurnEnded(ev)
			f.process()
			r := f.runs(s.ID)[0]
			assert.Equal(t, c.status, r.Status)
			if c.err == "" {
				assert.Nil(t, r.Error)
			} else {
				assert.Equal(t, c.err, deref(r.Error))
			}
			assert.NotNil(t, r.FinishedAt)
			assert.Equal(t, c.failures1, f.sched(s.ID).Failures)
			assert.Empty(t, f.s.open, "运行结束后不再登记")
			assert.Empty(t, f.s.tagged)
		})
	}
}

// 带标签的运行只认 TurnEnded：非 terminal agent_end（仍 working）与裸 idle 都不结算；别的标签不入队。
func TestTaggedIgnoresIdleAndForeignTags(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Kind: KindEvery, Every: "1h", Prompt: "p"})
	f.advanceTick(time.Hour)
	f.ack(0, nil)
	f.state(th, apigen.ThreadStateWorking)
	f.state(th, apigen.ThreadStateIdle)
	f.state(th, apigen.ThreadStateWorking)
	f.state(th, apigen.ThreadStateIdle)
	assert.Equal(t, runRunning, f.runs(s.ID)[0].Status)
	f.s.TurnEnded(threads.TurnEvent{ThreadID: th, Tag: "rep_x", Status: threads.TurnCompleted})
	f.s.mu.Lock()
	assert.Empty(t, f.s.queue, "协调回报的标签不入队")
	f.s.mu.Unlock()
	assert.Empty(t, f.th.Recovers(), "带标签的运行不走转录推断")
	f.done(0)
	assert.Equal(t, runCompleted, f.runs(s.ID)[0].Status)
}

// 线程因非 prompt 原因进入 error（orb 失败等）：按 thread_error 结算并计一次；之后的 TurnEnded{error} 被忽略。
func TestTaggedNonPromptErrorCountsOnce(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Kind: KindEvery, Every: "1h", Prompt: "p"})
	f.advanceTick(time.Hour)
	f.ack(0, nil)
	f.stateEv(th, apigen.ThreadStateError, false)
	f.turn(th, f.th.Delivers()[0].tag, threads.TurnError, "agent_exited")
	r := f.runs(s.ID)[0]
	assert.Equal(t, runFailed, r.Status)
	assert.Equal(t, "thread_error", deref(r.Error))
	assert.EqualValues(t, 1, f.sched(s.ID).Failures)
}

// 应答出错与 TurnEnded{error} 两种到达顺序都只计一次失败、只发一次通知。
func TestAckErrorAndTurnEndedCountOnce(t *testing.T) {
	t.Run("ack_first", func(t *testing.T) {
		f := newFixture(t)
		th := f.thread()
		s := f.putThread(th, Input{Name: "n", Kind: KindEvery, Every: "1h", Prompt: "p"})
		f.advanceTick(time.Hour)
		f.ack(0, &threads.Error{Code: "invalid", Message: "bad prompt"})
		f.turn(th, f.th.Delivers()[0].tag, threads.TurnError, "bad prompt")
		r := f.runs(s.ID)[0]
		assert.Equal(t, runFailed, r.Status)
		assert.Equal(t, "invalid: bad prompt", deref(r.Error))
		assert.EqualValues(t, 1, f.sched(s.ID).Failures)
		assert.Len(t, f.n.Posts(), 1)
	})
	t.Run("turn_first", func(t *testing.T) {
		f := newFixture(t)
		th := f.thread()
		s := f.putThread(th, Input{Name: "n", Kind: KindEvery, Every: "1h", Prompt: "p"})
		f.advanceTick(time.Hour)
		require.Contains(t, f.timers.Active(), deliverTimeout)
		// 运行仍 pending（应答还没处理）时先到 TurnEnded：按投递失败结算并通知
		f.turn(th, f.th.Delivers()[0].tag, threads.TurnError, "bad prompt")
		r := f.runs(s.ID)[0]
		assert.Equal(t, runFailed, r.Status)
		assert.Equal(t, "bad prompt", deref(r.Error))
		assert.NotContains(t, f.timers.Active(), deliverTimeout, "撤掉投递超时")
		f.ack(0, &threads.Error{Code: "invalid", Message: "bad prompt"})
		assert.EqualValues(t, 1, f.sched(s.ID).Failures)
		assert.Equal(t, []post{{th, "", `Scheduled run "n" could not be delivered: bad prompt`}}, f.n.Posts())
	})
}

// 带标签的 new_thread 运行同样按 TurnEnded 结算；线程在 provisioning 阶段因 orb 失败进入 error 时按 thread_error 结算。
func TestTaggedNewThread(t *testing.T) {
	f := newFixture(t)
	s := f.putNew(Input{Name: "tick", Kind: KindEvery, Every: "1h", Prompt: "p"})
	f.advanceTick(time.Hour)
	c := f.th.Creates()[0]
	tid := *f.runs(s.ID)[0].ThreadID
	f.state(tid, apigen.ThreadStateWorking)
	f.state(tid, apigen.ThreadStateIdle)
	f.turn(tid, c.Tag, threads.TurnError, "model not found")
	f.stateEv(tid, apigen.ThreadStateError, true)
	r := f.runs(s.ID)[0]
	assert.Equal(t, runFailed, r.Status)
	assert.Equal(t, "model not found", deref(r.Error))

	f.advanceTick(time.Hour)
	tid2 := *f.runs(s.ID)[0].ThreadID
	f.stateEv(tid2, apigen.ThreadStateError, false)
	assert.Equal(t, "thread_error", deref(f.runs(s.ID)[0].Error))
	assert.EqualValues(t, 2, f.sched(s.ID).Failures)
}

// ---- 恢复运行（补充契约 C1.6 / C1.7） ----

// recoverFixture 建一个名为 name 的线程内定时（every 1h）。
func recoverFixture(t *testing.T, name string) (*fixture, string, string) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Name: name, Kind: KindEvery, Every: "1h", Prompt: "p"})
	return f, th, s.ID
}

// restarted 是重启后重新登记的一次运行。
type restarted struct{ id string }

// deliverAndRestart 触发并应答下一次运行、线程进入 working，然后重启（命令 id 丢失，运行没有标签）。
func (f *fixture) deliverAndRestart(th string, i int) restarted {
	f.t.Helper()
	f.advanceTick(time.Hour)
	f.ack(i, nil)
	f.state(th, apigen.ThreadStateWorking)
	f.s = f.newService()
	f.s.recover(f.ctx)
	f.s.mu.Lock()
	runID := f.s.open[th]
	_, tagged := f.s.tagged[runID]
	_, recovered := f.s.recovered[runID]
	f.s.mu.Unlock()
	require.False(f.t, tagged, "恢复运行不在 tagged 里")
	require.True(f.t, recovered)
	return restarted{id: runID}
}

// 重启后按 agent_start → terminal agent_end(stopReason=error) → prompt_result(error) 连续 3 次：都 failed、自动暂停。
func TestRecoveredPromptErrorFailsAndPauses(t *testing.T) {
	f, th, sid := recoverFixture(t, "nightly")
	for i := range 3 {
		run := f.deliverAndRestart(th, i)
		require.NotEmpty(t, run.id)
		require.Equal(t, runRunning, f.run(run.id).Status)
		// agent_end 的条目入库后线程 idle，随后 prompt_result(error)
		f.th.setOutcome(th, &threads.TurnEvent{Status: threads.TurnError, Error: "provider exploded"})
		f.state(th, apigen.ThreadStateIdle)
		f.stateEv(th, apigen.ThreadStateError, true)
		r := f.run(run.id)
		assert.Equal(t, runFailed, r.Status, "第 %d 次", i+1)
		assert.Equal(t, "provider exploded", deref(r.Error))

		calls := f.th.Recovers()
		require.NotEmpty(t, calls)
		last := calls[len(calls)-1]
		assert.Equal(t, th, last.threadID)
		assert.Equal(t, f.th.Delivers()[i].text, last.anchor, "锚点落在本运行投递的头行")
		assert.Equal(t, r.StartedAt, last.since)
		f.th.setOutcome(th, nil)
	}
	sc := f.sched(sid)
	assert.EqualValues(t, 3, sc.Failures)
	assert.False(t, sc.Enabled)
	assert.Equal(t, pausedFailures, deref(sc.PausedReason))
}

// 投递 → 改名并改时区 → 重启 → 条目晚到：旧消息的头行仍是旧名字、旧时区的偏移；锚点既不依赖名称也不依赖
// 定时现在的时区，按条目结算而不是等到超时。
func TestRecoveredAfterRenameAndTimezone(t *testing.T) {
	f, th, sid := recoverFixture(t, "old name")
	f.advanceTick(time.Hour)
	f.ack(0, nil)
	f.state(th, apigen.ThreadStateWorking)
	require.Contains(t, f.th.Delivers()[0].text, "scheduled for 2026-11-14T10:00:00Z (UTC)")
	_, err := f.s.Put(f.ctx, sid, Input{Name: "new name", Kind: KindEvery, Every: "1h", Prompt: "p", Timezone: "Asia/Tokyo"}, "user")
	require.NoError(t, err)
	runID := f.runs(sid)[0].ID
	f.s = f.newService()
	f.s.recover(f.ctx)
	f.state(th, apigen.ThreadStateIdle)
	assert.Equal(t, runRunning, f.run(runID).Status)

	f.th.setOutcome(th, &threads.TurnEvent{Status: threads.TurnCompleted})
	f.s.EntriesAppended(th)
	f.process()
	assert.Equal(t, runCompleted, f.run(runID).Status)
	assert.Equal(t, 0, f.timers.Fire(recoverSettleTimeout), "兜底超时已撤掉")
}

// 上一轮的消息迟到：since 之后先出现上一个运行的头行（scheduled for 不同），再出现本运行的 → 锚点落在本运行。
func TestRecoveredPreviousRunLate(t *testing.T) {
	f, th, sid := recoverFixture(t, "late")
	// 第 1 个运行：已下发后线程因 orb 失败进入 error，按 thread_error 结束；它的消息其实还压在队列里
	f.advanceTick(time.Hour)
	f.ack(0, nil)
	f.stateEv(th, apigen.ThreadStateError, false)
	require.Equal(t, runFailed, f.runs(sid)[0].Status)
	// 第 2 个运行
	f.advanceTick(time.Hour)
	f.ack(1, nil)
	f.state(th, apigen.ThreadStateWorking)
	run2 := f.runs(sid)[0]
	f.s = f.newService()
	f.s.recover(f.ctx)

	// 转录：第 1 个运行的消息迟到（时间戳在第 2 个运行开始之后），排在第 2 个运行的消息之前
	ts := f.th.turns(th)
	require.Len(t, ts, 2)
	old, cur := ts[0], ts[1]
	old.ts = cur.ts
	old.ev = &threads.TurnEvent{Status: threads.TurnCompleted, Text: "old reply", HasText: true}
	cur.ev = &threads.TurnEvent{Status: threads.TurnError, Error: "provider exploded"}
	f.state(th, apigen.ThreadStateIdle)

	r := f.run(run2.ID)
	assert.Equal(t, runFailed, r.Status, "按本运行的回合结算，不是上一轮的")
	assert.Equal(t, "provider exploded", deref(r.Error))
	calls := f.th.Recovers()
	require.NotEmpty(t, calls)
	assert.Equal(t, f.th.Delivers()[1].text, calls[len(calls)-1].anchor)
	assert.EqualValues(t, 2, f.sched(sid).Failures)
}

// 手动触发的 ScheduledFor 带非零毫秒（头行只写到秒）：重启后仍能认出本运行的头行并结算。
func TestRecoveredManualRunWithMillis(t *testing.T) {
	f, th, sid := recoverFixture(t, "manual")
	f.clock.Advance(123 * time.Millisecond)
	run, err := f.s.RunNow(f.ctx, sid)
	require.NoError(t, err)
	require.EqualValues(t, 123, run.ScheduledFor%1000)
	f.ack(0, nil)
	f.state(th, apigen.ThreadStateWorking)
	f.s = f.newService()
	f.s.recover(f.ctx)
	f.th.setOutcome(th, &threads.TurnEvent{Status: threads.TurnCompleted})
	f.state(th, apigen.ThreadStateIdle)
	assert.Equal(t, runCompleted, f.run(run.ID).Status)
}

func TestRunAnchor(t *testing.T) {
	sf := time.Date(2026, 11, 14, 10, 0, 0, 456_000_000, time.UTC).UnixMilli()
	m := runAnchor(sf)
	for _, c := range []struct {
		text string
		want bool
	}{
		{"[Oryn scheduled run \"a\" #1 · scheduled for 2026-11-14T10:00:00Z (UTC)]\np", true},
		{"[Oryn scheduled run \"a\" #1 · scheduled for 2026-11-14T19:00:00+09:00 (Asia/Tokyo) · late, 3 missed runs coalesced · defined by an agent]", true},
		{"[Oryn scheduled run \"x · scheduled for 2026-11-14T09:00:00Z\" #2 · scheduled for 2026-11-14T10:00:00Z (UTC)]", true},
		{"[Oryn scheduled run \"x · scheduled for 2026-11-14T10:00:00Z\" #2 · scheduled for 2026-11-14T09:00:00Z (UTC)]", false},
		{"[Oryn scheduled run \"a\" #1 · scheduled for 2026-11-14T10:00:01Z (UTC)]", false},
		{"[Oryn scheduled run \"a\" #1 · scheduled for not-a-time (UTC)]", false},
		{"[Oryn scheduled run \"a\" #1 (UTC)]", false},
		{"hello\n[Oryn scheduled run \"a\" #1 · scheduled for 2026-11-14T10:00:00Z (UTC)]", false},
		{"[Oryn scheduled run \"a\" #1]\nsee · scheduled for 2026-11-14T10:00:00Z here", false},
	} {
		assert.Equal(t, c.want, m(c.text), c.text)
	}
}

// 先 idle、条目晚到：idle 时推断不出不结算；之后只来 EntriesAppended（不再有任何状态事件）也能结算。
func TestRecoveredEntriesArriveLate(t *testing.T) {
	for _, c := range []struct {
		name      string
		ev        threads.TurnEvent
		status    string
		failures1 int64
	}{
		{"completed", threads.TurnEvent{Status: threads.TurnCompleted, Text: "ok", HasText: true}, runCompleted, 0},
		{"error", threads.TurnEvent{Status: threads.TurnError, Error: "provider exploded"}, runFailed, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, th, sid := recoverFixture(t, "late")
			f.exec(`UPDATE schedules SET failures = 1 WHERE id = ?`, sid)
			run := f.deliverAndRestart(th, 0)

			// 线程仍 working：条目入库不触发重查
			f.s.EntriesAppended(th)
			f.s.mu.Lock()
			assert.Empty(t, f.s.queue, "没有等终态的恢复运行时不入队")
			f.s.mu.Unlock()
			assert.NotContains(t, f.timers.Active(), recoverSettleTimeout)

			f.state(th, apigen.ThreadStateIdle)
			assert.Equal(t, runRunning, f.run(run.id).Status, "推断不出：保持未结束")
			assert.Contains(t, f.timers.Active(), recoverSettleTimeout, "第一次推断不出时上兜底超时")

			f.th.setOutcome(th, &c.ev)
			f.s.EntriesAppended(th)
			f.process()
			r := f.run(run.id)
			assert.Equal(t, c.status, r.Status)
			if c.status == runFailed {
				assert.Equal(t, "provider exploded", deref(r.Error))
			}
			assert.Equal(t, c.failures1, f.sched(sid).Failures)
			assert.NotContains(t, f.timers.Active(), recoverSettleTimeout, "结算后撤掉兜底超时")
			assert.Empty(t, f.s.recovered)
		})
	}
}

// 兜底超时（注入的定时器）：仍推断不出 → outcome_unknown，不计连续失败。
func TestRecoveredSettleTimeout(t *testing.T) {
	f, th, sid := recoverFixture(t, "lost")
	f.exec(`UPDATE schedules SET failures = 1 WHERE id = ?`, sid)
	run := f.deliverAndRestart(th, 0)
	f.state(th, apigen.ThreadStateIdle)
	f.clock.Advance(recoverSettleTimeout)
	require.Equal(t, 1, f.timers.Fire(recoverSettleTimeout))
	f.process()
	r := f.run(run.id)
	assert.Equal(t, runFailed, r.Status)
	assert.Equal(t, "outcome_unknown", deref(r.Error))
	assert.EqualValues(t, 1, f.sched(sid).Failures)
	assert.Empty(t, f.s.open)
	// 超时之后的条目与状态事件不再改动它
	f.th.setOutcome(th, &threads.TurnEvent{Status: threads.TurnCompleted})
	f.s.EntriesAppended(th)
	f.state(th, apigen.ThreadStateIdle)
	assert.Equal(t, runFailed, f.run(run.id).Status)
}

// 恢复运行遇到非 prompt 原因的 error：照旧 thread_error 并计失败。
func TestRecoveredNonPromptError(t *testing.T) {
	f, th, sid := recoverFixture(t, "orb")
	run := f.deliverAndRestart(th, 0)
	f.stateEv(th, apigen.ThreadStateError, false)
	r := f.run(run.id)
	assert.Equal(t, runFailed, r.Status)
	assert.Equal(t, "thread_error", deref(r.Error))
	assert.EqualValues(t, 1, f.sched(sid).Failures)
}
