package schedule

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// ---- 假时钟与手动定时器 ----

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fakeTimer struct {
	d              time.Duration
	f              func()
	stopped, fired bool
}

type fakeTimers struct {
	mu sync.Mutex
	ts []*fakeTimer
	// armed 非 nil 时每登记一个定时器就发送其时长（Run 循环的测试用它同步）。
	armed chan time.Duration
}

func (ft *fakeTimers) AfterFunc(d time.Duration, f func()) func() {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	t := &fakeTimer{d: d, f: f}
	ft.ts = append(ft.ts, t)
	if ft.armed != nil {
		ft.armed <- d
	}
	return func() {
		ft.mu.Lock()
		t.stopped = true
		ft.mu.Unlock()
	}
}

// Fire 触发全部时长为 d、未停止的定时器，返回个数。
func (ft *fakeTimers) Fire(d time.Duration) int {
	ft.mu.Lock()
	var due []*fakeTimer
	for _, t := range ft.ts {
		if !t.stopped && !t.fired && t.d == d {
			t.fired = true
			due = append(due, t)
		}
	}
	ft.mu.Unlock()
	for _, t := range due {
		t.f()
	}
	return len(due)
}

// Active 返回未停止、未触发定时器的时长。
func (ft *fakeTimers) Active() []time.Duration {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var out []time.Duration
	for _, t := range ft.ts {
		if !t.stopped && !t.fired {
			out = append(out, t.d)
		}
	}
	return out
}

// ---- 假线程服务、orb 服务与通知 ----

type deliverCall struct {
	threadID string
	text     string
	ack      func(error)
	canceled bool
}

type fakeThreads struct {
	st *store.Store

	mu         sync.Mutex
	delivers   []*deliverCall
	deliverErr error
	cancelOK   bool
	creates    []threads.CreateParams
	createErr  error
	orbFor     func(n int) *string // 第 n 个（从 1 起）建出的线程挂的 orb
	archived   []string
	published  []string
	// delivered 非 nil 时每次成功投递发送线程 id。
	delivered chan string
}

func (f *fakeThreads) Get(ctx context.Context, id string) (apigen.Thread, error) {
	row, err := f.st.Q.GetThread(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.Thread{}, threads.ErrNotFound
	}
	if err != nil {
		return apigen.Thread{}, err
	}
	return apigen.Thread{ID: row.ID, Title: row.Title, State: apigen.ThreadState(row.State), ArchivedAt: row.ArchivedAt}, nil
}

func (f *fakeThreads) Deliver(_ context.Context, threadID string, m threads.AutoMessage, ack func(error)) (func() bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deliverErr != nil {
		return nil, f.deliverErr
	}
	c := &deliverCall{threadID: threadID, text: m.Text, ack: ack}
	f.delivers = append(f.delivers, c)
	if f.delivered != nil {
		f.delivered <- threadID
	}
	return func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		c.canceled = true
		return f.cancelOK
	}, nil
}

func (f *fakeThreads) Publish(_ context.Context, threadID string) {
	f.mu.Lock()
	f.published = append(f.published, threadID)
	f.mu.Unlock()
}

func (f *fakeThreads) Create(ctx context.Context, p threads.CreateParams) (apigen.Thread, error) {
	f.mu.Lock()
	err := f.createErr
	f.creates = append(f.creates, p)
	n := len(f.creates)
	orbFor := f.orbFor
	f.mu.Unlock()
	if err != nil {
		return apigen.Thread{}, err
	}
	id := ids.Thread()
	var orb *string
	if orbFor != nil {
		orb = orbFor(n)
	}
	now := int64(1_000 + n) // 按创建顺序递增，ListScheduleThreads 依此排序
	if err := f.st.Q.CreateThread(ctx, db.CreateThreadParams{
		Kind: "work", ID: id, Title: p.Title, TitleSource: "user", State: string(apigen.ThreadStateProvisioning),
		SessionDir: "~/.oryn/threads/" + id, CreatedAt: now, UpdatedAt: now, OrbID: orb,
		ProjectID: optStr(p.ProjectID), ScheduleID: optStr(p.ScheduleID), ScheduleRun: &p.ScheduleRun,
	}); err != nil {
		return apigen.Thread{}, err
	}
	return apigen.Thread{ID: id, Title: p.Title, State: apigen.ThreadStateProvisioning}, nil
}

func (f *fakeThreads) Archive(ctx context.Context, id string) error {
	f.mu.Lock()
	f.archived = append(f.archived, id)
	f.mu.Unlock()
	_, err := f.st.DB.ExecContext(ctx, `UPDATE threads SET archived_at = 1 WHERE id = ?`, id)
	return err
}

func (f *fakeThreads) Delivers() []*deliverCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*deliverCall(nil), f.delivers...)
}

func (f *fakeThreads) Creates() []threads.CreateParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]threads.CreateParams(nil), f.creates...)
}

type fakeOrbs struct {
	mu      sync.Mutex
	deleted []string
}

func (o *fakeOrbs) Delete(_ context.Context, orbID string) error {
	o.mu.Lock()
	o.deleted = append(o.deleted, orbID)
	o.mu.Unlock()
	return nil
}

type post struct{ threadID, title, body string }

