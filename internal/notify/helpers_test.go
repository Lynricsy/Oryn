package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/secrets"
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
	d       time.Duration
	f       func()
	stopped bool
	fired   bool
}

type fakeTimers struct {
	mu sync.Mutex
	ts []*fakeTimer
}

func (ft *fakeTimers) AfterFunc(d time.Duration, f func()) func() {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	t := &fakeTimer{d: d, f: f}
	ft.ts = append(ft.ts, t)
	return func() {
		ft.mu.Lock()
		t.stopped = true
		ft.mu.Unlock()
	}
}

// Active 返回未停止、未触发的定时器数。
func (ft *fakeTimers) Active() int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	n := 0
	for _, t := range ft.ts {
		if !t.stopped && !t.fired {
			n++
		}
	}
	return n
}

// Delays 返回全部未停止、未触发定时器的时长。
func (ft *fakeTimers) Delays() []time.Duration {
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

// FireAll 触发全部未停止的定时器。
func (ft *fakeTimers) FireAll() {
	ft.mu.Lock()
	var due []*fakeTimer
	for _, t := range ft.ts {
		if !t.stopped && !t.fired {
			t.fired = true
			due = append(due, t)
		}
	}
	ft.mu.Unlock()
	for _, t := range due {
		t.f()
	}
}

// ---- 假线程服务与广播 ----

type uiCall struct {
	ThreadID string
	Frame    json.RawMessage
}

type fakeThreads struct {
	st *store.Store

	mu       sync.Mutex
	ui       []uiCall
	cmds     []uiCall
	uiErr    error
	cmdReply json.RawMessage
}

func (f *fakeThreads) Get(ctx context.Context, id string) (apigen.Thread, error) {
	row, err := f.st.Q.GetThread(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.Thread{}, threads.ErrNotFound
	}
	if err != nil {
		return apigen.Thread{}, err
	}
	return threads.ThreadToAPI(row, nil), nil
}

func (f *fakeThreads) Command(_ context.Context, threadID string, frame json.RawMessage, reply func(json.RawMessage)) error {
	f.mu.Lock()
	f.cmds = append(f.cmds, uiCall{threadID, append(json.RawMessage(nil), frame...)})
	resp := f.cmdReply
	f.mu.Unlock()
	if resp == nil {
		resp = json.RawMessage(`{"type":"response","success":true}`)
	}
	reply(resp)
	return nil
}

func (f *fakeThreads) UIResponse(_ context.Context, threadID string, frame json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uiErr != nil {
		return f.uiErr
	}
	f.ui = append(f.ui, uiCall{threadID, append(json.RawMessage(nil), frame...)})
	return nil
}

func (f *fakeThreads) UICalls() []uiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uiCall(nil), f.ui...)
}

func (f *fakeThreads) Commands() []uiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uiCall(nil), f.cmds...)
}

type recBroadcaster struct {
	mu    sync.Mutex
	notes []apigen.Notification
	reads []string
}

func (r *recBroadcaster) Notification(n apigen.Notification) {
	r.mu.Lock()
	r.notes = append(r.notes, n)
	r.mu.Unlock()
}

func (r *recBroadcaster) NotificationsRead(threadID string) {
	r.mu.Lock()
	r.reads = append(r.reads, threadID)
	r.mu.Unlock()
}

func (r *recBroadcaster) Notes() []apigen.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]apigen.Notification(nil), r.notes...)
}

func (r *recBroadcaster) Reads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reads...)
}

// ---- 夹具 ----

type fixture struct {
	t      *testing.T
	ctx    context.Context
	dir    string
	st     *store.Store
	box    *secrets.Box
	clock  *fakeClock
	timers *fakeTimers
	th     *fakeThreads
	out    *recBroadcaster
	s      *Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(make([]byte, 32))
	require.NoError(t, err)
	f := &fixture{t: t, ctx: ctx, dir: dir, st: st, box: box,
		clock: &fakeClock{now: time.UnixMilli(1_800_000_000_000)}, timers: &fakeTimers{}, th: &fakeThreads{st: st}, out: &recBroadcaster{}}
	f.s = f.newService()
	return f
}

// newService 在同一个库上新建一个服务实例（模拟控制面重启）。
func (f *fixture) newService() *Service {
	f.t.Helper()
	s, err := New(Config{PublicURL: "https://oryn.test", DataDir: f.dir, Box: f.box, Now: f.clock.Now, AfterFunc: f.timers.AfterFunc}, f.st, f.th, f.out)
	require.NoError(f.t, err)
	return s
}

func (f *fixture) thread(title string) string {
	f.t.Helper()
	id := ids.Thread()
	require.NoError(f.t, f.st.Q.CreateThread(f.ctx, db.CreateThreadParams{ID: id, Title: title, TitleSource: "auto", State: threads.StateIdle,
		SessionDir: "~/.oryn/threads/" + id, CreatedAt: 1, UpdatedAt: 1}))
	return id
}

// done 模拟一次 working→idle（无人在看），触发定时器并处理事件。
func (f *fixture) done(threadID string) {
	f.s.StateChanged(threads.StateEvent{ThreadID: threadID, Prev: threads.StateWorking, State: threads.StateIdle})
	f.flush()
}

func (f *fixture) flush() {
	f.timers.FireAll()
	f.s.processEvents(f.ctx)
}

func (f *fixture) notifications() []db.Notification {
	f.t.Helper()
	ns, err := f.st.Q.ListNotifications(f.ctx, db.ListNotificationsParams{Before: 1 << 62, Limit: 100})
	require.NoError(f.t, err)
	return ns
}

func (f *fixture) deliveries(notificationID string) []db.NotificationDelivery {
	f.t.Helper()
	ds, err := f.st.Q.ListDeliveriesByNotifications(f.ctx, []string{notificationID})
	require.NoError(f.t, err)
	return ds
}

func (f *fixture) delivery(id string) db.NotificationDelivery {
	f.t.Helper()
	d, err := f.st.Q.GetNotificationDelivery(f.ctx, id)
	require.NoError(f.t, err)
	return d
}
