package coord

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 备用助手对话：常驻检查备好一个 agent 已就绪、不进线程列表的空白对话；新对话直接认领它（同一个线程、首条消息照常
// 送达、此后出现在列表里），随后再补一个新的。
func TestSpareConversationClaimedByNewConversation(t *testing.T) {
	e := newEnv(t, false, func(c *Config) { c.KeepWarm = 20 * time.Millisecond })
	readySpare := func(not string) threads.Spare {
		var got threads.Spare
		require.Eventually(t, func() bool {
			sp, ok, err := e.th.FindSpare(e.ctx, e.coordOrb)
			if err != nil || !ok || !sp.Ready || sp.ID == not {
				return false
			}
			got = sp
			return true
		}, 30*time.Second, 10*time.Millisecond)
		return got
	}
	listed := func(id string) bool {
		all, err := e.th.List(e.ctx, threads.ListFilter{Kind: "all"})
		require.NoError(t, err)
		for _, th := range all {
			if th.ID == id {
				return true
			}
		}
		return false
	}
	sp := readySpare("")
	assert.False(t, listed(sp.ID), "认领前不进线程列表")
	// threadstest 的假 Wake 会多出 orb 就绪事件，刚建的备用对话可能被再启动一次（真实 orbs.Wake 对过渡中的 orb 什么也
	// 不做）：等起步噪声过去，再取一次就绪的备用对话
	time.Sleep(300 * time.Millisecond)
	sp = readySpare("")

	conv, err := e.c.CreateConversation(e.ctx, threads.CoordinatorParams{Prompt: "hello spare"})
	require.NoError(t, err)
	assert.Equal(t, sp.ID, conv.ID, "认领备用对话，不另起 agent")
	assert.True(t, listed(conv.ID), "认领后进线程列表")
	require.Eventually(t, func() bool {
		entries, err := e.st.Q.ListEntriesAfter(e.ctx, db.ListEntriesAfterParams{ThreadID: conv.ID, AfterSeq: 0, Limit: 100})
		require.NoError(t, err)
		for _, en := range entries {
			if strings.Contains(string(en.Payload), "hello spare") {
				return true
			}
		}
		return false
	}, 30*time.Second, 20*time.Millisecond, "首条消息送达")

	next := readySpare(conv.ID)
	assert.NotEqual(t, conv.ID, next.ID, "补了一个新的备用对话")
	assert.False(t, listed(next.ID))
}

// 没有就绪的备用对话（常驻关着）时照常新建。
func TestCreateConversationWithoutSpare(t *testing.T) {
	e := newEnv(t, false)
	conv, err := e.c.CreateConversation(e.ctx, threads.CoordinatorParams{Prompt: "hi"})
	require.NoError(t, err)
	_, ok, err := e.th.FindSpare(e.ctx, e.coordOrb)
	require.NoError(t, err)
	assert.False(t, ok)
	require.NotNil(t, conv.Kind)
	assert.EqualValues(t, threads.KindAssistant, *conv.Kind)
}