type fakeNotifier struct {
	mu    sync.Mutex
	posts []post
}

func (n *fakeNotifier) Post(_ context.Context, threadID, title, body string) error {
	n.mu.Lock()
	n.posts = append(n.posts, post{threadID, title, body})
	n.mu.Unlock()
	return nil
}

func (n *fakeNotifier) Posts() []post {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]post(nil), n.posts...)
}

// ---- 夹具 ----

const deliverTimeout = 7 * time.Minute

type fixture struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	clock  *fakeClock
	timers *fakeTimers
	th     *fakeThreads
	orbs   *fakeOrbs
	n      *fakeNotifier
	s      *Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{t: t, ctx: ctx, st: st, clock: &fakeClock{now: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)},
		timers: &fakeTimers{}, th: &fakeThreads{st: st}, orbs: &fakeOrbs{}, n: &fakeNotifier{}}
	f.s = f.newService()
	return f
}

// newService 在同一个库上新建调度器（模拟控制面重启）。
func (f *fixture) newService() *Service {
	return New(Config{Now: f.clock.Now, AfterFunc: f.timers.AfterFunc, DeliverTimeout: deliverTimeout}, f.st, f.th, f.orbs, f.n)
}

func (f *fixture) thread() string {
	f.t.Helper()
	id := ids.Thread()
	require.NoError(f.t, f.st.Q.CreateThread(f.ctx, db.CreateThreadParams{Kind: "work", ID: id, Title: "work " + id[len(id)-4:],
		TitleSource: "user", State: string(apigen.ThreadStateIdle), SessionDir: "~/.oryn/threads/" + id, CreatedAt: 1, UpdatedAt: 1}))
	return id
}

func (f *fixture) project(name string) string {
	f.t.Helper()
	id := ids.New("prj")
	require.NoError(f.t, f.st.Q.CreateProject(f.ctx, db.CreateProjectParams{ID: id, Name: name, CreatedAt: 1, UpdatedAt: 1}))
	return id
}

func (f *fixture) orb() string {
	f.t.Helper()
	if _, err := f.st.DB.ExecContext(f.ctx, `INSERT OR IGNORE INTO providers (id, name, token_hash, created_at) VALUES ('prov_t', 'local', 'h', 1)`); err != nil {
		f.t.Fatal(err)
	}
	id := "orb-" + ids.New("x")[2:]
	_, err := f.st.DB.ExecContext(f.ctx, `INSERT INTO orbs (id, provider_id, type, cpus, memory_mb, image, state, state_changed_at, token_hash, created_at)
		VALUES (?, 'prov_t', 'small', 1, 1024, 'img', 'ready', 1, ?, 1)`, id, id)
	require.NoError(f.t, err)
	return id
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	_, err := f.st.DB.ExecContext(f.ctx, q, args...)
	require.NoError(f.t, err)
}

// state 写入线程状态并发出状态事件，然后处理事件。
func (f *fixture) state(threadID string, state apigen.ThreadState) {
	f.t.Helper()
	f.exec(`UPDATE threads SET state = ? WHERE id = ?`, string(state), threadID)
	f.s.StateChanged(threads.StateEvent{ThreadID: threadID, State: string(state)})
	f.process()
}

func (f *fixture) process() {
	f.s.loopMu.Lock()
	f.s.processEvents(f.ctx)
	f.s.loopMu.Unlock()
}

// ack 应答第 i 次投递并处理事件。
func (f *fixture) ack(i int, err error) {
	f.t.Helper()
	ds := f.th.Delivers()
	require.Greater(f.t, len(ds), i)
	ds[i].ack(err)
	f.process()
}

func (f *fixture) advanceTick(d time.Duration) {
	f.clock.Advance(d)
	f.s.Tick(f.ctx)
}

func (f *fixture) sched(id string) db.Schedule {
	f.t.Helper()
	s, err := f.st.Q.GetSchedule(f.ctx, id)
	require.NoError(f.t, err)
	return s
}

func (f *fixture) runs(id string) []db.ScheduleRun {
	f.t.Helper()
	rs, err := f.st.Q.ListScheduleRuns(f.ctx, db.ListScheduleRunsParams{ScheduleID: id, Limit: 1000})
	require.NoError(f.t, err)
	return rs
}

func (f *fixture) putThread(threadID string, in Input) apigen.Schedule {
	f.t.Helper()
	s, err := f.s.PutForThread(f.ctx, threadID, in, "user")
	require.NoError(f.t, err)
	return s
}

func (f *fixture) putNew(in Input) apigen.Schedule {
	f.t.Helper()
	in.Mode = ModeNewThread
	s, err := f.s.Put(f.ctx, "", in, "user")
	require.NoError(f.t, err)
	return s
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var se *Error
	require.ErrorAs(t, err, &se, "err = %v", err)
	assert.Equal(t, code, se.Code)
}

func ms(t time.Time) int64 { return t.UnixMilli() }

// ---- 线程内定时 ----

