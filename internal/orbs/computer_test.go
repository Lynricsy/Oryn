package orbs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M19 4.3：全库至多一台未删除的助手的电脑（插入事务内复核）；它不属于项目、不来自快照或预热池；View 带 computer；
// 旧的进入删除之后可以再建。
func TestCreateComputerOnlyOne(t *testing.T) {
	ctx := context.Background()
	b := newBare(t, func(c *Config) { c.Defaults.Type = "small" })

	orb, err := b.svc.Create(ctx, CreateParams{Computer: true})
	require.NoError(t, err)
	assert.EqualValues(t, 1, orb.Computer)
	assert.Nil(t, orb.ProjectID)
	assert.Nil(t, orb.PoolState)
	v, err := View(ctx, b.st.Q, orb)
	require.NoError(t, err)
	require.NotNil(t, v.Computer)
	assert.True(t, *v.Computer)

	_, err = b.svc.Create(ctx, CreateParams{Computer: true})
	require.ErrorIs(t, err, ErrInvalidState, "第二台被拒")
	for _, p := range []CreateParams{
		{Computer: true, ProjectID: "prj_x"},
		{Computer: true, SnapshotID: "snap_x"},
	} {
		_, err = b.svc.Create(ctx, p)
		require.ErrorIs(t, err, ErrInvalidState, "%+v", p)
	}
	plain, err := b.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	assert.EqualValues(t, 0, plain.Computer, "普通 orb 不是电脑")
	_, ok := b.svc.ClaimPool(ctx, CreateParams{Computer: true})
	assert.False(t, ok, "电脑不走预热池")

	require.NoError(t, b.st.Q.SetOrbState(ctx, db.SetOrbStateParams{State: StateDeleting, StateChangedAt: 2, ID: orb.ID}))
	again, err := b.svc.Create(ctx, CreateParams{Computer: true})
	require.NoError(t, err, "旧的进入删除之后可以再建")
	assert.EqualValues(t, 1, again.Computer)
}
