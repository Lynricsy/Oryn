package coord

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/uiprompt"
)

// confirmEnv 在 env 上登记一个测试动作 probe（执行时计数，返回 execErr），并收集确认结果消息。
type confirmEnv struct {
	*env
	execs   atomic.Int32
	execErr error
	results chan string
}

// newConfirmEnv 用真实拉起的 fakeomp：协调 orb 就绪后对话的状态才按待答请求推导（provisioning 优先）。
func newConfirmEnv(t *testing.T) *confirmEnv {
	ce := &confirmEnv{env: newEnv(t, false), results: make(chan string, 16)}
	// 先等 Create 发起的 fake orbd 启动完成。否则打开对话可能在启动途中的 orb 上另发 Wake；
	// 测试替身会启动第二个 orbd，失去对第一个的引用，清理时就收不到它的 agent 退出。
	require.Eventually(t, func() bool {
		orb, err := ce.st.Q.GetOrb(ce.ctx, ce.coordOrb)
		return err == nil && orb.State == orbs.StateReady
	}, 5*time.Second, 10*time.Millisecond, "协调 orb 就绪")
	ce.register(ce.c)
	ce.c.sentConfirm = func(_, text string) { ce.results <- text }
	return ce
}

func (ce *confirmEnv) register(c *Service) {
	c.onConfirm("probe", func(_ context.Context, row db.AssistantConfirmation, args json.RawMessage) (string, error) {
		ce.execs.Add(1)
		assert.JSONEq(ce.t, `{"n":1}`, string(args))
		return "", ce.execErr
	})
}

// request 以对话 conv 的身份挂一张 probe 确认卡，返回确认 id。
func (ce *confirmEnv) request(c *Service, conv apigen.Thread) (string, error) {
	v, err := c.requestConfirm(ce.ctx, toolCtx{HostToolCall: threads.HostToolCall{ThreadID: conv.ID, Kind: threads.KindAssistant}},
		confirmReq{Action: "probe", Target: "thr_target", TargetLabel: "Target", Title: `Probe "Target"?`, Summary: "probe it", AuditThread: "thr_target", Args: map[string]int{"n": 1}})
	if err != nil {
		return "", err
	}
	m := v.(map[string]any)
	assert.Equal(ce.t, "awaiting_owner", m["status"])
	return m["confirmationId"].(string), nil
}

// pending 返回线程服务 th 上对话 id 当前待答的请求帧。
func pending(t *testing.T, th *threads.Service, id string) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	require.NoError(t, th.Subscribe(context.Background(), id, func(s threads.Snapshot) { out = s.Agent.PendingUI }))
	return out
}

func (ce *confirmEnv) state(id string) string {
	ce.t.Helper()
	row, err := ce.st.Q.GetAssistantConfirmation(ce.ctx, id)
	require.NoError(ce.t, err)
	return row.State
}

func (ce *confirmEnv) result() string {
	ce.t.Helper()
	select {
	case s := <-ce.results:
		return s
	case <-time.After(5 * time.Second):
		ce.t.Fatal("没有收到确认结果消息")
		return ""
	}
}

func answer(id string, yes bool) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"type": "extension_ui_response", "id": id, "confirmed": yes})
	return b
}

// 确认卡：对话变 awaiting_input；通知的一触作答（confirm 的第一个选项）批准后执行函数恰好执行一次，
// 审计 origin=owner，结果以自动消息回到对话；重复作答回 ui_resolved。
func TestConfirmApproveViaQuickAnswer(t *testing.T) {
	ce := newConfirmEnv(t)
	asst := ce.assistant()
	id, err := ce.request(ce.c, asst)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(id, "cfm_"))
	assert.Equal(t, confirmPending, ce.state(id))
	assert.Empty(t, ce.actions(), "作答之前不写审计")

	require.Eventually(t, func() bool {
		got, err := ce.th.Get(ce.ctx, asst.ID)
		return err == nil && string(got.State) == threads.StateAwaitingInput
	}, 10*time.Second, 20*time.Millisecond, "协调 orb 就绪后对话处于 awaiting_input")
	frames := pending(t, ce.th, asst.ID)
	require.Len(t, frames, 1)
	var f map[string]any
	require.NoError(t, json.Unmarshal(frames[0], &f))
	assert.Equal(t, "confirm", f["method"])
	assert.Equal(t, id, f["id"])
	assert.Equal(t, `Probe "Target"?`, f["title"])
	assert.Equal(t, "probe it", f["message"])
	assert.Equal(t, map[string]any{"action": "probe", "targetLabel": "Target"}, f["oryn"])

	// notify.Answer 的路径：可一触作答，第一个选项是「是」
	c, ok := uiprompt.SingleChoice(frames[0], uiprompt.Labels{})
	require.True(t, ok)
	assert.True(t, c.Confirm)
	resp, err := uiprompt.AnswerOption(frames[0], 0)
	require.NoError(t, err)
	require.NoError(t, ce.th.UIResponse(ce.ctx, asst.ID, resp))
	assert.Equal(t, "[Oryn confirmation · automatic] probe thr_target: approved and done.", ce.result())
	assert.EqualValues(t, 1, ce.execs.Load())
	assert.Equal(t, confirmApproved, ce.state(id))
	acts := ce.actions()
	require.Len(t, acts, 1)
	assert.Equal(t, "probe", acts[0].Action)
	assert.Equal(t, "owner", acts[0].Origin)
	assert.Equal(t, threads.KindAssistant, acts[0].ActorKind)
	assert.Equal(t, asst.ID, deref(acts[0].ActorThreadID))
	assert.Equal(t, "thr_target", deref(acts[0].TargetThreadID))
	assert.True(t, acts[0].Ok)

	assert.ErrorIs(t, ce.th.UIResponse(ce.ctx, asst.ID, answer(id, true)), threads.ErrUIResolved)
	assert.EqualValues(t, 1, ce.execs.Load(), "重复作答不再执行")
	got, err := ce.th.Get(ce.ctx, asst.ID)
	require.NoError(t, err)
	assert.NotEqual(t, threads.StateAwaitingInput, string(got.State))
}