func TestOnceDeliversOnceThenDone(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	at := f.clock.Now().Add(time.Minute).UnixMilli()
	s := f.putThread(th, Input{Name: "daily", Kind: KindOnce, At: &at, Prompt: "run #{{run_number}} at {{scheduled_at}} {x}", Timezone: "Asia/Shanghai"})
	assert.Equal(t, at, *s.NextRunAt)
	assert.Equal(t, []time.Duration{time.Minute}, f.timers.Active(), "按 next_run_at 上定时器")

	f.advanceTick(30 * time.Second)
	assert.Empty(t, f.th.Delivers(), "未到点")
	f.advanceTick(30 * time.Second)
	ds := f.th.Delivers()
	require.Len(t, ds, 1)
	assert.Equal(t, th, ds[0].threadID)
	assert.Equal(t, "[Oryn scheduled run \"daily\" #1 · scheduled for 2026-11-14T17:01:00+08:00 (Asia/Shanghai)]\nrun #1 at 2026-11-14T17:01:00+08:00 {x}", ds[0].text)

	sc := f.sched(s.ID)
	assert.False(t, sc.Enabled)
	assert.Equal(t, "done", *sc.PausedReason)
	assert.Nil(t, sc.NextRunAt)
	assert.EqualValues(t, 1, sc.RunCount)
	rs := f.runs(s.ID)
	require.Len(t, rs, 1)
	assert.Equal(t, runPending, rs[0].Status)
	assert.Equal(t, triggerSchedule, rs[0].Trigger)
	assert.Contains(t, f.timers.Active(), deliverTimeout, "等应答的超时")

	f.ack(0, nil)
	assert.Equal(t, runRunning, f.runs(s.ID)[0].Status)
	assert.NotNil(t, f.runs(s.ID)[0].DeliveredAt)
	assert.NotContains(t, f.timers.Active(), deliverTimeout, "应答后撤掉超时")
	f.state(th, apigen.ThreadStateWorking)
	f.state(th, apigen.ThreadStateIdle)
	r := f.runs(s.ID)[0]
	assert.Equal(t, runCompleted, r.Status)
	assert.NotNil(t, r.FinishedAt)

	f.advanceTick(time.Hour)
	assert.Len(t, f.th.Delivers(), 1, "一次性只投递一次")
	assert.Contains(t, f.th.published, th)
}

func TestCronAdvancesAndEndsAt(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	ends := f.clock.Now().Add(25 * time.Minute).UnixMilli()
	s := f.putThread(th, Input{Kind: KindEvery, Every: "10m", Prompt: "p {{date}} [{{last_run_at}}]", EndsAt: &ends})
	start := f.clock.Now()
	assert.Equal(t, ms(start.Add(10*time.Minute)), *s.NextRunAt)

	f.advanceTick(10 * time.Minute)
	require.Len(t, f.th.Delivers(), 1)
	assert.Contains(t, f.th.Delivers()[0].text, "\np 2026-11-14 []", "首次没有上次运行")
	assert.Equal(t, ms(start.Add(20*time.Minute)), *f.sched(s.ID).NextRunAt)
	f.ack(0, nil)
	f.state(th, apigen.ThreadStateIdle)

	f.advanceTick(10 * time.Minute)
	require.Len(t, f.th.Delivers(), 2)
	assert.Contains(t, f.th.Delivers()[1].text, "[2026-11-14T09:10:00Z]")
	sc := f.sched(s.ID)
	assert.Nil(t, sc.NextRunAt, "下一次越过截止时间")
	assert.False(t, sc.Enabled)
	assert.Equal(t, "done", *sc.PausedReason)
}

func TestMissedRunsCoalesced(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Name: "m", Kind: KindCron, Cron: "* * * * *", Prompt: "p"})
	f.advanceTick(5*time.Minute + 30*time.Second)
	ds := f.th.Delivers()
	require.Len(t, ds, 1, "只补一次")
	assert.Contains(t, ds[0].text, "· late, 4 missed runs coalesced]")
	r := f.runs(s.ID)[0]
	assert.Equal(t, triggerCatchup, r.Trigger)
	assert.EqualValues(t, 4, r.Missed)
	assert.Equal(t, ms(time.Date(2026, 11, 14, 9, 1, 0, 0, time.UTC)), r.ScheduledFor)
	assert.Greater(t, *f.sched(s.ID).NextRunAt, ms(f.clock.Now()))
}

func TestRestartCatchesUpOnce(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Kind: KindEvery, Every: "1h", Prompt: "p"})
	// 控制面停了 5 小时
	f.clock.Advance(5*time.Hour + time.Minute)
	f.s = f.newService()
	f.s.recover(f.ctx)
	f.s.Tick(f.ctx)
	require.Len(t, f.th.Delivers(), 1)
	r := f.runs(s.ID)[0]
	assert.Equal(t, triggerCatchup, r.Trigger)
	assert.EqualValues(t, 4, r.Missed)
	assert.Equal(t, ms(f.clock.Now().Add(time.Hour)), *f.sched(s.ID).NextRunAt, "every 取 now + d")
}

