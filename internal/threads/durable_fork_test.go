package threads

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// forkCreateOrbs 记录 Create 调用次数（不应发生）。
type forkCreateOrbs struct {
	nopOrbs
	creates atomic.Int32
}

func (c *forkCreateOrbs) Create(context.Context, OrbCreate) (db.Orb, error) {
	c.creates.Add(1)
	return db.Orb{ID: "orb-dup"}, nil
}

// fork copy 在「以快照建好 orb、尚未绑定线程」时重启：恢复按快照 id 找回那台 orb 复用，不再建第二台。
func TestRecoverForkReusesUnboundSnapshotOrb(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	const src, snap, made = "orb-src", "oryn-snap-fork1", "orb-made"
	for _, o := range []db.CreateOrbParams{
		{ID: src, ProviderID: "prov_1", Type: "small", State: "ready", StateChangedAt: 1, TokenHash: "t1", CreatedAt: 1},
		// orbs.insertOrb 建快照 orb 时 snapshot_of = image = 快照 id
		{ID: made, ProviderID: "prov_1", Type: "small", Image: snap, SnapshotOf: new(snap), State: "requested", StateChangedAt: 2, TokenHash: "t2", CreatedAt: 2},
	} {
		require.NoError(t, st.Q.CreateOrb(ctx, o))
	}
	const id = "T-fork-copy"
	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: id, TitleSource: "auto", State: StateProvisioning, SessionDir: "~/.oryn/threads/" + id}))
	op := forkOp{Stage: forkStageCreateOrb, Src: "T-src", SrcOrb: src, SnapshotID: snap, Type: "small"}
	_, err = st.DB.Exec(`UPDATE threads SET fork_op = ? WHERE id = ?`, op.bytes(), id)
	require.NoError(t, err)

	orbs := &forkCreateOrbs{}
	s := New(Config{DataDir: t.TempDir()}, st, orbs)
	t.Cleanup(s.Close)
	s.recoverForks(ctx)

	assert.Zero(t, orbs.creates.Load(), "已建好的快照 orb 被复用")
	th, err := st.Q.GetThread(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, th.OrbID)
	assert.Equal(t, made, *th.OrbID)
}
