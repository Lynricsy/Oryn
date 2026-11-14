package threads

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// fixedProfile 是只返回固定 Profile 的 Profiles 替身。
type fixedProfile string

func (p fixedProfile) ResolveForNewOrb(context.Context, *string, string, apigen.BrowserProfileMode) (string, error) {
	return string(p), nil
}

func (f *fixture) project(name string) string {
	f.t.Helper()
	id := ids.New("prj")
	require.NoError(f.t, f.st.Q.CreateProject(f.ctx, db.CreateProjectParams{ID: id, Name: name, CreatedAt: 1, UpdatedAt: 1}))
	return id
}

// M11a 3.3：有项目、不选浏览器 Profile 的新线程先认领预热池 orb；认领成功就用它，不另建 orb。
func TestCreateClaimsPoolOrb(t *testing.T) {
	f := newFixture(t, Config{})
	prj := f.project("app")
	pool := f.orb("ready")
	f.orbs.mu.Lock()
	f.orbs.poolOrb = pool
	f.orbs.mu.Unlock()

	got, err := f.s.Create(f.ctx, CreateParams{Prompt: "hi", ProjectID: prj})
	require.NoError(t, err)
	require.NotNil(t, got.OrbID)
	assert.Equal(t, pool, *got.OrbID)
	f.orbs.mu.Lock()
	assert.Len(t, f.orbs.claims, 1)
	assert.Equal(t, prj, f.orbs.claims[0].ProjectID)
	assert.Empty(t, f.orbs.creates, "认领成功不另建 orb")
	f.orbs.mu.Unlock()

	// 池里没有可认领的：照常新建
	got, err = f.s.Create(f.ctx, CreateParams{Prompt: "hi", ProjectID: prj})
	require.NoError(t, err)
	assert.NotEqual(t, pool, *got.OrbID)
	f.orbs.mu.Lock()
	assert.Len(t, f.orbs.claims, 2)
	assert.Len(t, f.orbs.creates, 1)
	f.orbs.mu.Unlock()

	// 无项目：不认领
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "hi"})
	require.NoError(t, err)
	f.orbs.mu.Lock()
	assert.Len(t, f.orbs.claims, 2)
	f.orbs.mu.Unlock()
}

// 选了浏览器 Profile 的线程不认领池 orb（Profile 须在 ready 之前装载）。
func TestCreateWithProfileSkipsPool(t *testing.T) {
	f := newFixture(t, Config{})
	f.s.SetProfiles(fixedProfile("bpf_x"))
	prj := f.project("app")
	f.orbs.mu.Lock()
	f.orbs.poolOrb = f.orb("ready")
	f.orbs.mu.Unlock()
	_, err := f.s.Create(f.ctx, CreateParams{Prompt: "hi", ProjectID: prj, BrowserProfileID: new("bpf_x")})
	require.NoError(t, err)
	f.orbs.mu.Lock()
	defer f.orbs.mu.Unlock()
	assert.Empty(t, f.orbs.claims)
	require.Len(t, f.orbs.creates, 1)
	assert.Equal(t, "bpf_x", f.orbs.creates[0].BrowserProfileID)
}

// executor.kind = orb 指向预热池 orb → orb_is_pooled。
func TestCreateOnPooledOrbRejected(t *testing.T) {
	f := newFixture(t, Config{})
	id := f.orb("ready")
	n, err := f.st.DB.Exec(`UPDATE orbs SET pool_state = 'idle' WHERE id = ?`, id)
	require.NoError(t, err)
	rows, _ := n.RowsAffected()
	require.EqualValues(t, 1, rows)
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "hi", OrbID: id})
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeOrbIsPooled, te.Code)

	// 离开池之后可以挂
	_, err = f.st.DB.Exec(`UPDATE orbs SET pool_state = NULL WHERE id = ?`, id)
	require.NoError(t, err)
	got, err := f.s.Create(f.ctx, CreateParams{Prompt: "hi", OrbID: id})
	require.NoError(t, err)
	assert.Equal(t, id, *got.OrbID)
}