func TestThreadOverlapSkips(t *testing.T) {
	for _, st := range []apigen.ThreadState{apigen.ThreadStateWorking, apigen.ThreadStateAwaitingInput} {
		t.Run(string(st), func(t *testing.T) {
			f := newFixture(t)
			th := f.thread()
			s := f.putThread(th, Input{Kind: KindEvery, Every: "1m", Prompt: "p"})
			f.advanceTick(time.Minute)
			f.ack(0, nil)
			f.state(th, apigen.ThreadStateWorking)
			if st == apigen.ThreadStateAwaitingInput {
				f.state(th, st)
				assert.Equal(t, runNeedsInput, f.runs(s.ID)[0].Status)
			}
			f.advanceTick(time.Minute)
			assert.Len(t, f.th.Delivers(), 1, "上次未结束：不投递")
			rs := f.runs(s.ID)
			require.Len(t, rs, 2)
			assert.Equal(t, runSkipped, rs[0].Status)
			assert.Equal(t, "overlap", *rs[0].Error)
			assert.EqualValues(t, 0, f.sched(s.ID).Failures, "跳过不计失败")

			_, err := f.s.RunNow(f.ctx, s.ID)
			requireCode(t, err, CodeScheduleBusy)

			f.state(th, apigen.ThreadStateWorking)
			f.state(th, apigen.ThreadStateIdle)
			assert.Equal(t, runCompleted, f.runs(s.ID)[1].Status)
			f.advanceTick(time.Minute)
			assert.Len(t, f.th.Delivers(), 2)
		})
	}
}

func TestFailuresAutoPauseAndNotify(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Name: "nightly", Kind: KindEvery, Every: "1m", Prompt: "p"})

	// 1：同步投递失败
	f.th.deliverErr = &threads.Error{Code: "queue_full", Message: "queue is full"}
	f.advanceTick(time.Minute)
	f.th.deliverErr = nil
	assert.Equal(t, runFailed, f.runs(s.ID)[0].Status)
	assert.Equal(t, "queue_full: queue is full", *f.runs(s.ID)[0].Error)
	// 2：应答出错
	f.advanceTick(time.Minute)
	f.ack(0, errors.New("rejected"))
	assert.Equal(t, runFailed, f.runs(s.ID)[0].Status)
	assert.EqualValues(t, 2, f.sched(s.ID).Failures)
	// 3：线程进入 error（M5 已有通知，不再 Post）
	f.advanceTick(time.Minute)
	f.ack(1, nil)
	f.state(th, apigen.ThreadStateWorking)
	f.state(th, apigen.ThreadStateError)
	assert.Equal(t, "thread_error", *f.runs(s.ID)[0].Error)

	sc := f.sched(s.ID)
	assert.False(t, sc.Enabled)
	assert.Equal(t, "failures", *sc.PausedReason)
	assert.EqualValues(t, 3, sc.Failures)
	assert.Equal(t, []post{
		{th, "", `Scheduled run "nightly" could not be delivered: queue_full: queue is full`},
		{th, "", `Scheduled run "nightly" could not be delivered: rejected`},
		{th, "", `Schedule "nightly" was paused after 3 consecutive failures`},
	}, f.n.Posts())

	f.advanceTick(time.Hour)
	assert.Len(t, f.th.Delivers(), 2, "暂停后不再触发")
	// 恢复：清零失败、不补跑
	got, err := f.s.SetEnabled(f.ctx, s.ID, true)
	require.NoError(t, err)
	assert.Equal(t, 0, got.Failures)
	assert.Equal(t, ms(f.clock.Now().Add(time.Minute)), *got.NextRunAt)
	assert.Len(t, f.th.Delivers(), 2)
}

func TestCompletionResetsFailures(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Kind: KindEvery, Every: "1m", Prompt: "p"})
	f.advanceTick(time.Minute)
	f.ack(0, errors.New("x"))
	assert.EqualValues(t, 1, f.sched(s.ID).Failures)
	f.advanceTick(time.Minute)
	f.ack(1, nil)
	f.state(th, apigen.ThreadStateIdle)
	assert.EqualValues(t, 0, f.sched(s.ID).Failures)
}

func TestDeliverTimeout(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Kind: KindEvery, Every: "1h", Prompt: "p"})

	// 已下发（cancel 返回 false）：继续等应答
	f.advanceTick(time.Hour)
	require.Equal(t, 1, f.timers.Fire(deliverTimeout))
	f.process()
	assert.True(t, f.th.Delivers()[0].canceled)
	assert.Equal(t, runPending, f.runs(s.ID)[0].Status)
	f.ack(0, nil)
	assert.Equal(t, runRunning, f.runs(s.ID)[0].Status)
	f.state(th, apigen.ThreadStateIdle)

	// 仍在排队：撤回并记 deliver_timeout
	f.th.cancelOK = true
	f.advanceTick(time.Hour)
	require.Equal(t, 1, f.timers.Fire(deliverTimeout))
	f.process()
	r := f.runs(s.ID)[0]
	assert.Equal(t, runFailed, r.Status)
	assert.Equal(t, "deliver_timeout", *r.Error)
	assert.Len(t, f.n.Posts(), 1)
	f.ack(1, nil) // 撤回之后迟到的应答被忽略
	assert.Equal(t, runFailed, f.runs(s.ID)[0].Status)
}

