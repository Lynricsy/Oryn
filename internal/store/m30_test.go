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
