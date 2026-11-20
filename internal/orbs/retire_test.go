package orbs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// thread 在 orb 上建一个线程行；archived 为真时同时归档。
func (b *bare) thread(orbID string, archived bool) string {
	b.t.Helper()
	ctx := context.Background()
	id := ids.Thread()
	require.NoError(b.t, b.st.Q.CreateThread(ctx, db.CreateThreadParams{
		Kind: "work", ID: id, OrbID: &orbID, TitleSource: "auto", State: "idle", SessionDir: "/s/" + id, CreatedAt: 1, UpdatedAt: 1,
	}))
	if archived {
		_, err := b.st.DB.ExecContext(ctx, `UPDATE threads SET archived_at = 1 WHERE id = ?`, id)
		require.NoError(b.t, err)
	}
	return id
}

// 线程全部归档的 orb 不等空闲计时，经闸门立即冻结；还有未归档线程或没有线程的 orb 照常等空闲。
func TestArchivedThreadsFreezeOrb(t *testing.T) {
	b := newBare(t, nil) // 空闲时长 1 小时：只有归档能触发冻结
	ctx := context.Background()
	now := time.Now().UnixMilli()

	retired := b.orb(StateReady, now)
	b.thread(retired.ID, true)
	b.thread(retired.ID, true)
	mixed := b.orb(StateReady, now)
	b.thread(mixed.ID, true)
	b.thread(mixed.ID, false)
	bare := b.orb(StateReady, now)
	busy := b.orb(StateReady, now)
	b.thread(busy.ID, true)

	b.svc.SetQuiescence(func(orbID string, commit func() bool) bool {
		if orbID == busy.ID {
			return false // agent 仍在工作：闸门不放行
		}
		return commit()
	})
	b.svc.EvaluateIdle(ctx)

	assert.Equal(t, StateFreezing, b.state(retired.ID))
	assert.Equal(t, []string{"pause:freeze"}, b.pending(retired.ID))
	assert.Equal(t, StateReady, b.state(mixed.ID), "还有未归档的线程")
	assert.Equal(t, StateReady, b.state(bare.ID), "没有线程（新建尚未绑定、预热池）")
	assert.Equal(t, StateReady, b.state(busy.ID), "归档也要等闸门确认静止")
}

// orb 跟随线程回收：仍有线程（含已归档的）引用时不删；最后一个线程删除后删除；协调 orb 从不在这里删除。
func TestDeleteUnusedFollowsThreads(t *testing.T) {
	b := newBare(t, nil)
	ctx := context.Background()

	orb := b.orb(StateFrozen, 1)
	live := b.thread(orb.ID, false)
	archived := b.thread(orb.ID, true)

	for _, id := range []string{live, archived} {
		deleted, err := b.svc.DeleteUnused(ctx, orb.ID)
		require.NoError(t, err)
		assert.False(t, deleted, "仍有线程引用")
		assert.Equal(t, StateFrozen, b.state(orb.ID))
		_, err = b.st.Q.DeleteThread(ctx, id)
		require.NoError(t, err)
	}
	deleted, err := b.svc.DeleteUnused(ctx, orb.ID)
	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Equal(t, StateDeleting, b.state(orb.ID))
	assert.Equal(t, []string{"delete:"}, b.pending(orb.ID), "冻结的 orb 与状态变化同一事务入队 delete")

	coord := b.orb(StateReady, 1)
	_, err = b.st.DB.ExecContext(ctx, `UPDATE orbs SET coordinator = 1 WHERE id = ?`, coord.ID)
	require.NoError(t, err)
	deleted, err = b.svc.DeleteUnused(ctx, coord.ID)
	require.NoError(t, err)
	assert.False(t, deleted)
	assert.Equal(t, StateReady, b.state(coord.ID))
}