func TestArchivedThreadPauses(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Kind: KindEvery, Every: "1m", Prompt: "p"})
	f.exec(`UPDATE threads SET archived_at = 1 WHERE id = ?`, th)
	f.advanceTick(time.Minute)
	assert.Empty(t, f.th.Delivers())
	r := f.runs(s.ID)[0]
	assert.Equal(t, runSkipped, r.Status)
	assert.Equal(t, "thread_archived", *r.Error)
	sc := f.sched(s.ID)
	assert.False(t, sc.Enabled)
	assert.Equal(t, "archived", *sc.PausedReason)

	_, err := f.s.PutForThread(f.ctx, th, Input{Kind: KindEvery, Every: "1m", Prompt: "p"}, "user")
	requireCode(t, err, CodeThreadArchived)
	_, err = f.s.SetEnabled(f.ctx, s.ID, true)
	requireCode(t, err, CodeThreadArchived)

	// 删除线程：定时与运行记录级联消失
	f.exec(`DELETE FROM threads WHERE id = ?`, th)
	_, err = f.s.Get(f.ctx, s.ID)
	requireCode(t, err, CodeScheduleNotFound)
	assert.Empty(t, f.runs(s.ID))
}

func TestRunNowAndSetEnabled(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Kind: KindEvery, Every: "1h", Prompt: "p", Enabled: new(false)})
	assert.Nil(t, s.NextRunAt)
	assert.Equal(t, apigen.SchedulePausedReasonUser, *s.PausedReason)

	run, err := f.s.RunNow(f.ctx, s.ID)
	require.NoError(t, err, "已暂停的定时也能手动触发")
	assert.Equal(t, apigen.ScheduleRunTriggerManual, run.Trigger)
	assert.Equal(t, apigen.ScheduleRunStatusPending, run.Status)
	require.Len(t, f.th.Delivers(), 1)
	assert.Contains(t, f.th.Delivers()[0].text, `#1 · scheduled for 2026-11-14T09:00:00Z (UTC)]`)
	assert.Nil(t, f.sched(s.ID).NextRunAt, "手动触发不改 next_run_at")
	_, err = f.s.RunNow(f.ctx, s.ID)
	requireCode(t, err, CodeScheduleBusy)

	// 一次性已结束：恢复要先改时间
	th2 := f.thread()
	at := f.clock.Now().Add(time.Minute).UnixMilli()
	o := f.putThread(th2, Input{Kind: KindOnce, At: &at, Prompt: "p"})
	f.advanceTick(time.Minute)
	_, err = f.s.SetEnabled(f.ctx, o.ID, true)
	requireCode(t, err, CodeInvalidSchedule)

	_, err = f.s.RunNow(f.ctx, "sch_missing")
	requireCode(t, err, CodeScheduleNotFound)
}

func TestRecoveryBranches(t *testing.T) {
	f := newFixture(t)
	mk := func(state apigen.ThreadState) (string, string) {
		th := f.thread()
		f.exec(`UPDATE threads SET state = ? WHERE id = ?`, string(state), th)
		s := f.putThread(th, Input{Kind: KindEvery, Every: "1h", Prompt: "p"})
		return th, s.ID
	}
	insert := func(sid, th, status string, started time.Time) string {
		id := ids.New("srn")
		require.NoError(t, f.st.Q.InsertScheduleRun(f.ctx, db.InsertScheduleRunParams{ID: id, ScheduleID: sid, ThreadID: &th,
			Trigger: triggerSchedule, ScheduledFor: ms(started), StartedAt: ms(started), Status: status}))
		return id
	}
	now := f.clock.Now()
	thP, sP := mk(apigen.ThreadStateIdle)
	rPending := insert(sP, thP, runPending, now.Add(-time.Minute))
	thS, sS := mk(apigen.ThreadStateWorking)
	rStale := insert(sS, thS, runRunning, now.Add(-25*time.Hour))
	thI, sI := mk(apigen.ThreadStateIdle)
	rIdle := insert(sI, thI, runRunning, now.Add(-time.Minute))
	thE, sE := mk(apigen.ThreadStateError)
	rErr := insert(sE, thE, runRunning, now.Add(-time.Minute))
	thW, sW := mk(apigen.ThreadStateAwaitingInput)
	rWait := insert(sW, thW, runRunning, now.Add(-time.Minute))

	s := f.newService()
	s.recover(f.ctx)
	get := func(id string) db.ScheduleRun {
		r, err := f.st.Q.GetScheduleRun(f.ctx, id)
		require.NoError(t, err)
		return r
	}
	assert.Equal(t, "interrupted", *get(rPending).Error)
	assert.Equal(t, "stale", *get(rStale).Error)
	assert.Equal(t, runCompleted, get(rIdle).Status)
	assert.Equal(t, runFailed, get(rErr).Status)
	assert.EqualValues(t, 1, f.sched(sE).Failures)
	assert.Equal(t, runNeedsInput, get(rWait).Status)
	assert.Equal(t, rWait, s.open[thW], "仍未结束的运行重新登记")

	f.s = s
	f.state(thW, apigen.ThreadStateWorking)
	f.state(thW, apigen.ThreadStateIdle)
	assert.Equal(t, runCompleted, get(rWait).Status)
}

