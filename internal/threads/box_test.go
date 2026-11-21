package threads

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// boxOrb 建一台 box 的 orb（状态 state），并让替身的闸门对它报告 stopped。
func (f *fixture) boxOrb(state string, stopped bool) string {
	f.t.Helper()
	id := f.orb(state)
	_, err := f.st.DB.ExecContext(f.ctx, `UPDATE orbs SET box = 1 WHERE id = ?`, id)
	require.NoError(f.t, err)
	f.orbs.mu.Lock()
	if f.orbs.boxStopped == nil {
		f.orbs.boxStopped = map[string]bool{}
	}
	f.orbs.boxStopped[id] = stopped
	f.orbs.mu.Unlock()
	return id
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var te *Error
	require.True(t, errors.As(err, &te), "want *threads.Error, got %v", err)
	assert.Equal(t, code, te.Code)
}

func noWake(t *testing.T, f *fixture) {
	t.Helper()
	select {
	case id := <-f.orbs.wakes:
		t.Fatalf("不该唤醒 %s", id)
	case <-time.After(200 * time.Millisecond):
	}
}

// M23 1.1：停着的 box 在受理消息时同步拒绝（box_stopped），消息不进队列、不唤醒；带附件的入口同样。
func TestCommandRejectedOnStoppedBox(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.boxOrb("stopped", true)
	th := f.thread(orb, StateIdle)

	err := f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply)
	requireCode(t, err, CodeBoxStopped)
	th.mu.Lock()
	assert.Empty(t, th.queue, "消息不进队列")
	assert.Empty(t, th.pending)
	th.mu.Unlock()
	noWake(t, f)

	f.s.SetAttachments(&stubAttachments{deliver: func(context.Context, []string) ([]DeliveredAttachment, error) {
		t.Error("停着的 box 不该投递附件")
		return nil, nil
	}})
	err = f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi","orynAttachments":["att_A"]}`), noReply)
	requireCode(t, err, CodeBoxStopped)
	th.mu.Lock()
	assert.Empty(t, th.msgOrder, "附件消息不占位")
	th.mu.Unlock()
	noWake(t, f)
}

// orb 是 ready 时不查闸门，照常受理；闸门放行的 box（没停）停机时照常唤醒。
func TestCommandAcceptedOnRunningBox(t *testing.T) {
	f := newFixture(t, Config{})
	ready := f.boxOrb("ready", true)
	th := f.thread(ready, StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply))
	assert.Contains(t, recvFrame(t, a), `"message":"hi"`)

	waking := f.boxOrb("stopped", false)
	th2 := f.thread(waking, StateIdle)
	require.NoError(t, f.s.Command(f.ctx, th2.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply))
	select {
	case id := <-f.orbs.wakes:
		assert.Equal(t, waking, id)
	case <-time.After(5 * time.Second):
		t.Fatal("没停的 box 应当照常唤醒")
	}
}

// POST threads/{id}/wake：orbs 报 ErrBoxStopped → box_stopped。
func TestWakeStoppedBox(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.boxOrb("stopped", true)
	th := f.thread(orb, StateIdle)
	f.orbs.mu.Lock()
	f.orbs.wakeErr = orbs.ErrBoxStopped
	f.orbs.mu.Unlock()
	requireCode(t, f.s.Wake(f.ctx, th.id), CodeBoxStopped)
}

// box 上的线程只能共用 fork：env=copy → box_unsupported。
func TestForkCopyRejectedOnBox(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.boxOrb("ready", false)
	th := f.thread(orb, StateIdle)
	leaf := "e1"
	th.mu.Lock()
	th.leaf = &leaf
	th.mu.Unlock()
	_, err := f.s.Fork(f.ctx, th.id, nil, "copy")
	requireCode(t, err, CodeBoxUnsupported)
}
