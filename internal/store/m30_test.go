package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestM30SandboxesMigrationDownUp 覆盖 0064：回滚到 63 后 sandboxes 表与 orbs.sandbox_network 列消失，重新应用后恢复。
func TestM30SandboxesMigrationDownUp(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	p := githubProvider(t, s)
	const (
		tables = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sandboxes'`
		cols   = `SELECT COUNT(*) FROM pragma_table_info('orbs') WHERE name = 'sandbox_network'`
	)
	assert.Equal(t, 1, queryInt(ctx, t, s, tables))
	assert.Equal(t, 1, queryInt(ctx, t, s, cols))

	_, err := p.DownTo(ctx, 63)
	require.NoError(t, err)
	assert.Zero(t, queryInt(ctx, t, s, tables), "0064 回滚后 sandboxes 表消失")
	assert.Zero(t, queryInt(ctx, t, s, cols), "0064 回滚后 orbs.sandbox_network 列消失")

	_, err = p.Up(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, queryInt(ctx, t, s, tables))
	assert.Equal(t, 1, queryInt(ctx, t, s, cols))
}

// TestM30JobsSetNetworkMigration 覆盖 0065：jobs.op 接受 set_network，回滚到 64 后删掉这类任务并恢复原 CHECK，其余任务保留。
func TestM30JobsSetNetworkMigration(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	p := githubProvider(t, s)
	exec := func(q string, args ...any) error {
		_, err := s.DB.ExecContext(ctx, q, args...)
		return err
	}
	require.NoError(t, exec(`INSERT INTO providers (id, name, token_hash, created_at) VALUES ('prv-1', 'p', x'01', 1)`))
	insert := func(id, op string) error {
		return exec(`INSERT INTO jobs (id, provider_id, op, created_at) VALUES (?, 'prv-1', ?, 1)`, id, op)
	}
	require.NoError(t, insert("j-spawn", "spawn"))
	require.NoError(t, insert("j-net", "set_network"))
	assert.Error(t, insert("j-bad", "reboot"))

	_, err := p.DownTo(ctx, 64)
	require.NoError(t, err)
	assert.Equal(t, 1, queryInt(ctx, t, s, `SELECT COUNT(*) FROM jobs`), "回滚删掉 set_network 任务，保留其余")
	assert.Error(t, insert("j-net2", "set_network"))

	_, err = p.Up(ctx)
	require.NoError(t, err)
	require.NoError(t, insert("j-net3", "set_network"))
	assert.Equal(t, 0, queryInt(ctx, t, s, `SELECT claim_gen FROM jobs WHERE id = 'j-net3'`))
}