func TestScheduleDefinitionRules(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	s := f.putThread(th, Input{Name: "a", Kind: KindEvery, Every: "1h", Prompt: "p"})
	// 替换：保留 id 与 run_count，清零失败
	f.exec(`UPDATE schedules SET failures = 2, run_count = 7 WHERE id = ?`, s.ID)
	s2 := f.putThread(th, Input{Name: "b", Kind: KindCron, Cron: "0 9 * * *", Prompt: "q"})
	assert.Equal(t, s.ID, s2.ID)
	assert.EqualValues(t, 7, s2.RunCount)
	assert.Equal(t, 0, s2.Failures)
	assert.Equal(t, "0 9 * * *", *s2.Cron)
	assert.Nil(t, s2.Every)

	// POST 同一线程的第二个定时 → conflict
	_, err := f.s.Put(f.ctx, "", Input{Mode: ModeThread, ThreadID: th, Kind: KindEvery, Every: "1h", Prompt: "p"}, "user")
	requireCode(t, err, CodeConflict)
	_, err = f.s.Put(f.ctx, "", Input{Kind: KindEvery, Every: "1h", Prompt: "p"}, "user")
	requireCode(t, err, CodeInvalidSchedule)
	_, err = f.s.PutForThread(f.ctx, "T-missing", Input{Kind: KindEvery, Every: "1h", Prompt: "p"}, "user")
	requireCode(t, err, CodeNotFound)
	// 目标不可改
	_, err = f.s.Put(f.ctx, s.ID, Input{Mode: ModeNewThread, Name: "x", Kind: KindEvery, Every: "1h", Prompt: "p"}, "user")
	requireCode(t, err, CodeInvalidSchedule)
	_, err = f.s.Put(f.ctx, s.ID, Input{ThreadID: f.thread(), Kind: KindEvery, Every: "1h", Prompt: "p"}, "user")
	requireCode(t, err, CodeInvalidSchedule)

	got, err := f.s.GetByThread(f.ctx, th)
	require.NoError(t, err)
	assert.Equal(t, s.ID, got.ID)
	require.NoError(t, f.s.DeleteByThread(f.ctx, th))
	_, err = f.s.GetByThread(f.ctx, th)
	requireCode(t, err, CodeScheduleNotFound)
	requireCode(t, f.s.Delete(f.ctx, s.ID), CodeScheduleNotFound)

	pv, err := f.s.Preview(Input{Kind: KindCron, Cron: "0 9 * * 1-5", Timezone: "Asia/Shanghai"})
	require.NoError(t, err)
	require.Len(t, pv, 5)
	assert.Equal(t, ms(time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)), pv[0], "周一上海 9 点")
	_, err = f.s.Preview(Input{Kind: KindCron, Cron: "61 * * * *"})
	requireCode(t, err, CodeInvalidSchedule)
}

func TestEnabledLimit(t *testing.T) {
	f := newFixture(t)
	p := f.project("p")
	for i := range maxEnabledSchedules {
		f.putNew(Input{ProjectID: p, Name: fmt.Sprintf("s%d", i), Kind: KindEvery, Every: "1h", Prompt: "p"})
	}
	_, err := f.s.Put(f.ctx, "", Input{Mode: ModeNewThread, ProjectID: p, Name: "over", Kind: KindEvery, Every: "1h", Prompt: "p"}, "user")
	requireCode(t, err, CodeScheduleLimit)
	off := f.putNew(Input{ProjectID: p, Name: "off", Kind: KindEvery, Every: "1h", Prompt: "p", Enabled: new(false)})
	_, err = f.s.SetEnabled(f.ctx, off.ID, true)
	requireCode(t, err, CodeScheduleLimit)
}

// ---- new_thread ----

func TestNewThreadCreatesThread(t *testing.T) {
	f := newFixture(t)
	p := f.project("sandbox")
	spc := ids.New("spc")
	chat := f.thread()
	f.exec(`INSERT INTO spaces (id, name, thread_id, created_at, updated_at) VALUES (?, 'm9', ?, 1, 1)`, spc, chat)
	s := f.putNew(Input{ProjectID: p, SpaceID: spc, Name: "tick", Kind: KindEvery, Every: "1m", Timezone: "Asia/Shanghai",
		Prompt: "only reply run {{run_number}}", Model: &apigen.ModelRef{Provider: "anthropic", ID: "claude/x"}, ThinkingLevel: "high",
		Provider: "local", OrbType: "small"})
	assert.Equal(t, apigen.ScheduleModeNewThread, s.Mode)
	assert.Equal(t, "claude/x", s.Model.ID)

	f.advanceTick(time.Minute)
	cs := f.th.Creates()
	require.Len(t, cs, 1)
	c := cs[0]
	assert.Equal(t, p, c.ProjectID)
	assert.Equal(t, spc, c.SpaceID)
	assert.Equal(t, "local", c.Provider)
	assert.Equal(t, "small", c.Type)
	assert.Equal(t, &apigen.ModelRef{Provider: "anthropic", ID: "claude/x"}, c.Model)
	assert.Equal(t, "high", c.ThinkingLevel)
	assert.Equal(t, "tick · 11-14 17:01", c.Title)
	assert.Equal(t, s.ID, c.ScheduleID)
	assert.EqualValues(t, 1, c.ScheduleRun)
	assert.Equal(t, "[Oryn scheduled run \"tick\" #1 · scheduled for 2026-11-14T17:01:00+08:00 (Asia/Shanghai)]\nonly reply run 1", c.Prompt)
	assert.Empty(t, f.th.Delivers())

	r := f.runs(s.ID)[0]
	assert.Equal(t, runRunning, r.Status)
	require.NotNil(t, r.ThreadID)
	assert.NotNil(t, r.DeliveredAt)
	tid := *r.ThreadID
	f.state(tid, apigen.ThreadStateWorking)
	f.state(tid, apigen.ThreadStateIdle)
	assert.Equal(t, runCompleted, f.runs(s.ID)[0].Status)

	got, err := f.s.Get(f.ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, apigen.ScheduleRunStatusCompleted, got.LastRun.Status)
	assert.Equal(t, 0, got.OpenRuns)
}

