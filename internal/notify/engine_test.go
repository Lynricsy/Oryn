package notify

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

func uiFrame(id string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"type": "extension_ui_request", "id": id, "method": "select", "title": "选 A 还是 B？", "options": []string{"A", "B"}})
	return b
}

func TestTriggerMatrix(t *testing.T) {
	type step func(f *fixture, id string)
	state := func(prev, st string, focused bool) step {
		return func(f *fixture, id string) {
			f.s.StateChanged(threads.StateEvent{ThreadID: id, Prev: prev, State: st, Focused: focused, Reason: "orb crashed"})
		}
	}
	input := func(req string, focused bool) step {
		return func(f *fixture, id string) {
			f.s.InputRequested(threads.InputEvent{ThreadID: id, RequestID: req, Frame: uiFrame(req), Focused: focused})
		}
	}
	resolved := func(req string) step { return func(f *fixture, id string) { f.s.InputResolved(id, req) } }
	fire := func(f *fixture, _ string) { f.flush() }

	cases := []struct {
		name  string
		steps []step
		want  []string // 通知种类（按创建时间倒序）
	}{
		{"working→idle 无人在看", []step{state(threads.StateWorking, threads.StateIdle, false), fire}, []string{KindDone}},
		{"awaiting_input→idle", []step{state(threads.StateAwaitingInput, threads.StateIdle, false), fire}, []string{KindDone}},
		{"focus 中", []step{state(threads.StateWorking, threads.StateIdle, true), fire}, nil},
		{"去抖期内回到 working", []step{state(threads.StateWorking, threads.StateIdle, false), state(threads.StateIdle, threads.StateWorking, false), fire}, nil},
		{"provisioning→idle（fork）", []step{state(threads.StateProvisioning, threads.StateIdle, false), fire}, nil},
		{"→error", []step{state(threads.StateWorking, threads.StateError, false), fire}, []string{KindError}},
		{"error→error 不重复", []step{state(threads.StateError, threads.StateError, false), fire}, nil},
		{"进入 awaiting_input 本身不触发", []step{state(threads.StateWorking, threads.StateAwaitingInput, false), fire}, nil},
		{"同线程两个请求", []step{input("r1", false), input("r2", false), fire}, []string{KindInput, KindInput}},
		{"请求时有人在看", []step{input("r1", true), fire}, nil},
		{"作答先于定时器", []step{input("r1", false), resolved("r1"), fire}, nil},
		{"重复事件重置同一个定时器", []step{state(threads.StateWorking, threads.StateIdle, false), state(threads.StateAwaitingInput, threads.StateIdle, false), fire}, []string{KindDone}},
		{"出错后又完成只留最新", []step{state(threads.StateWorking, threads.StateError, false), state(threads.StateError, threads.StateIdle, false), fire}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			id := f.thread("修复登录")
			for _, s := range c.steps {
				s(f, id)
			}
			var got []string
			for _, n := range f.notifications() {
				got = append(got, n.Kind)
			}
			assert.Equal(t, c.want, got)
			assert.Len(t, f.out.Notes(), len(c.want), "每条通知广播一次")
		})
	}
}

