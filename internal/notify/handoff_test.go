package notify

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// handedThread 建一个由开着“交给助手”的 new_thread 定时建出的运行线程。
func (f *fixture) handedThread(title string) string {
	f.t.Helper()
	id := f.thread(title)
	sid := ids.New("sch")
	require.NoError(f.t, f.st.Q.InsertSchedule(f.ctx, db.InsertScheduleParams{
		ID: sid, Mode: "new_thread", Name: "digest", Kind: "every", Spec: "1h", Timezone: "UTC", Prompt: "go",
		Enabled: true, CreatedBy: "user", CreatedAt: 1, UpdatedAt: 1, Overlap: "skip", KeepLast: 5, HandToAssistant: 1,
	}))
	f.exec(`UPDATE threads SET schedule_id = ? WHERE id = ?`, sid, id)
	return id
}

// M19 步骤 3.3：交给助手的运行完成后不发完成通知（只看自动化回合）；出错照发；主人亲自参与的回合照常通知。
func TestHandToAssistantSuppressesDone(t *testing.T) {
	f := newFixture(t)
	handed := f.handedThread("运行")
	plain := f.thread("普通")

	auto := func(id string) {
		f.s.StateChanged(threads.StateEvent{ThreadID: id, Prev: threads.StateWorking, State: threads.StateIdle, Auto: true})
		f.flush()
	}
	auto(handed)
	auto(plain)
	ns := f.notifications()
	require.Len(t, ns, 1)
	assert.Equal(t, plain, *ns[0].ThreadID, "交给助手的运行不单独推送完成")
	assert.False(t, f.s.unread[handed], "未发通知的线程不算未读通知")

	// 出错照常通知
	f.s.StateChanged(threads.StateEvent{ThreadID: handed, Prev: threads.StateWorking, State: threads.StateError, Reason: "boom", Auto: true})
	f.flush()
	ns = f.notifications()
	require.Len(t, ns, 2)
	assert.Equal(t, KindError, ns[0].Kind)
	assert.Equal(t, handed, *ns[0].ThreadID)

	// 主人亲自参与的回合（Auto 为假）照常通知完成
	f.clock.Advance(1000)
	f.done(handed)
	ns = f.notifications()
	require.Len(t, ns, 3)
	assert.Equal(t, KindDone, ns[0].Kind)
}

// 助手在主线的自动化回合里沉默（Quiet）：不排完成定时器，也不发通知。
func TestQuietSkipsDoneNotification(t *testing.T) {
	f := newFixture(t)
	main := f.thread("Main")
	f.exec(`UPDATE threads SET kind = 'assistant', main = 1 WHERE id = ?`, main)
	f.s.StateChanged(threads.StateEvent{ThreadID: main, Prev: threads.StateWorking, State: threads.StateIdle, Quiet: true, Auto: true})
	assert.Zero(t, f.timers.Active())
	f.flush()
	assert.Empty(t, f.notifications())

	f.done(main)
	assert.Len(t, f.notifications(), 1, "没有沉默的回合照常通知")
}
