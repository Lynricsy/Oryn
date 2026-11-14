package threads

import (
	"context"
	"sync"
	"testing"

	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kickOrbs 记录被关闭 link 的 orb。
type kickOrbs struct {
	nopOrbs
	mu     sync.Mutex
	kicked []string
}

func (k *kickOrbs) Kick(orbID, _ string) {
	k.mu.Lock()
	k.kicked = append(k.kicked, orbID)
	k.mu.Unlock()
}

func (k *kickOrbs) list() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.kicked...)
}

// 已删除的线程（orb 磁盘上留着会话目录、hello 仍报上来）只回 foreign_thread 让 orbd 放弃它，不关闭 link；
// 否则 orb 每次重连都被踢，永远到不了 ready。真正属于别的 orb 的线程仍关闭 link。
func TestOwnThreadDeletedIsDroppedNotKicked(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	orbs := &kickOrbs{}
	s := New(Config{DataDir: t.TempDir()}, st, orbs)
	t.Cleanup(s.Close)

	_, err = s.ownThread(ctx, "orb-a", "T-deleted")
	var ce *link.CtlError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "foreign_thread", ce.Code)
	assert.Empty(t, orbs.list(), "已删除的线程不应关闭 link")

	const other = "T-other"
	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: other, Title: "", TitleSource: "auto", State: StateIdle, SessionDir: sessionDirOf(other)}))
	th, err := s.get(ctx, other)
	require.NoError(t, err)
	th.mu.Lock()
	th.orbID = "orb-b"
	th.mu.Unlock()

	_, err = s.ownThread(ctx, "orb-a", other)
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "forbidden", ce.Code)
	assert.Equal(t, []string{"orb-a"}, orbs.list(), "别的 orb 的线程仍关闭 link")

	got, err := s.ownThread(ctx, "orb-b", other)
	require.NoError(t, err)
	assert.Equal(t, other, got.id)
}