func TestNewThreadCreateFailures(t *testing.T) {
	f := newFixture(t)
	s := f.putNew(Input{Name: "bad", Kind: KindEvery, Every: "1h", Prompt: "x", Provider: "nope"})
	f.th.createErr = &threads.Error{Code: "invalid", Message: `provider "nope" not found`}
	for i := range 3 {
		run, err := f.s.RunNow(f.ctx, s.ID)
		require.NoError(t, err, "第 %d 次", i+1)
		assert.Equal(t, apigen.ScheduleRunStatusFailed, run.Status)
		assert.Contains(t, *run.Error, "provider")
		assert.Nil(t, run.ThreadID)
	}
	sc := f.sched(s.ID)
	assert.False(t, sc.Enabled)
	assert.Equal(t, "failures", *sc.PausedReason)
	ps := f.n.Posts()
	require.Len(t, ps, 4)
	for _, p := range ps[:3] {
		assert.Equal(t, post{"", `Schedule "bad"`, `Scheduled run could not start a thread: invalid: provider "nope" not found`}, p)
	}
	assert.Equal(t, post{"", `Schedule "bad"`, `Schedule "bad" was paused after 3 consecutive failures`}, ps[3])
}

func TestNewThreadOverlap(t *testing.T) {
	f := newFixture(t)
	s := f.putNew(Input{Name: "slow", Kind: KindEvery, Every: "1m", Prompt: "p"})
	f.advanceTick(time.Minute)
	first := *f.runs(s.ID)[0].ThreadID
	f.state(first, apigen.ThreadStateWorking)
	f.advanceTick(time.Minute)
	assert.Len(t, f.th.Creates(), 1, "上一线程仍在工作：跳过")
	assert.Equal(t, "overlap", *f.runs(s.ID)[0].Error)
	_, err := f.s.RunNow(f.ctx, s.ID)
	requireCode(t, err, CodeScheduleBusy)

	// 在等主人回答：不挡后续运行，但仍留在未结束表里
	f.state(first, apigen.ThreadStateAwaitingInput)
	f.advanceTick(time.Minute)
	assert.Len(t, f.th.Creates(), 2)
	f.state(first, apigen.ThreadStateWorking)
	f.state(first, apigen.ThreadStateIdle)
	assert.Equal(t, runCompleted, f.runs(s.ID)[2].Status, "最早的运行最终完成")

	// allow：至多 3 个并存
	in := Input{Name: "slow", Kind: KindEvery, Every: "1m", Prompt: "p", Overlap: OverlapAllow}
	_, err = f.s.Put(f.ctx, s.ID, in, "user")
	require.NoError(t, err)
	f.advanceTick(time.Minute) // 第 2 个仍 provisioning（running），这是第 3 个
	f.advanceTick(time.Minute) // 第 4 个
	assert.Len(t, f.th.Creates(), 4)
	f.advanceTick(time.Minute)
	assert.Len(t, f.th.Creates(), 4)
	assert.Equal(t, "overlap_cap", *f.runs(s.ID)[0].Error)
}

func TestNewThreadRetention(t *testing.T) {
	f := newFixture(t)
	shared := f.orb()
	orbs := map[int]string{}
	f.th.orbFor = func(n int) *string {
		if n == 2 {
			return &shared // 第 2 个线程与另一个线程共用 orb
		}
		id := f.orb()
		orbs[n] = id
		return &id
	}
	other := f.thread()
	f.exec(`UPDATE threads SET orb_id = ? WHERE id = ?`, shared, other)

	s := f.putNew(Input{Name: "keep", Kind: KindEvery, Every: "1m", Prompt: "p", Overlap: OverlapAllow, KeepLast: new(2)})
	var tids []string
	for i := range 5 {
		f.advanceTick(time.Minute)
		tid := *f.runs(s.ID)[0].ThreadID
		tids = append(tids, tid)
		switch i {
		case 0, 1:
			f.state(tid, apigen.ThreadStateIdle)
		case 2:
			f.exec(`UPDATE threads SET pinned = 1 WHERE id = ?`, tid)
			f.state(tid, apigen.ThreadStateError)
		}
	}
	// 第 4、5 个仍 provisioning；第 3 个置顶；第 1、2 个静止 → 归档
	assert.ElementsMatch(t, []string{tids[0], tids[1]}, f.th.archived)
	assert.Equal(t, []string{orbs[1]}, f.orbs.deleted, "共用的 orb 不删")
}

