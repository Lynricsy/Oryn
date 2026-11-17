package notify

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M19 步骤 3.1：主线对话的通知标题是全局助手的名字；未设置时跟随 agent 身份，都没有则为 Assistant。普通线程仍用线程标题。
func TestMainNotificationTitle(t *testing.T) {
	f := newFixture(t)
	main := f.thread("Main")
	f.exec(`UPDATE threads SET kind = 'assistant', main = 1 WHERE id = ?`, main)
	plain := f.thread("数数")

	f.done(main)
	f.done(plain)
	titles := map[string]string{}
	for _, n := range f.notifications() {
		titles[*n.ThreadID] = n.Title
	}
	assert.Equal(t, "Assistant", titles[main], "没有所有者行：回落到 Assistant")
	assert.Equal(t, "数数", titles[plain])

	require.NoError(t, f.st.Q.CreateOwner(f.ctx, db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	_, err := f.st.Q.SetOwnerAgentIdentity(f.ctx, db.SetOwnerAgentIdentityParams{AgentName: new("Lin")})
	require.NoError(t, err)
	f.clock.Advance(time.Second)
	f.done(main)
	assert.Equal(t, "Lin", f.notifications()[0].Title, "助手名字未设置：跟随 agent 身份")

	_, err = f.st.Q.SetOwnerAssistantIdentity(f.ctx, db.SetOwnerAssistantIdentityParams{AssistantName: new("小狐")})
	require.NoError(t, err)
	f.clock.Advance(time.Second)
	f.done(main)
	assert.Equal(t, "小狐", f.notifications()[0].Title)
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	_, err := f.st.DB.ExecContext(f.ctx, q, args...)
	require.NoError(f.t, err)
}
