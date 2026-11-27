package opsconf_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/opsconf"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func rows(t *testing.T, st *store.Store) []db.ConfigOverride {
	t.Helper()
	rs, err := st.Q.ListConfigOverrides(context.Background())
	require.NoError(t, err)
	return rs
}

func TestApplyAndRevert(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	base := config.DefaultOrynd()
	c, err := opsconf.New(ctx, st, base)
	require.NoError(t, err)
	assert.Equal(t, opsconf.State{Effective: base, YAML: base, Overridden: []string{}}, c.State())

	var calls []config.Orynd
	c.Subscribe(func(o config.Orynd) { calls = append(calls, o) })

	s, err := c.Apply(ctx, opsconf.Patch{"idle.agent_idle": new(int64(600000))})
	require.NoError(t, err)
	assert.Equal(t, 10*time.Minute, c.Current().Idle.AgentIdle.D())
	assert.Equal(t, []string{"idle.agent_idle"}, s.Overridden)
	assert.Equal(t, 10*time.Minute, s.Effective.Idle.AgentIdle.D())
	assert.Equal(t, base.Idle.AgentIdle, s.YAML.Idle.AgentIdle)
	assert.Positive(t, s.UpdatedAt)
	require.Len(t, calls, 1)
	assert.Equal(t, 10*time.Minute, calls[0].Idle.AgentIdle.D())

	// 同一个库重新加载仍是 10m
	c2, err := opsconf.New(ctx, st, base)
	require.NoError(t, err)
	assert.Equal(t, 10*time.Minute, c2.Current().Idle.AgentIdle.D())

	s, err = c.Apply(ctx, opsconf.Patch{"idle.agent_idle": nil})
	require.NoError(t, err)
	assert.Equal(t, base.Idle.AgentIdle, c.Current().Idle.AgentIdle)
	assert.NotNil(t, s.Overridden)
	assert.Empty(t, s.Overridden)
	assert.Zero(t, s.UpdatedAt)
	assert.Empty(t, rows(t, st))
}

func TestApplyBoolAndInvalid(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	base := config.DefaultOrynd()
	c, err := opsconf.New(ctx, st, base)
	require.NoError(t, err)

	_, err = c.Apply(ctx, opsconf.Patch{"idle.wake_on_focus": new(int64(0))})
	require.NoError(t, err)
	assert.False(t, c.Current().Idle.WakeOnFocus)
	assert.Equal(t, []db.ConfigOverride{{Key: "idle.wake_on_focus", Value: "false", UpdatedAt: rows(t, st)[0].UpdatedAt}}, rows(t, st))
	_, err = c.Apply(ctx, opsconf.Patch{"idle.wake_on_focus": nil})
	require.NoError(t, err)

	// 基线总量 40，单个 50 不合法
	before := c.Current()
	_, err = c.Apply(ctx, opsconf.Patch{"snapshots.max_snapshot_gb": new(int64(50))})
	require.ErrorIs(t, err, opsconf.ErrInvalid)
	assert.Equal(t, before, c.Current())
	assert.Empty(t, rows(t, st))

	_, err = c.Apply(ctx, opsconf.Patch{"bogus.key": new(int64(1))})
	require.ErrorIs(t, err, opsconf.ErrInvalid)
}

func TestNewDropsInvalidRows(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	for _, r := range []db.UpsertConfigOverrideParams{
		{Key: "snapshots.max_snapshot_gb", Value: "30", UpdatedAt: 1},
		{Key: "stats.retention.samples", Value: "1000", UpdatedAt: 2},
		{Key: "bogus.key", Value: "1", UpdatedAt: 3},
	} {
		require.NoError(t, st.Q.UpsertConfigOverride(ctx, r))
	}
	base := config.DefaultOrynd()
	c, err := opsconf.New(ctx, st, base)
	require.NoError(t, err)
	assert.Equal(t, 30, c.Current().Snapshots.MaxSnapshotGB)
	assert.Equal(t, base.Stats.Retention.Samples, c.Current().Stats.Retention.Samples)
	s := c.State()
	assert.Equal(t, []string{"snapshots.max_snapshot_gb"}, s.Overridden)
	assert.Equal(t, int64(1), s.UpdatedAt)
	assert.Equal(t, []db.ConfigOverride{{Key: "snapshots.max_snapshot_gb", Value: "30", UpdatedAt: 1}}, rows(t, st))
}

func TestNewDropsOverrideInvalidAgainstBase(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	require.NoError(t, st.Q.UpsertConfigOverride(ctx, db.UpsertConfigOverrideParams{Key: "snapshots.max_snapshot_gb", Value: "30", UpdatedAt: 1}))
	base := config.DefaultOrynd()
	base.Snapshots.MaxTotalGB = 20
	c, err := opsconf.New(ctx, st, base)
	require.NoError(t, err)
	assert.Equal(t, base.Snapshots.MaxSnapshotGB, c.Current().Snapshots.MaxSnapshotGB)
	assert.Empty(t, c.State().Overridden)
	assert.Empty(t, rows(t, st))
}

func TestParsePatch(t *testing.T) {
	ok := map[string]opsconf.Patch{
		`{"idle":{"agentIdleMs":null}}`:              {"idle.agent_idle": nil},
		`{"idle":{"wakeOnFocus":false}}`:             {"idle.wake_on_focus": new(int64(0))},
		`{"repos":{"maxMirrorGb":7},"snapshots":{}}`: {"repos.max_mirror_gb": new(int64(7))},
		`{"statsRetention":{"samplesMs":86400000}}`:  {"stats.retention.samples": new(int64(86400000))},
		`{}`: {},
	}
	for body, want := range ok {
		got, err := opsconf.ParsePatch([]byte(body))
		require.NoError(t, err, body)
		assert.Equal(t, want, got, body)
	}
	for _, body := range []string{
		`{"idle":{"agentIdleMs":1.5}}`,
		`{"nope":{}}`,
		`{"idle":{"x":1}}`,
		`[]`,
		`null`,
		`{"idle":null}`,
		`{"repos":{"maxMirrorGb":-1}}`,
		`{"repos":{"maxMirrorGb":"5"}}`,
		`{"repos":{"maxMirrorGb":1000001}}`,
		`{"idle":{"wakeOnFocus":1}}`,
		`{"idle":{"agentIdleMs":[1]}}`,
	} {
		_, err := opsconf.ParsePatch([]byte(body))
		assert.True(t, errors.Is(err, opsconf.ErrInvalid), body)
	}
}
