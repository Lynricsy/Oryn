package threads

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 备用助手对话认领之前对用户不可见：建它时不推 index.patch、状态变化也不推，列表里没有；FindSpare 找得到它，
// agent 没就绪时认领返回 ErrSpareUnavailable（调用方改走新建）。只能是不带首条消息的普通助手对话。
func TestSpareConversationHidden(t *testing.T) {
	f := newFixture(t, Config{})
	sink := &indexSink{}
	f.s.SetSink(sink)
	orb := f.orb("ready")

	_, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Hidden: true, Prompt: "x"})
	require.Error(t, err, "备用对话不带首条消息")
	_, err = f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindSpace, OrbID: orb, Hidden: true})
	require.Error(t, err)

	sp, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Hidden: true})
	require.NoError(t, err)
	th, err := f.s.get(f.ctx, sp.ID)
	require.NoError(t, err)
	th.mu.Lock()
	f.s.publish(f.ctx, sp.ID)
	th.mu.Unlock()
	for _, u := range sink.snapshot() {
		assert.NotEqual(t, sp.ID, u.ID, "不推 index.patch")
	}
	all, err := f.s.List(f.ctx, ListFilter{Kind: "all"})
	require.NoError(t, err)
	for _, x := range all {
		assert.NotEqual(t, sp.ID, x.ID, "不进列表")
	}

	found, ok, err := f.s.FindSpare(f.ctx, orb)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, sp.ID, found.ID)
	assert.False(t, found.Ready, "agent 没就绪")
	_, err = f.s.ClaimSpare(f.ctx, sp.ID, CoordinatorParams{Prompt: "hi"})
	require.ErrorIs(t, err, ErrSpareUnavailable)

	visible, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb})
	require.NoError(t, err)
	var pushed bool
	for _, u := range sink.snapshot() {
		pushed = pushed || u.ID == visible.ID
	}
	assert.True(t, pushed, "对照：普通助手对话照常推送")
}

// readySpare 建一个备用助手对话，并让它像 agent 已起好、会话静止那样可认领。
func (f *fixture) readySpare(orb string, a *agentConn) *thread {
	f.t.Helper()
	sp, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Hidden: true})
	require.NoError(f.t, err)
	th, err := f.s.get(f.ctx, sp.ID)
	require.NoError(f.t, err)
	attach(th, a, apigen.AgentStatusReady)
	th.mu.Lock()
	th.state = StateIdle
	th.mu.Unlock()
	return th
}

// 认领与首批命令同一事务落库：认领返回成功后、agent 应答之前控制面重启，加载线程时按原顺序重发
// set_model → set_thinking_level → 首条消息；prompt 的应答之后才清除记录。
func TestClaimedSparePromptSurvivesRestart(t *testing.T) {
	f := newFixture(t, Config{})
	lib := &fakeLibrary{}
	f.s.SetLibrary(lib)
	th := f.readySpare(f.orb("ready"), fakeAgent(16))

	out, err := f.s.ClaimSpare(f.ctx, th.id, CoordinatorParams{Prompt: "hello spare", Model: "prov/m2", ThinkingLevel: "high", Files: []string{"afl_1"}})
	require.NoError(t, err)
	row, err := f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	assert.Zero(t, row.Hidden, "认领后可见")
	require.NotEmpty(t, row.StartupFrames, "应答之前首批命令已落库")
	require.Len(t, lib.binds, 1)
	assert.True(t, lib.binds[0].inTx, "文件与认领同一事务绑定")

	// 模拟控制面重启：同一数据库上的新服务
	f.s.Close()
	s2 := New(Config{DataDir: t.TempDir()}, f.st, f.orbs)
	t.Cleanup(s2.Close)
	th2, err := s2.get(f.ctx, out.ID)
	require.NoError(t, err)
	a := fakeAgent(16)
	attach(th2, a, apigen.AgentStatusReady)
	th2.mu.Lock()
	require.Len(t, th2.queue, 3)
	s2.onReadyLocked(f.ctx, th2)
	th2.mu.Unlock()
	frames := afterMark(t, sent(a))
	require.GreaterOrEqual(t, len(frames), 3)
	assert.Contains(t, frames[0], `"set_model"`)
	assert.Contains(t, frames[0], `"m2"`)
	assert.Contains(t, frames[1], `"set_thinking_level"`)
	assert.Contains(t, frames[2], `"message":"hello spare`)
	assert.Contains(t, frames[2], "~/library/threads/"+out.ID+"/afl_1/a.txt", "附件块随首条消息持久化")
	assert.NotContains(t, frames[2], orynFilesKey)

	f2 := &fixture{t: t, ctx: f.ctx, st: f.st, s: s2}
	f2.respond(th2, frames[0], true, "")
	row, err = f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, row.StartupFrames, "set_model 的应答不清除")
	f2.respond(th2, frames[2], true, `{"agentInvoked":true}`)
	row, err = f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	assert.Empty(t, row.StartupFrames, "agent 应答 prompt 后清除")
}

// 认领事务失败（文件库 id 不存在）：什么也没下发，备用对话仍隐藏、可认领。
func TestClaimSpareRollsBackOnBindFailure(t *testing.T) {
	f := newFixture(t, Config{})
	f.s.SetLibrary(&fakeLibrary{missing: map[string]bool{"afl_gone": true}})
	orb := f.orb("ready")
	a := fakeAgent(16)
	th := f.readySpare(orb, a)
	sent(a)

	_, err := f.s.ClaimSpare(f.ctx, th.id, CoordinatorParams{Prompt: "hi", Model: "prov/m2", Files: []string{"afl_gone"}})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrSpareUnavailable)
	assert.Empty(t, sent(a), "认领失败时不下发任何命令")
	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	assert.EqualValues(t, 1, row.Hidden)
	assert.Empty(t, row.StartupFrames)
	sp, ok, err := f.s.FindSpare(f.ctx, orb)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, th.id, sp.ID)
	assert.True(t, sp.Ready, "仍可认领")
}

// 只改模型的空对话认领：没有 prompt 时最后一条命令的应答清除记录（不留到每次重启都重发）。
func TestClaimSpareWithoutPromptClearsOnLastFrame(t *testing.T) {
	f := newFixture(t, Config{})
	a := fakeAgent(16)
	th := f.readySpare(f.orb("ready"), a)
	sent(a)

	out, err := f.s.ClaimSpare(f.ctx, th.id, CoordinatorParams{Model: "prov/m2", ThinkingLevel: "low"})
	require.NoError(t, err)
	frames := sent(a)
	require.Len(t, frames, 2)
	f.respond(th, frames[0], true, "")
	row, err := f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, row.StartupFrames)
	f.respond(th, frames[1], true, "")
	row, err = f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	assert.Empty(t, row.StartupFrames)
}