// 拒绝与过期都不执行、不写审计；过期撤回卡片；过期后作答回 ui_resolved。执行失败记 failed，审计 ok=false。
func TestConfirmRejectExpireAndFail(t *testing.T) {
	ce := newConfirmEnv(t)
	asst := ce.assistant()

	rej, err := ce.request(ce.c, asst)
	require.NoError(t, err)
	require.NoError(t, ce.th.UIResponse(ce.ctx, asst.ID, answer(rej, false)))
	assert.Equal(t, "[Oryn confirmation · automatic] probe thr_target: rejected by the owner.", ce.result())
	assert.Equal(t, confirmRejected, ce.state(rej))

	exp, err := ce.request(ce.c, asst)
	require.NoError(t, err)
	ce.c.expireConfirmations(ce.ctx)
	assert.Equal(t, confirmPending, ce.state(exp), "未满 24 小时不过期")
	ce.clk.Add(confirmTTL + time.Minute)
	ce.c.expireConfirmations(ce.ctx)
	assert.Equal(t, "[Oryn confirmation · automatic] probe thr_target: expired without an answer.", ce.result())
	assert.Equal(t, confirmExpired, ce.state(exp))
	assert.Empty(t, pending(t, ce.th, asst.ID), "过期撤回卡片")
	assert.ErrorIs(t, ce.th.UIResponse(ce.ctx, asst.ID, answer(exp, true)), threads.ErrUIResolved)
	assert.Zero(t, ce.execs.Load())
	assert.Empty(t, ce.actions())

	ce.execErr = errf(CodeNotFound, "gone")
	fail, err := ce.request(ce.c, asst)
	require.NoError(t, err)
	require.NoError(t, ce.th.UIResponse(ce.ctx, asst.ID, answer(fail, true)))
	assert.Equal(t, "[Oryn confirmation · automatic] probe thr_target: approved but failed: not_found.\n\ngone", ce.result())
	assert.Equal(t, confirmFailed, ce.state(fail))
	acts := ce.actions()
	require.Len(t, acts, 1)
	assert.Equal(t, "owner", acts[0].Origin)
	assert.False(t, acts[0].Ok)
	assert.Equal(t, CodeNotFound, deref(acts[0].Error))
}

// 每个对话同时至多 5 张待答；第 6 张回 rate_limited，别的对话不受影响。
func TestConfirmPendingCap(t *testing.T) {
	ce := newConfirmEnv(t)
	asst := ce.assistant()
	for range maxPendingConfirms {
		_, err := ce.request(ce.c, asst)
		require.NoError(t, err)
	}
	_, err := ce.request(ce.c, asst)
	assert.Equal(t, CodeRateLimited, errCode(err))
	assert.Len(t, pending(t, ce.th, asst.ID), maxPendingConfirms)
	_, err = ce.request(ce.c, ce.assistant())
	assert.NoError(t, err)
}

// 控制面重启（重建线程服务与协调服务）后，Run 把仍待答的确认卡放回对话，作答照常执行。
func TestConfirmRestoredAfterRestart(t *testing.T) {
	ce := newConfirmEnv(t)
	asst := ce.assistant()
	id, err := ce.request(ce.c, asst)
	require.NoError(t, err)

	th2 := threads.New(threads.Config{DataDir: filepath.Join(t.TempDir(), "data")}, ce.st, ce.orbs)
	t.Cleanup(th2.Close)
	c2 := New(Config{Now: ce.clk.Now}, ce.st, th2, nil, hostStub{}, ce.sched)
	ce.register(c2)
	c2.sentConfirm = func(_, text string) { ce.results <- text }
	th2.SetCoordinator(c2)
	assert.Empty(t, pending(t, th2, asst.ID), "线程服务的待答请求只在内存里")

	ctx, cancel := context.WithCancel(ce.ctx)
	done := make(chan struct{})
	go func() { c2.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { return len(pending(t, th2, asst.ID)) == 1 }, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, th2.UIResponse(ce.ctx, asst.ID, answer(id, true)))
	assert.Equal(t, "[Oryn confirmation · automatic] probe thr_target: approved and done.", ce.result())
	assert.EqualValues(t, 1, ce.execs.Load())
}

// 重启后动作已没有执行函数的待答卡（升级前已删除工具的确认）不再挂出：置 expired 并告知对话。
func TestConfirmRestoreExpiresUnknownAction(t *testing.T) {
	ce := newConfirmEnv(t)
	asst := ce.assistant()
	id, err := ce.request(ce.c, asst)
	require.NoError(t, err)

	th2 := threads.New(threads.Config{DataDir: filepath.Join(t.TempDir(), "data")}, ce.st, ce.orbs)
	t.Cleanup(th2.Close)
	c2 := New(Config{Now: ce.clk.Now}, ce.st, th2, nil, hostStub{}, ce.sched) // 不登记 probe
	c2.sentConfirm = func(_, text string) { ce.results <- text }
	th2.SetCoordinator(c2)

	ctx, cancel := context.WithCancel(ce.ctx)
	done := make(chan struct{})
	go func() { c2.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	assert.Equal(t, "[Oryn confirmation · automatic] probe thr_target: expired without an answer.", ce.result())
	assert.Equal(t, confirmExpired, ce.state(id))
	assert.Empty(t, pending(t, th2, asst.ID))
	assert.Zero(t, ce.execs.Load())
}
