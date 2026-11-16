package threads

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// errStartFailed 是测试里直接 markFailed 用的失败原因。
var errStartFailed = errors.New("agent.start failed")

// listedThread 从 List 结果里取出线程 id。
func listedThread(t *testing.T, f *fixture, id string) apigen.Thread {
	t.Helper()
	all, err := f.s.List(f.ctx, ListFilter{})
	require.NoError(t, err)
	for _, th := range all {
		if th.ID == id {
			return th
		}
	}
	t.Fatalf("线程 %s 不在列表里", id)
	return apigen.Thread{}
}

// 回归（M11b X27）：runner 拒绝线程的 cwd（agent.start 回 ctl 错误 dir_not_served）→ 线程进 error，
// GET / 列表 / index.patch 的 Thread.error 与状态事件都带 {agent_failed, "dir_not_served: …"}；
// 重试再失败时原因随之更新；agent 之后成功启动则原因消失。
func TestAgentStartFailureReason(t *testing.T) {
	f := newFixture(t, Config{})
	o := newCtlOrbs(t, f.orbs)
	f.s.orbs = o
	sink := &indexSink{}
	f.s.SetSink(sink)
	obs := &recObserver{}
	f.s.AddObserver(obs)
	orb := f.orb("ready")
	th := f.thread(orb, StateProvisioning)
	th.mu.Lock()
	th.cwd = "/tmp/m11-a/esc"
	th.mu.Unlock()

	o.mu.Lock()
	o.startErr = &link.CtlError{Code: "dir_not_served", Message: `"/tmp/m11-a/esc" is not inside a directory served by this runner`}
	o.mu.Unlock()
	f.s.startAgent(f.ctx, th.id)
	want := &apigen.ThreadError{Kind: apigen.AgentFailed, Reason: `dir_not_served: "/tmp/m11-a/esc" is not inside a directory served by this runner`,
		Cause: new(apigen.DirNotServed), Detail: new("/tmp/m11-a/esc")}

	got, err := f.s.Get(f.ctx, th.id)
	require.NoError(t, err)
	assert.Equal(t, apigen.ThreadState(StateError), got.State)
	assert.Equal(t, want, got.Error, "GET 线程带 agent_failed 原因")
	assert.Equal(t, want, listedThread(t, f, th.id).Error, "列表同样带原因")
	ups := sink.snapshot()
	require.NotEmpty(t, ups)
	assert.Equal(t, want, ups[len(ups)-1].Error, "index.patch 带原因")
	states := obs.snapshot().states
	require.NotEmpty(t, states)
	last := states[len(states)-1]
	assert.Equal(t, StateError, last.State)
	assert.Equal(t, want.Reason, last.Reason, "状态事件带同样原因")
	assert.False(t, last.PromptError)

	// 重试再次失败、原因不同：状态不变（不再有状态事件），但 index.patch 推送新原因
	o.mu.Lock()
	o.startErr = &link.CtlError{Code: "dir_not_served", Message: "still not served"}
	o.mu.Unlock()
	nStates := len(obs.snapshot().states)
	f.s.startAgent(f.ctx, th.id)
	ups = sink.snapshot()
	assert.Equal(t, &apigen.ThreadError{Kind: apigen.AgentFailed, Reason: "dir_not_served: still not served", Cause: new(apigen.DirNotServed)}, ups[len(ups)-1].Error)
	assert.Len(t, obs.snapshot().states, nStates)

	// 重试成功：agent 就绪后线程离开 error，原因消失
	o.mu.Lock()
	o.startErr = nil
	o.mu.Unlock()
	f.s.startAgent(f.ctx, th.id)
	th.mu.Lock()
	th.agent = fakeAgent(16)
	th.mu.Unlock()
	raw, _ := json.Marshal(apigen.LinkCtlAgentStatusParams{ThreadID: th.id, Status: "ready"})
	f.s.onAgentStatus(f.ctx, orb, raw)
	got, err = f.s.Get(f.ctx, th.id)
	require.NoError(t, err)
	assert.NotEqual(t, apigen.ThreadState(StateError), got.State)
	assert.Nil(t, got.Error)
	ups = sink.snapshot()
	assert.Nil(t, ups[len(ups)-1].Error)
	_, failed := f.s.agentFailReason(th.id)
	assert.False(t, failed, "成功启动后清掉失败原因")
}

