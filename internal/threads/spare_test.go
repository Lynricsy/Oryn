package threads

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
