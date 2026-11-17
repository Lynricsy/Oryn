package threads

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M18 6.5：全自动入口建的线程其 orb 标为全自动；普通线程不标。
func TestCreateOrbAutomation(t *testing.T) {
	f := newFixture(t, Config{})
	f.s.SetGitHub(stubAutomation{slot: "agent"})
	_, err := f.s.Create(f.ctx, CreateParams{Prompt: "auto", Automation: true})
	require.NoError(t, err)
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "manual"})
	require.NoError(t, err)
	f.orbs.mu.Lock()
	defer f.orbs.mu.Unlock()
	require.Len(t, f.orbs.creates, 2)
	assert.True(t, f.orbs.creates[0].Automation)
	assert.False(t, f.orbs.creates[1].Automation)
}

// M18 6.5：orb 内派生的子线程沿用父线程 orb 的全自动标记，即使不带 Automation；父 orb 不是全自动时子线程也不是。
func TestCreateOrbAutomationInheritsParent(t *testing.T) {
	f := newFixture(t, Config{})
	prov, err := f.st.Q.GetOrb(f.ctx, f.orb("ready"))
	require.NoError(t, err)
	autoOrb := ids.Orb()
	require.NoError(t, f.st.Q.CreateOrb(f.ctx, db.CreateOrbParams{ID: autoOrb, ProviderID: prov.ProviderID, Type: "medium", Image: "test",
		State: "ready", StateChangedAt: 1, TokenHash: ids.Hash("oot_" + autoOrb), CreatedAt: 1, Automation: new(true)}))
	autoParent := f.thread(autoOrb, StateIdle)
	plainParent := f.thread(f.orb("ready"), StateIdle)

	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "child", ParentThreadID: autoParent.id})
	require.NoError(t, err)
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "child", ParentThreadID: plainParent.id})
	require.NoError(t, err)
	f.orbs.mu.Lock()
	defer f.orbs.mu.Unlock()
	require.Len(t, f.orbs.creates, 2)
	assert.True(t, f.orbs.creates[0].Automation)
	assert.False(t, f.orbs.creates[1].Automation)
}

// recordForkOrbs 记下 fork copy 新建 orb 的请求。
type recordForkOrbs struct {
	nopOrbs
	mu  sync.Mutex
	req []OrbCreate
}

func (c *recordForkOrbs) Create(_ context.Context, p OrbCreate) (db.Orb, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.req = append(c.req, p)
	return db.Orb{ID: "orb-copy"}, nil
}

// M18 6.5：fork copy 新建的 orb 沿用源 orb 的全自动标记。
func TestForkCopyInheritsAutomation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	const src, snap, id = "orb-src", "oryn-snap-auto", "T-fork-auto"
	require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: src, ProviderID: "prov_1", Type: "small", State: "ready",
		StateChangedAt: 1, TokenHash: "t1", CreatedAt: 1, Automation: new(true)}))
	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: id, TitleSource: "auto", State: StateProvisioning, SessionDir: "~/.oryn/threads/" + id}))
	op := forkOp{Stage: forkStageCreateOrb, Src: "T-src", SrcOrb: src, SnapshotID: snap, Type: "small"}
	_, err = st.DB.Exec(`UPDATE threads SET fork_op = ? WHERE id = ?`, op.bytes(), id)
	require.NoError(t, err)

	orbs := &recordForkOrbs{}
	s := New(Config{DataDir: t.TempDir()}, st, orbs)
	t.Cleanup(s.Close)
	s.recoverForks(ctx)

	orbs.mu.Lock()
	defer orbs.mu.Unlock()
	require.Len(t, orbs.req, 1)
	assert.True(t, orbs.req[0].Automation)
	assert.Equal(t, snap, orbs.req[0].SnapshotID)
}