// orb 本身 failed 时仍报 orb_failed（优先于 agent_failed）。
func TestOrbFailureWinsOverAgentFailure(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateProvisioning)
	f.s.markFailed(f.ctx, th, errStartFailed)
	f.setOrbState(orb, "failed")
	f.s.applyOrbState(orb, "failed", 10)
	got, err := f.s.Get(f.ctx, th.id)
	require.NoError(t, err)
	require.NotNil(t, got.Error)
	assert.Equal(t, apigen.OrbFailed, got.Error.Kind)
}

func TestFailReason(t *testing.T) {
	ce := &link.CtlError{Code: "dir_not_served", Message: "nope"}
	assert.Equal(t, "dir_not_served: nope", failReason(ce))
	assert.Equal(t, "dir_not_served: nope", failReason(fmt.Errorf("wrapped: %w", ce)), "包装过的 ctl 错误取 code: message")
	assert.Equal(t, "agent.start failed", failReason(errStartFailed))
}

// countCtlOrbs 记录 Ctl 查找次数：startAgent 在发 agent.start 前必先查 Ctl（与 ctx 是否已取消无关）。
type countCtlOrbs struct {
	*ctlOrbs
	lookups atomic.Int32
}

func (o *countCtlOrbs) Ctl(id string) (*link.Ctl, bool) {
	o.lookups.Add(1)
	return o.ctlOrbs.Ctl(id)
}

// failedFixture 建一个开了 WakeOnFocus 的服务和一条失败的线程：orbFailed 时 orb 为 failed，否则 orb ready、
// agent.start 回 dir_not_served 而线程 failed。
func failedFixture(t *testing.T, orbFailed bool) (*fixture, *countCtlOrbs, *thread) {
	t.Helper()
	f := newFixture(t, Config{WakeOnFocus: true})
	o := &countCtlOrbs{ctlOrbs: newCtlOrbs(t, f.orbs)}
	f.s.orbs = o
	if orbFailed {
		return f, o, f.thread(f.orb("failed"), StateError)
	}
	th := f.thread(f.orb("ready"), StateProvisioning)
	f.s.markFailed(f.ctx, th, &link.CtlError{Code: "dir_not_served", Message: "not served"})
	return f, o, th
}

// 回归（M11b X27 / X37）：打开线程页（focus）不自动重试失败——orb failed 时不唤醒（不入队 spawn），
// agent 没能启动时不重发 agent.start；失败只由显式重试恢复。
func TestFocusDoesNotRetryFailures(t *testing.T) {
	for name, orbFailed := range map[string]bool{"orb failed": true, "agent failed": false} {
		t.Run(name, func(t *testing.T) {
			f, o, th := failedFixture(t, orbFailed)
			f.s.Focus(f.ctx, "conn-1", th.id)
			f.s.Close() // 等 focus 可能发起的后台唤醒全部执行完
			assert.Empty(t, f.orbs.wakes, "focus 不唤醒失败的 orb")
			assert.Zero(t, o.lookups.Load(), "focus 不重新启动 agent")
		})
	}
}

// 显式重试（POST threads/{id}/wake，即失败横幅的「重试」）仍然恢复：orb failed → 唤醒 orb；
// agent 没能启动 → 重新 agent.start，成功就绪后 Thread.error 消失。
func TestExplicitWakeRetriesFailures(t *testing.T) {
	t.Run("orb failed", func(t *testing.T) {
		f, _, th := failedFixture(t, true)
		require.NoError(t, f.s.Wake(f.ctx, th.id))
		f.s.Close()
		require.Len(t, f.orbs.wakes, 1)
		assert.Equal(t, th.orbID, <-f.orbs.wakes)
	})
	t.Run("agent failed", func(t *testing.T) {
		f, o, th := failedFixture(t, false)
		got, err := f.s.Get(f.ctx, th.id)
		require.NoError(t, err)
		require.NotNil(t, got.Error)
		assert.Equal(t, apigen.AgentFailed, got.Error.Kind)

		require.NoError(t, f.s.Wake(f.ctx, th.id))
		require.Equal(t, "agent.start", o.nextCall(t).method, "显式重试重新 agent.start")
		th.mu.Lock()
		th.agent = fakeAgent(16)
		th.mu.Unlock()
		raw, _ := json.Marshal(apigen.LinkCtlAgentStatusParams{ThreadID: th.id, Status: "ready"})
		f.s.onAgentStatus(f.ctx, th.orbID, raw)
		got, err = f.s.Get(f.ctx, th.id)
		require.NoError(t, err)
		assert.NotEqual(t, apigen.ThreadState(StateError), got.State)
		assert.Nil(t, got.Error)
	})
}
