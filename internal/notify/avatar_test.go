package notify

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func TestNotificationAvatarResolution(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.st.Q.CreateOwner(f.ctx, db.CreateOwnerParams{
		DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1,
	}))
	// 设置默认 Agent 头像，验证全局助手头像不跟随 Agent 身份（与客户端 AssistantMark 一致）
	_, err := f.st.Q.SetOwnerAgentIdentity(f.ctx, db.SetOwnerAgentIdentityParams{
		AgentName:       new("Agent"),
		AgentAvatarIcon: new("🤖"),
	})
	require.NoError(t, err)

	// 1. 普通线程：Emoji 标题卡 + 线程色
	emojiThread := f.thread("🐳 DOCKER: Speed up builds")
	f.exec(`UPDATE threads SET color = 'blue' WHERE id = ?`, emojiThread)

	// 2. 普通线程：Nerd Font 标题卡（无线程色）
	nerdIcon := "\U000f0868"
	nerdThread := f.thread(nerdIcon + " BUILD: Fix CI")

	// 3. 普通线程：无标题卡图标
	plainThread := f.thread("普通任务")

	// 4. 全局助手主线（动态）与全局助手普通对话线程
	assistantMain := f.thread("Main")
	f.exec(`UPDATE threads SET kind = 'assistant', main = 1 WHERE id = ?`, assistantMain)
	assistantChat := f.thread("✨ CHAT: 周报整理")
	f.exec(`UPDATE threads SET kind = 'assistant', main = 0 WHERE id = ?`, assistantChat)

	f.done(emojiThread)
	f.clock.Advance(time.Second)
	f.done(nerdThread)
	f.clock.Advance(time.Second)
	f.done(plainThread)
	f.clock.Advance(time.Second)
	f.done(assistantMain)
	f.clock.Advance(time.Second)
	f.done(assistantChat)
	f.clock.Advance(time.Second)
	require.NoError(t, f.s.PostSystem(f.ctx, KindBudget, "Budget exceeded", "body", "/stats/usage"))
	f.clock.Advance(time.Second)
	require.NoError(t, f.s.PostSystem(f.ctx, KindBudget, "Research paused", "body", "/settings/assistant"))

	list, err := f.s.List(f.ctx, 20, 0, false)
	require.NoError(t, err)
	byThread := map[string]apigen.Notification{}
	byURL := map[string]apigen.Notification{}
	for _, n := range list.Notifications {
		if n.ThreadID != nil {
			byThread[*n.ThreadID] = n
		} else {
			byURL[n.URL] = n
		}
	}

	// 普通线程：Emoji + 蓝色
	av := byThread[emojiThread].Avatar
	require.NotNil(t, av)
	assert.Equal(t, apigen.NotificationAvatarKindThread, av.Kind)
	require.NotNil(t, av.Icon)
	assert.Equal(t, "🐳", *av.Icon)
	require.NotNil(t, av.Color)
	assert.Equal(t, "blue", *av.Color)
	assert.Nil(t, av.Hash)

	// 普通线程：Nerd Font 保留在 avatar.icon 里，推送载荷 title 仍去 Nerd 图标以兼容旧端
	av = byThread[nerdThread].Avatar
	require.NotNil(t, av)
	assert.Equal(t, apigen.NotificationAvatarKindThread, av.Kind)
	require.NotNil(t, av.Icon)
	assert.Equal(t, nerdIcon, *av.Icon)
	assert.Nil(t, av.Color)

	// 普通线程：无标题卡图标
	av = byThread[plainThread].Avatar
	require.NotNil(t, av)
	assert.Equal(t, apigen.NotificationAvatarKindThread, av.Kind)
	assert.Nil(t, av.Icon)

	// 全局助手主线 & 对话：未自定义助手头像 → assistant + 空 icon/hash（不跟随 Agent 身份 🤖）
	for _, tid := range []string{assistantMain, assistantChat} {
		av := byThread[tid].Avatar
		require.NotNil(t, av)
		assert.Equal(t, apigen.NotificationAvatarKindAssistant, av.Kind)
		assert.Nil(t, av.Icon)
		assert.Nil(t, av.Hash)
	}

	// 系统通知：预算告警 → system；后台研究预算告警 (/settings/assistant) → assistant
	require.NotNil(t, byURL["/stats/usage"].Avatar)
	assert.Equal(t, apigen.NotificationAvatarKindSystem, byURL["/stats/usage"].Avatar.Kind)
	require.NotNil(t, byURL["/settings/assistant"].Avatar)
	assert.Equal(t, apigen.NotificationAvatarKindAssistant, byURL["/settings/assistant"].Avatar.Kind)

	// 全局助手设置字形头像 → icon + hash 随通知与推送下发
	_, err = f.st.Q.SetOwnerAssistantIdentity(f.ctx, db.SetOwnerAssistantIdentityParams{
		AssistantName:       new("酒狐"),
		AssistantAvatarIcon: new("🦊"),
	})
	require.NoError(t, err)
	f.clock.Advance(time.Second)
	f.done(assistantMain)
	notes := f.out.Notes()
	lastNote := notes[len(notes)-1]
	require.NotNil(t, lastNote.Avatar)
	assert.Equal(t, apigen.NotificationAvatarKindAssistant, lastNote.Avatar.Kind)
	require.NotNil(t, lastNote.Avatar.Icon)
	assert.Equal(t, "🦊", *lastNote.Avatar.Icon)
	require.NotNil(t, lastNote.Avatar.Hash)
	assert.Len(t, *lastNote.Avatar.Hash, 8)
	iconHash := *lastNote.Avatar.Hash

	// 全局助手改为图片头像 → icon 为空、hash 变化且推送载荷不内联大图
	imgDataURL := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	_, err = f.st.Q.SetOwnerAssistantIdentity(f.ctx, db.SetOwnerAssistantIdentityParams{
		AssistantName:        new("酒狐"),
		AssistantAvatarImage: &imgDataURL,
	})
	require.NoError(t, err)
	f.clock.Advance(time.Second)
	f.done(assistantChat)
	rawRows := f.notifications()
	msg := f.s.message(f.ctx, rawRows[0], db.NotificationDelivery{})
	require.NotNil(t, msg.Avatar)
	assert.Equal(t, apigen.NotificationAvatarKindAssistant, msg.Avatar.Kind)
	assert.Nil(t, msg.Avatar.Icon)
	require.NotNil(t, msg.Avatar.Hash)
	assert.NotEqual(t, iconHash, *msg.Avatar.Hash)

	payload, err := pushPayload(msg)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	assert.Equal(t, float64(msg.CreatedAt), decoded["ts"])
	avMap, ok := decoded["avatar"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", avMap["kind"])
	assert.Equal(t, *msg.Avatar.Hash, avMap["hash"])
	assert.NotContains(t, avMap, "icon")
	assert.NotContains(t, string(payload), "data:image/png")

	// Nerd Font 普通线程的推送载荷：title 去掉了 Nerd 图标，但 avatar.icon 保留原始 Nerd 字形
	for _, r := range rawRows {
		if r.ThreadID != nil && *r.ThreadID == nerdThread {
			nm := f.s.message(f.ctx, r, db.NotificationDelivery{})
			pb, err := pushPayload(nm)
			require.NoError(t, err)
			var pd map[string]any
			require.NoError(t, json.Unmarshal(pb, &pd))
			assert.Equal(t, "BUILD: Fix CI", pd["title"])
			pav := pd["avatar"].(map[string]any)
			assert.Equal(t, "thread", pav["kind"])
			assert.Equal(t, nerdIcon, pav["icon"])
		}
	}
}