func TestNewThreadProjectArchivedAndNames(t *testing.T) {
	f := newFixture(t)
	p := f.project("p")
	s := f.putNew(Input{ProjectID: p, Name: "tick", Kind: KindEvery, Every: "1m", Prompt: "p"})
	_, err := f.s.Put(f.ctx, "", Input{Mode: ModeNewThread, ProjectID: p, Name: "tick", Kind: KindEvery, Every: "1h", Prompt: "p"}, "user")
	requireCode(t, err, CodeNameTaken)
	noProj := f.putNew(Input{Name: "tick", Kind: KindEvery, Every: "1h", Prompt: "p"})
	_, err = f.s.Put(f.ctx, "", Input{Mode: ModeNewThread, Name: "tick", Kind: KindEvery, Every: "1h", Prompt: "p"}, "user")
	requireCode(t, err, CodeNameTaken) // 无项目之间同样唯一
	_, err = f.s.Put(f.ctx, noProj.ID, Input{Name: "tick", Kind: KindEvery, Every: "2h", Prompt: "p"}, "user")
	require.NoError(t, err, "改自己不算冲突")
	_, err = f.s.Put(f.ctx, s.ID, Input{ProjectID: f.project("q"), Name: "tick", Kind: KindEvery, Every: "1h", Prompt: "p"}, "user")
	requireCode(t, err, CodeInvalidSchedule)
	_, err = f.s.Put(f.ctx, "", Input{Mode: ModeNewThread, ProjectID: "prj_missing", Name: "z", Kind: KindEvery, Every: "1h", Prompt: "p"}, "user")
	requireCode(t, err, CodeInvalidSchedule)

	f.exec(`UPDATE projects SET archived_at = 1 WHERE id = ?`, p)
	f.advanceTick(time.Minute)
	assert.Empty(t, f.th.Creates())
	assert.Equal(t, "project_archived", *f.runs(s.ID)[0].Error)
	sc := f.sched(s.ID)
	assert.False(t, sc.Enabled)
	assert.Equal(t, "archived", *sc.PausedReason)
}

func TestNewThreadInterruptedOnRestart(t *testing.T) {
	f := newFixture(t)
	s := f.putNew(Input{Name: "n", Kind: KindEvery, Every: "1h", Prompt: "p"})
	id := ids.New("srn")
	require.NoError(t, f.st.Q.InsertScheduleRun(f.ctx, db.InsertScheduleRunParams{ID: id, ScheduleID: s.ID, Trigger: triggerSchedule,
		ScheduledFor: 1, StartedAt: ms(f.clock.Now()), Status: runPending}))
	f.newService().recover(f.ctx)
	r, err := f.st.Q.GetScheduleRun(f.ctx, id)
	require.NoError(t, err)
	assert.Equal(t, runFailed, r.Status)
	assert.Equal(t, "interrupted", *r.Error)
}

func TestListFilters(t *testing.T) {
	f := newFixture(t)
	p := f.project("p")
	spc := ids.New("spc")
	chat := f.thread()
	f.exec(`INSERT INTO spaces (id, name, thread_id, created_at, updated_at) VALUES (?, 'm9', ?, 1, 1)`, spc, chat)
	inSpace := f.thread()
	f.exec(`UPDATE threads SET space_id = ? WHERE id = ?`, spc, inSpace)

	tA := f.putThread(inSpace, Input{Kind: KindEvery, Every: "1h", Prompt: "p"})
	tB := f.putThread(f.thread(), Input{Kind: KindEvery, Every: "1h", Prompt: "p"})
	nP := f.putNew(Input{ProjectID: p, Name: "a", Kind: KindEvery, Every: "1h", Prompt: "p"})
	nS := f.putNew(Input{SpaceID: spc, Name: "b", Kind: KindEvery, Every: "1h", Prompt: "p"})

	list := func(fl Filter) []string {
		list, err := f.s.List(f.ctx, fl)
		require.NoError(t, err)
		var out []string
		for _, s := range list {
			out = append(out, s.ID)
		}
		return out
	}
	assert.ElementsMatch(t, []string{tA.ID, tB.ID, nP.ID, nS.ID}, list(Filter{}))
	assert.ElementsMatch(t, []string{tA.ID, tB.ID}, list(Filter{Mode: ModeThread}))
	assert.Equal(t, []string{nP.ID}, list(Filter{ProjectID: p}))
	assert.Equal(t, []string{nS.ID}, list(Filter{NoProject: true}))
	assert.ElementsMatch(t, []string{tA.ID, nS.ID}, list(Filter{SpaceID: spc}))
	assert.Equal(t, []string{tB.ID}, list(Filter{ThreadID: *tB.ThreadID}))
}

func TestRunLoopFiresOnTimer(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	f.putThread(th, Input{Kind: KindEvery, Every: "5m", Prompt: "p"})
	f.timers.armed = make(chan time.Duration, 16)
	f.th.delivered = make(chan string, 1)
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { f.s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// Run 开头的 Tick 上了 5 分钟的定时器
	require.Equal(t, 5*time.Minute, <-f.timers.armed)
	f.clock.Advance(5 * time.Minute)
	require.Equal(t, 1, f.timers.Fire(5*time.Minute))
	assert.Equal(t, th, <-f.th.delivered)
	assert.Equal(t, deliverTimeout, <-f.timers.armed, "投递后等应答的超时")
	assert.Equal(t, 5*time.Minute, <-f.timers.armed, "触发后按新的 next_run_at 重新上定时器")
}