// error→idle 不在「完成」规则里（Prev 不是 working / awaiting_input），且会取消 error 定时器。
func TestErrorBodyAndDoneBody(t *testing.T) {
	f := newFixture(t)
	id := f.thread("")
	f.s.StateChanged(threads.StateEvent{ThreadID: id, Prev: threads.StateWorking, State: threads.StateError, Reason: "orb 启动失败"})
	f.flush()
	ns := f.notifications()
	require.Len(t, ns, 1)
	assert.Equal(t, KindError, ns[0].Kind)
	assert.Equal(t, "orb 启动失败", ns[0].Body)
	assert.Equal(t, "", ns[0].Title, "标题跟线程走；为空时由各渠道按语言补「未命名线程」")

	// done 的正文 = 最近一条有文本的 assistant 消息的摘要
	g := newFixture(t)
	tid := g.thread("数数")
	for i, line := range []string{
		`{"type":"message","id":"a1","message":{"role":"user","content":"数到 3"}}`,
		`{"type":"message","id":"a2","message":{"role":"assistant","content":[{"type":"text","text":"# 结果\n1\n2\n3"}]}}`,
		`{"type":"message","id":"a3","message":{"role":"assistant","content":[{"type":"toolCall","id":"c","name":"bash","arguments":{}}]}}`,
	} {
		var e struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &e))
		_, err := g.st.Q.UpsertEntry(g.ctx, db.UpsertEntryParams{ThreadID: tid, File: "s.jsonl", EntryID: e.ID, Type: "message", Ts: new(int64(i)), Payload: []byte(line)})
		require.NoError(t, err)
	}
	g.done(tid)
	ns = g.notifications()
	require.Len(t, ns, 1)
	assert.Equal(t, "结果 1 2 3", ns[0].Body)
	assert.Equal(t, "数数", ns[0].Title)
	n := g.out.Notes()[0]
	assert.Equal(t, "/t/"+tid, n.URL)
	require.NotNil(t, n.ThreadID)
}

func TestInputNotificationBodyAndDedup(t *testing.T) {
	f := newFixture(t)
	id := f.thread("t")
	f.s.InputRequested(threads.InputEvent{ThreadID: id, RequestID: "r1", Frame: uiFrame("r1")})
	assert.Equal(t, []time.Duration{defaultInputDelay}, f.timers.Delays(), "input 去抖 2s")
	f.flush()
	ns := f.notifications()
	require.Len(t, ns, 1)
	assert.Equal(t, "选 A 还是 B？", ns[0].Body)
	require.NotNil(t, ns[0].RequestID)
	assert.Equal(t, "r1", *ns[0].RequestID)

	// 控制面重启后 orbd 重放同一个未决请求：不重复通知
	f.s = f.newService()
	f.s.InputRequested(threads.InputEvent{ThreadID: id, RequestID: "r1", Frame: uiFrame("r1")})
	f.flush()
	assert.Len(t, f.notifications(), 1)
	assert.Len(t, f.out.Notes(), 1)

	// 计划提案：request_id = plan，正文为空（渠道按语言补固定文案）
	f.s.InputRequested(threads.InputEvent{ThreadID: id, RequestID: threads.PlanRequestID, Frame: json.RawMessage(`{"type":"plan_proposal","title":"计划"}`), Plan: true})
	f.flush()
	ns = f.notifications()
	require.Len(t, ns, 2)
	assert.Equal(t, "plan", *ns[0].RequestID)
	assert.Equal(t, "", ns[0].Body)

	// 作答 → resolved_at
	f.s.InputResolved(id, "r1")
	f.s.processEvents(f.ctx)
	n, err := f.st.Q.GetNotification(f.ctx, ns[1].ID)
	require.NoError(t, err)
	assert.NotNil(t, n.ResolvedAt)
}

func TestSeenCancelsTimersAndMarksRead(t *testing.T) {
	f := newFixture(t)
	id := f.thread("t")
	other := f.thread("o")
	f.done(id)
	require.Len(t, f.notifications(), 1)

	// 有定时器在等：Seen 取消它
	f.s.StateChanged(threads.StateEvent{ThreadID: other, Prev: threads.StateWorking, State: threads.StateIdle})
	require.Equal(t, 1, f.timers.Active())
	f.s.Seen(other)
	assert.Equal(t, 0, f.timers.Active())
	f.flush()
	assert.Len(t, f.notifications(), 1, "被 Seen 取消的定时器不产生通知")
	assert.Empty(t, f.out.Reads(), "other 没有未读通知")

	// 有未读：Seen 标记已读并广播
	f.s.Seen(id)
	f.s.processEvents(f.ctx)
	ns := f.notifications()
	require.NotNil(t, ns[0].ReadAt)
	assert.Equal(t, []string{id}, f.out.Reads())

	// 热路径：没有未读、没有定时器 → 不入队、不碰库
	f.s.Seen(id)
	f.s.Seen(other)
	f.s.mu.Lock()
	assert.Empty(t, f.s.queue)
	f.s.mu.Unlock()
	assert.Len(t, f.out.Reads(), 1)

	// 定时器已触发、尚未处理时的 Seen 同样取消
	f.s.StateChanged(threads.StateEvent{ThreadID: id, Prev: threads.StateWorking, State: threads.StateIdle})
	f.timers.FireAll()
	f.s.Seen(id)
	f.s.processEvents(f.ctx)
	assert.Len(t, f.notifications(), 1)
}

