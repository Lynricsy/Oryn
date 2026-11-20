package threads

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orb 跟随线程回收：共用 orb 的线程删掉一个不删 orb，删掉最后一个才删。
func TestDeleteThreadReleasesOrb(t *testing.T) {
	f := newFixture(t, Config{})
	orb := mkOrbRow(f.ctx, f.st, "ready")
	a, err := f.s.Create(f.ctx, CreateParams{Prompt: "a", OrbID: orb})
	require.NoError(t, err)
	b, err := f.s.Create(f.ctx, CreateParams{Prompt: "b", OrbID: orb})
	require.NoError(t, err)

	require.NoError(t, f.s.Delete(f.ctx, a.ID))
	f.orbs.mu.Lock()
	assert.Empty(t, f.orbs.deletes, "另一个线程仍在用这台 orb")
	f.orbs.mu.Unlock()

	require.NoError(t, f.s.Delete(f.ctx, b.ID))
	f.orbs.mu.Lock()
	assert.Equal(t, []string{orb}, f.orbs.deletes)
	f.orbs.mu.Unlock()
}

// 预检通过之后 orb 进入删除（最后一个线程刚被删掉）：写线程行的事务复核到，挂载失败，不会落在被回收的 orb 上。
func TestAttachRechecksOrbDeletingInTx(t *testing.T) {
	f := newFixture(t, Config{})
	orb := mkOrbRow(f.ctx, f.st, "ready")
	f.s.afterSpacePrecheck = func() {
		_, err := f.st.DB.ExecContext(f.ctx, `UPDATE orbs SET state = 'deleting' WHERE id = ?`, orb)
		require.NoError(t, err)
	}
	_, err := f.s.Create(f.ctx, CreateParams{Prompt: "x", OrbID: orb})
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "not_found", te.Code)
	n, err := f.st.Q.OrbThreadCounts(f.ctx, orb)
	require.NoError(t, err)
	assert.Zero(t, n.Total)
}
