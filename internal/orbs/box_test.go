package orbs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// boxOrb 直接以给定状态写一台 box 的 orb。
func (b *bare) boxOrb(state string, changedAt int64) db.Orb {
	b.t.Helper()
	ctx := context.Background()
	id := ids.Orb()
	require.NoError(b.t, b.st.Q.CreateOrb(ctx, db.CreateOrbParams{
		ID: id, ProviderID: b.provID, Type: "small", State: state, StateChangedAt: changedAt,
		TokenHash: ids.Hash(orbToken(b.svc.cfg.TokenKey, id, 0)), CreatedAt: 1, Box: new(int64(1)),
	}))
	require.NoError(b.t, b.st.Q.InsertOrbEvent(ctx, db.InsertOrbEventParams{OrbID: id, Ts: changedAt, FromState: "ready", ToState: state, Detail: "{}"}))
	orb, err := b.st.Q.GetOrb(ctx, id)
	require.NoError(b.t, err)
	return orb
}

// M23 1.1：Create 带 Box 写入标记，View 带 box；box 不属于项目、不来自快照或预热池。
func TestCreateBoxOrb(t *testing.T) {
	ctx := context.Background()
	b := newBare(t, func(c *Config) { c.Defaults.Type = "small" })
	orb, err := b.svc.Create(ctx, CreateParams{Box: true})
	require.NoError(t, err)
	assert.EqualValues(t, 1, orb.Box)
	v, err := View(ctx, b.st.Q, orb)
	require.NoError(t, err)
	require.NotNil(t, v.Box)
	assert.True(t, *v.Box)
	for _, p := range []CreateParams{
		{Box: true, ProjectID: "prj_x"},
		{Box: true, SnapshotID: "snap_x"},
		{Box: true, SnapshotOf: "orb-x"},
		{Box: true, Pool: true},
	} {
		_, err = b.svc.Create(ctx, p)
		require.ErrorIs(t, err, ErrInvalidState, "%+v", p)
	}
	plain, err := b.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	v, err = View(ctx, b.st.Q, plain)
	require.NoError(t, err)
	assert.Nil(t, v.Box, "普通 orb 省略 box")
}

// box 的 orb 过了空闲阈值不冻结，线程全部归档也不算退役，DeleteUnused 不删它；普通 orb 作对照。
func TestBoxOrbIsAlwaysOn(t *testing.T) {
	ctx := context.Background()
	b := newBare(t, func(c *Config) {
		c.Idle = config.Idle{AgentIdle: config.Duration(time.Millisecond), UserIdle: config.Duration(time.Millisecond), StopAfterFrozen: config.Duration(time.Millisecond)}
	})
	old := time.Now().Add(-time.Hour).UnixMilli()
	box := b.boxOrb(StateReady, old)
	frozenBox := b.boxOrb(StateFrozen, old)
	plain := b.orb(StateReady, old)
	b.thread(box.ID, true)

	b.svc.EvaluateIdle(ctx)
	assert.Equal(t, StateFreezing, b.state(plain.ID), "对照：阈值在生效")
	assert.Equal(t, StateReady, b.state(box.ID), "box 不冻结")
	assert.Equal(t, StateFrozen, b.state(frozenBox.ID), "box 不因冻结过久停机")
	assert.Empty(t, b.pending(box.ID))

	assert.False(t, b.svc.retired(ctx, box), "线程全部归档的 box 不算退役")

	deleted, err := b.svc.DeleteUnused(ctx, box.ID, "")
	require.NoError(t, err)
	assert.False(t, deleted)
	assert.Equal(t, StateReady, b.state(box.ID))
}

// 闸门关着时 Wake 返回 ErrBoxStopped、BoxStopped 为 true；普通 orb 与没有闸门时不受影响。
func TestBoxGate(t *testing.T) {
	ctx := context.Background()
	b := newBare(t, nil)
	box := b.boxOrb(StateStopped, 1)
	plain := b.orb(StateStopped, 1)

	assert.False(t, b.svc.BoxStopped(ctx, box.ID), "没有闸门")
	allowed := false
	b.svc.SetBoxGate(func(orbID string) bool { return allowed })

	assert.True(t, b.svc.BoxStopped(ctx, box.ID))
	assert.False(t, b.svc.BoxStopped(ctx, plain.ID), "不是 box")
	assert.False(t, b.svc.BoxStopped(ctx, "orb-missing"), "读不到 orb")
	require.ErrorIs(t, b.svc.Wake(ctx, box.ID), ErrBoxStopped)
	assert.Equal(t, StateStopped, b.state(box.ID))
	assert.Empty(t, b.pending(box.ID))

	require.NoError(t, b.svc.Wake(ctx, plain.ID), "普通 orb 不看闸门")
	assert.Equal(t, StateWaking, b.state(plain.ID))

	allowed = true
	assert.False(t, b.svc.BoxStopped(ctx, box.ID))
	require.NoError(t, b.svc.Wake(ctx, box.ID))
	assert.Equal(t, StateWaking, b.state(box.ID))
}

// Revive 把 ready 的 orb 送回 waking 并恰好入队一个 spawn；状态已变时无操作。
func TestReviveBoxOrb(t *testing.T) {
	ctx := context.Background()
	b := newBare(t, nil)
	box := b.boxOrb(StateReady, 1)

	require.NoError(t, b.svc.Revive(ctx, box.ID))
	assert.Equal(t, StateWaking, b.state(box.ID))
	assert.Equal(t, []string{"spawn:"}, b.pending(box.ID))
	ev, err := b.st.Q.GetLatestOrbStateChange(ctx, box.ID)
	require.NoError(t, err)
	assert.Equal(t, StateReady, ev.FromState)
	assert.Equal(t, StateWaking, ev.ToState)
	assert.JSONEq(t, `{"reason":"link lost"}`, ev.Detail)

	require.NoError(t, b.svc.Revive(ctx, box.ID), "已不是 ready：无操作")
	assert.Equal(t, []string{"spawn:"}, b.pending(box.ID))
}

// 协调 orb（全局助手）常驻：过了空闲阈值不冻结，冻结状态下也不因冻结过久停机；普通 orb 作对照。
func TestCoordinatorOrbIsAlwaysOn(t *testing.T) {
	ctx := context.Background()
	b := newBare(t, func(c *Config) {
		c.Idle = config.Idle{AgentIdle: config.Duration(time.Millisecond), UserIdle: config.Duration(time.Millisecond), StopAfterFrozen: config.Duration(time.Millisecond)}
	})
	old := time.Now().Add(-time.Hour).UnixMilli()
	coord := b.orb(StateReady, old)
	frozen := b.orb(StateFrozen, old)
	for _, id := range []string{coord.ID, frozen.ID} {
		_, err := b.st.DB.ExecContext(ctx, `UPDATE orbs SET coordinator = 1 WHERE id = ?`, id)
		require.NoError(t, err)
	}
	plain := b.orb(StateReady, old)

	b.svc.EvaluateIdle(ctx)
	assert.Equal(t, StateFreezing, b.state(plain.ID), "对照：阈值在生效")
	assert.Equal(t, StateReady, b.state(coord.ID), "协调 orb 不冻结")
	assert.Equal(t, StateFrozen, b.state(frozen.ID), "协调 orb 不因冻结过久停机")
	assert.Empty(t, b.pending(coord.ID))
}
