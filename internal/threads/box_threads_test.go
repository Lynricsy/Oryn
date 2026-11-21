package threads

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// boxFixture 是带 ctl 替身的夹具，并数 BoxThreadsChanged 的调用。
func boxFixture(t *testing.T) (*fixture, *ctlOrbs, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	f := newFixture(t, Config{BoxThreadsChanged: func() { n.Add(1) }})
	o := newCtlOrbs(t, f.orbs)
	f.s.orbs = o
	return f, o, &n
}

// nextStart 等下一个 agent.start（跳过其它 ctl 请求）并返回它的参数。
func nextStart(t *testing.T, o *ctlOrbs) apigen.LinkCtlAgentStartParams {
	t.Helper()
	for {
		c := o.nextCall(t)
		if c.method != "agent.start" {
			continue
		}
		var p apigen.LinkCtlAgentStartParams
		require.NoError(t, json.Unmarshal(c.params, &p))
		return p
	}
}

// M23 阶段 5：box orb + Cwd 时线程行写入 cwd、agent.start 带上它；建、删各触发一次 BoxThreadsChanged；
// 删掉 box 上最后一个线程后 orb 仍在。
func TestCreateOnBoxWithCwd(t *testing.T) {
	f, o, n := boxFixture(t)
	orb := f.boxOrb("ready", false)
	const cwd = "~/apps/hello/current"

	th, err := f.s.Create(f.ctx, CreateParams{Prompt: "x", OrbID: orb, Cwd: cwd})
	require.NoError(t, err)
	row, err := f.st.Q.GetThread(f.ctx, th.ID)
	require.NoError(t, err)
	require.NotNil(t, row.Cwd)
	assert.Equal(t, cwd, *row.Cwd)
	assert.Equal(t, cwd, nextStart(t, o).Cwd, "agent.start 带上线程的 cwd")
	assert.Equal(t, int32(1), n.Load(), "建线程通知一次")

	require.NoError(t, f.s.Delete(f.ctx, th.ID))
	assert.Equal(t, int32(2), n.Load(), "删线程通知一次")
	f.orbs.mu.Lock()
	assert.NotContains(t, f.orbs.deletes, orb, "box 上最后一个线程删掉后 orb 仍在")
	f.orbs.mu.Unlock()
}

// 普通 orb 上建、删线程不通知。
func TestCreateOnPlainOrbNoBoxNotify(t *testing.T) {
	f, o, n := boxFixture(t)
	th, err := f.s.Create(f.ctx, CreateParams{Prompt: "x", OrbID: f.orb("ready")})
	require.NoError(t, err)
	assert.Empty(t, nextStart(t, o).Cwd)
	require.NoError(t, f.s.Delete(f.ctx, th.ID))
	assert.Equal(t, int32(0), n.Load())
}

// box orb 必须经 executor.kind = box（带 Cwd）建线程；Cwd 只对 box 有效。
func TestCreateBoxCwdValidation(t *testing.T) {
	f, _, n := boxFixture(t)
	box := f.boxOrb("ready", false)
	plain := f.orb("ready")
	for _, tc := range []struct {
		name string
		p    CreateParams
		code string
	}{
		{name: "box orb without cwd", p: CreateParams{Prompt: "x", OrbID: box}, code: CodeOrbIsBox},
		{name: "plain orb with cwd", p: CreateParams{Prompt: "x", OrbID: plain, Cwd: "~"}, code: "invalid"},
		{name: "cwd without orb", p: CreateParams{Prompt: "x", Cwd: "~"}, code: "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.s.Create(f.ctx, tc.p)
			requireCode(t, err, tc.code)
		})
	}
	assert.Zero(t, f.threadCount(), "被拒的请求不留线程")
	assert.Equal(t, int32(0), n.Load())
}

// box 上的线程共用 fork：子线程沿用源线程的 cwd，并通知一次 BoxThreadsChanged。
func TestForkShareOnBoxNotifies(t *testing.T) {
	f, _, n := boxFixture(t)
	orb := f.boxOrb("ready", false)
	const cwd = "~/apps/hello/current"
	id := ids.Thread()
	require.NoError(t, f.st.Q.CreateThread(f.ctx, db.CreateThreadParams{Kind: "work", ID: id, OrbID: &orb, TitleSource: "auto",
		State: StateIdle, SessionDir: sessionDirOf(id), Cwd: new(cwd), CreatedAt: 1, UpdatedAt: 1}))
	src, err := f.s.get(f.ctx, id)
	require.NoError(t, err)
	leaf := "e1"
	src.mu.Lock()
	src.leaf = &leaf
	src.mu.Unlock()
	a := fakeAgent(8)
	attach(src, a, apigen.AgentStatusReady)

	type result struct {
		th  apigen.Thread
		err error
	}
	done := make(chan result, 1)
	go func() {
		th, err := f.s.Fork(f.ctx, id, nil, "share")
		done <- result{th, err}
	}()
	frame := recvFrame(t, a)
	require.Contains(t, frame, `"fork_from"`)
	f.respond(src, frame, true, `{"sessionPath":"`+sessionDirOf(id)+`/fork.jsonl"}`)
	r := <-done
	require.NoError(t, r.err)
	row, err := f.st.Q.GetThread(f.ctx, r.th.ID)
	require.NoError(t, err)
	require.NotNil(t, row.Cwd)
	assert.Equal(t, cwd, *row.Cwd, "共用 fork 的子线程沿用源线程的 cwd")
	assert.Equal(t, orb, *row.OrbID)
	assert.Equal(t, int32(1), n.Load(), "共用 fork 通知一次")
}