func TestUnreadSetSurvivesRestartAndMarkRead(t *testing.T) {
	f := newFixture(t)
	a, b := f.thread("a"), f.thread("b")
	f.done(a)
	f.done(b)
	f.s = f.newService()
	f.s.mu.Lock()
	assert.Equal(t, map[string]bool{a: true, b: true}, f.s.unread, "启动时由库装载")
	f.s.mu.Unlock()

	require.NoError(t, f.s.MarkRead(f.ctx, a))
	list, err := f.s.List(f.ctx, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, list.Unread)
	require.NoError(t, f.s.MarkRead(f.ctx, ""))
	list, err = f.s.List(f.ctx, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, list.Unread)
	assert.Equal(t, []string{a, ""}, f.out.Reads())
	f.s.mu.Lock()
	assert.Empty(t, f.s.unread)
	f.s.mu.Unlock()
}

func TestDeletedThreadDropsNotification(t *testing.T) {
	f := newFixture(t)
	f.s.StateChanged(threads.StateEvent{ThreadID: "T-missing", Prev: threads.StateWorking, State: threads.StateIdle})
	f.flush()
	assert.Empty(t, f.notifications())
	f.s.mu.Lock()
	assert.Empty(t, f.s.unread)
	f.s.mu.Unlock()
}

func TestRetentionCleanup(t *testing.T) {
	f := newFixture(t)
	id := f.thread("t")
	f.done(id)
	f.clock.Advance(31 * 24 * time.Hour)
	f.s.cleanup(f.ctx)
	assert.Empty(t, f.notifications())
	f.s.mu.Lock()
	assert.Empty(t, f.s.unread)
	f.s.mu.Unlock()
}

// 回归（B9）：emit 已预登记未读、尚未落库时（此处在 Threads.Get 里暂停），REST MarkRead 的 SQL 标不到那条新通知；
// 它不得据此清掉内存未读集合，否则之后的 Seen 走快速路径，库里的未读永远读不掉。
func TestMarkReadInterleavedWithEmit(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "thread", true: "all"}[all], func(t *testing.T) {
			f := newFixture(t)
			id := f.thread("t")
			entered, release := make(chan struct{}), make(chan struct{})
			f.th.mu.Lock()
			f.th.getHook = func() {
				f.th.mu.Lock()
				f.th.getHook = nil
				f.th.mu.Unlock()
				close(entered)
				<-release
			}
			f.th.mu.Unlock()

			f.s.StateChanged(threads.StateEvent{ThreadID: id, Prev: threads.StateWorking, State: threads.StateIdle})
			f.timers.FireAll()
			processed := make(chan struct{})
			go func() {
				defer close(processed)
				f.s.processEvents(f.ctx)
			}()
			<-entered // 未读已预登记，通知尚未落库
			target := id
			if all {
				target = ""
			}
			require.NoError(t, f.s.MarkRead(f.ctx, target))
			close(release)
			<-processed

			ns := f.notifications()
			require.Len(t, ns, 1)
			require.Nil(t, ns[0].ReadAt, "通知在 MarkRead 的 SQL 之后落库，仍未读")
			f.s.mu.Lock()
			assert.True(t, f.s.unread[id], "库里有未读时集合必须登记")
			f.s.mu.Unlock()

			// Seen 能把它读掉：库与集合一致
			f.s.Seen(id)
			f.s.processEvents(f.ctx)
			unread, err := f.st.Q.CountUnreadNotifications(f.ctx)
			require.NoError(t, err)
			assert.EqualValues(t, 0, unread)
			f.s.mu.Lock()
			assert.False(t, f.s.unread[id])
			f.s.mu.Unlock()
		})
	}
}
